package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

const appUser = "app-user"

type fakeIssue struct {
	linearIssue
	actor    string
	assignee string
	delegate string
	state    string
}

// givenToApp is whether a query's filter matches the issue as Linear would.
// The issue is the app's if the app is its assignee, or its delegate when the
// query asks for the delegate too.
func (issue *fakeIssue) givenToApp(query string) bool {
	return issue.assignee == appUser || (issue.delegate == appUser && strings.Contains(query, `delegate: {id: {eq: "app-user"}}`))
}

// fakeStates is every fake team's board. In Review is a second started state.
var fakeStates = []linearState{
	{ID: "s-backlog", Name: "Backlog", Type: "backlog", Position: 0},
	{ID: "s-todo", Name: "Todo", Type: "unstarted", Position: 1},
	{ID: "s-progress", Name: "In Progress", Type: "started", Position: 2},
	{ID: "s-review", Name: "In Review", Type: "started", Position: 3},
	{ID: "s-done", Name: "Done", Type: "completed", Position: 4},
}

func stateType(id string) string {
	for _, state := range fakeStates {
		if state.ID == id {
			return state.Type
		}
	}
	return ""
}

// fakeLinear answers the calls Machinist makes to Linear's GraphQL API.
type fakeLinear struct {
	mu       sync.Mutex
	issues   []fakeIssue
	comments map[string][]string
	limited  int
	// brokenMoves is how many issueUpdate calls fail before one works.
	brokenMoves int
	moves       int
	queries     []string
	auth        string
	// replies are comments people left, as Linear would list them.
	replies []fakeReply
	// endlessReplies makes a replies query with no upper time bound page on
	// for ever, as a huge backlog would.
	endlessReplies bool
}

type fakeReply struct {
	issue, user, body string
	at                time.Time
}

var createdInQuery = regexp.MustCompile(`createdAt: \{gt: "([^"]+)"`)
var untilInQuery = regexp.MustCompile(`lte: "([^"]+)"`)

// reply adds a comment that user left on an issue.
func (f *fakeLinear) reply(issue, user, body string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, fakeReply{issue: issue, user: user, body: body, at: at})
}

var sinceInQuery = regexp.MustCompile(`updatedAt: \{gt: "([^"]+)"\}`)

func (f *fakeLinear) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	var request struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&request)
	f.queries = append(f.queries, request.Query)
	if f.limited > 0 {
		f.limited--
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"errors":[{"message":"slow down","extensions":{"code":"RATELIMITED"}}]}`))
		return
	}
	answer := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": data}) }
	switch {
	case strings.Contains(request.Query, "issueUpdate"):
		if f.brokenMoves > 0 {
			f.brokenMoves--
			_, _ = w.Write([]byte(`{"errors":[{"message":"something broke"}]}`))
			return
		}
		f.moves++
		issue := f.issue(request.Variables["issue"].(string))
		issue.state = request.Variables["state"].(string)
		answer(map[string]any{"issueUpdate": map[string]any{"success": true}})
		return
	case strings.Contains(request.Query, "TeamStates"):
		answer(map[string]any{"issue": map[string]any{"team": map[string]any{"states": map[string]any{"nodes": fakeStates}}}})
		return
	case strings.Contains(request.Query, "query States"):
		nodes := []map[string]any{}
		for _, id := range request.Variables["ids"].([]any) {
			if issue := f.issue(id.(string)); issue != nil {
				node := map[string]any{"id": issue.ID, "state": map[string]any{"type": stateType(issue.state)}}
				if issue.assignee != "" {
					node["assignee"] = map[string]any{"id": issue.assignee}
				}
				if issue.delegate != "" && strings.Contains(request.Query, "delegate { id }") {
					node["delegate"] = map[string]any{"id": issue.delegate}
				}
				nodes = append(nodes, node)
			}
		}
		answer(map[string]any{"issues": map[string]any{"nodes": nodes}})
		return
	}
	if strings.Contains(request.Query, "query Replies") {
		since, _ := time.Parse(time.RFC3339Nano, createdInQuery.FindStringSubmatch(request.Query)[1])
		until := time.Time{}
		if match := untilInQuery.FindStringSubmatch(request.Query); match != nil {
			until, _ = time.Parse(time.RFC3339Nano, match[1])
		} else if f.endlessReplies {
			answer(map[string]any{"comments": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": true, "endCursor": "more"}}})
			return
		}
		nodes := []map[string]any{}
		// Linear lists the newest first.
		for index := len(f.replies) - 1; index >= 0; index-- {
			reply := f.replies[index]
			if issue := f.issue(reply.issue); issue == nil || !issue.givenToApp(request.Query) || !reply.at.After(since) || (!until.IsZero() && reply.at.After(until)) || reply.user == appUser {
				continue
			}
			node := map[string]any{"id": fmt.Sprintf("c%d", index), "body": reply.body, "createdAt": reply.at, "issue": map[string]any{"id": reply.issue}}
			if reply.user != "" {
				node["user"] = map[string]any{"id": reply.user}
			}
			nodes = append(nodes, node)
		}
		answer(map[string]any{"comments": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}})
		return
	}
	if strings.Contains(request.Query, "commentCreate") {
		issue, _ := request.Variables["issue"].(string)
		body, _ := request.Variables["body"].(string)
		f.comments[issue] = append(f.comments[issue], body)
		_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true}}}`))
		return
	}
	since, _ := time.Parse(time.RFC3339Nano, sinceInQuery.FindStringSubmatch(request.Query)[1])
	nodes := []linearIssue{}
	for _, issue := range f.issues {
		if issue.givenToApp(request.Query) && issue.UpdatedAt.After(since) {
			nodes = append(nodes, issue.linearIssue)
		}
	}
	// Linear lists the most recently updated first.
	for a, b := 0, len(nodes)-1; a < b; a, b = a+1, b-1 {
		nodes[a], nodes[b] = nodes[b], nodes[a]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}})
}

func (f *fakeLinear) issue(id string) *fakeIssue {
	for index := range f.issues {
		if f.issues[index].ID == id {
			return &f.issues[index]
		}
	}
	return nil
}

// set changes an issue as a person in Linear would.
func (f *fakeLinear) set(id string, change func(*fakeIssue)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.issue(id))
}

// news is Machinist's comments on an issue after it said it was on it.
func (f *fakeLinear) news(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []string
	for _, comment := range f.comments[id] {
		if comment != acknowledgement {
			kept = append(kept, comment)
		}
	}
	return kept
}

func (f *fakeLinear) stateOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.issue(id).state
}

// assign adds an issue that actor assigned to the app at the given time, as
// workspaces that assign apps directly do.
func (f *fakeLinear) assign(id, team, actor string, at time.Time) {
	f.add(id, team, actor, at, func(issue *fakeIssue, change *linearChange) {
		issue.assignee = appUser
		change.ToAssigneeID = appUser
	})
}

// delegate adds an issue that actor delegated to the app at the given time.
// The actor stays its assignee, as in Linear.
func (f *fakeLinear) delegate(id, team, actor string, at time.Time) {
	f.add(id, team, actor, at, func(issue *fakeIssue, change *linearChange) {
		issue.assignee = actor
		issue.delegate = appUser
		change.ToDelegate = &linearUser{ID: appUser}
	})
}

// add adds an issue that actor gave to the app at the given time. Someone
// earlier gave it to the app first. give sets how it was given.
func (f *fakeLinear) add(id, team, actor string, at time.Time, give func(*fakeIssue, *linearChange)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := fakeIssue{actor: actor, state: "s-todo"}
	issue.linearIssue = linearIssue{ID: id, Identifier: strings.ToUpper(team) + "-" + id, Title: "Fix " + id, Description: "The details of " + id, URL: "https://linear.app/x/issue/" + id, UpdatedAt: at}
	issue.Team.Key = team
	earlier := linearChange{CreatedAt: at.Add(-time.Hour), ActorID: "someone-earlier"}
	latest := linearChange{CreatedAt: at, ActorID: actor}
	give(&issue, &earlier)
	give(&issue, &latest)
	if actor != "" {
		issue.History.Nodes = append(issue.History.Nodes, earlier, latest)
	}
	f.issues = append(f.issues, issue)
}

type polling struct {
	poller *Poller
	linear *fakeLinear
	store  *tickets.Store
	now    time.Time
	log    *bytes.Buffer
}

func newPolling(t *testing.T) *polling {
	t.Helper()
	intake, _ := newIntake(t)
	intake.Presets = func(name string) bool { return name == "default" }
	fake := &fakeLinear{comments: map[string][]string{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	p := &polling{linear: fake, store: intake.Store, now: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), log: &bytes.Buffer{}}
	p.poller = &Poller{
		Tracker:        &Linear{Token: "lin_oauth_token", AppUserID: appUser, Teams: []string{"ENG"}, Endpoint: server.URL, Client: server.Client()},
		Rules:          TrackerRules{AllowedUsers: []string{"owain"}, Preset: "default", Teams: map[string]string{"ENG": "owner/repo"}},
		Intake:         intake,
		Store:          intake.Store,
		Interval:       time.Minute,
		MaxJobsPerHour: 20,
		Log:            p.log,
		now:            func() time.Time { return p.now },
	}
	intake.Store.SetClock(func() time.Time { return p.now })
	return p
}

func (p *polling) poll(t *testing.T) {
	t.Helper()
	if err := p.poller.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (p *polling) tickets(t *testing.T) []tickets.Ticket {
	t.Helper()
	listed, err := p.store.ListTickets(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	return listed
}

func TestAnAssignmentStartsOneJob(t *testing.T) {
	p := newPolling(t)
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.assign("a1", "ENG", "owain", p.now)
	p.poll(t)
	listed := p.tickets(t)
	if len(listed) != 1 {
		t.Fatalf("tickets = %+v", listed)
	}
	job := listed[0]
	if job.Source != "linear" || job.Repo != "owner/repo" || job.Agent != "default" || job.Title != "Fix a1" || job.CreatedBy != "linear" {
		t.Fatalf("job = %+v", job)
	}
	entries, _ := p.store.Entries(t.Context(), job.ID)
	if prompt := entries[0].Body; !strings.Contains(prompt, "The details of a1") || !strings.Contains(prompt, "https://linear.app/x/issue/a1") {
		t.Fatalf("prompt = %q", prompt)
	}
	if p.linear.auth != "Bearer lin_oauth_token" {
		t.Fatalf("authorization = %q", p.linear.auth)
	}
	var query string
	for _, sent := range p.linear.queries {
		if strings.Contains(sent, "query Assigned") {
			query = sent
		}
	}
	for _, want := range []string{`assignee: {id: {eq: "app-user"}}`, `team: {key: {in: ["ENG"]}}`, "toAssigneeId"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query lacks %s:\n%s", want, query)
		}
	}

	// The same issue again, a later edit to it, and a restart all start nothing.
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.mu.Lock()
	p.linear.issues[0].UpdatedAt = p.now
	p.linear.mu.Unlock()
	p.poll(t)
	restarted := &Poller{Tracker: p.poller.Tracker, Rules: p.poller.Rules, Intake: p.poller.Intake, Store: p.store, Interval: time.Minute, MaxJobsPerHour: 20, now: p.poller.now}
	if err := restarted.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if listed := p.tickets(t); len(listed) != 1 {
		t.Fatalf("tickets = %+v", listed)
	}
}

func TestADelegationStartsOneJob(t *testing.T) {
	p := newPolling(t)
	job := p.delegated(t)
	if job.Source != "linear" || job.Title != "Fix a1" {
		t.Fatalf("job = %+v", job)
	}
	if got := p.linear.stateOf("a1"); got != "s-progress" {
		t.Fatalf("issue state = %q", got)
	}
	var query string
	for _, sent := range p.linear.queries {
		if strings.Contains(sent, "query Assigned") {
			query = sent
		}
	}
	for _, want := range []string{`delegate: {id: {eq: "app-user"}}`, `assignee: {id: {eq: "app-user"}}`, "toDelegate { id }"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query lacks %s:\n%s", want, query)
		}
	}
	// Later polls and edits to the issue start nothing more.
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.set("a1", func(issue *fakeIssue) { issue.UpdatedAt = p.now })
	p.poll(t)
	if listed := p.tickets(t); len(listed) != 1 {
		t.Fatalf("tickets = %+v", listed)
	}
}

func TestOnlyAllowedUsersStartJobsByDelegating(t *testing.T) {
	p := newPolling(t)
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.delegate("stranger", "ENG", "mallory", p.now)
	p.poll(t)
	if listed := p.tickets(t); len(listed) != 0 {
		t.Fatalf("tickets = %+v", listed)
	}
	if want := `user "mallory" may not start jobs`; !strings.Contains(p.log.String(), want) {
		t.Fatalf("log lacks %q:\n%s", want, p.log)
	}
}

func TestTheFirstPollStartsNothingAlreadyAssigned(t *testing.T) {
	p := newPolling(t)
	p.linear.assign("old", "ENG", "owain", p.now.Add(-time.Hour))
	p.poll(t)
	if listed := p.tickets(t); len(listed) != 0 {
		t.Fatalf("tickets = %+v", listed)
	}
}

func TestOnlyAllowedUsersInConnectedTeamsStartJobs(t *testing.T) {
	p := newPolling(t)
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.assign("stranger", "ENG", "mallory", p.now)
	p.linear.assign("unknown", "ENG", "", p.now)
	p.linear.assign("elsewhere", "OPS", "owain", p.now)
	p.poll(t)
	if listed := p.tickets(t); len(listed) != 0 {
		t.Fatalf("tickets = %+v", listed)
	}
	for _, want := range []string{`user "mallory" may not start jobs`, "who assigned it is not known", `team "OPS" is not connected`} {
		if !strings.Contains(p.log.String(), want) {
			t.Fatalf("log lacks %q:\n%s", want, p.log)
		}
	}
}

func TestTheActorIsWhoMadeTheLatestAssignment(t *testing.T) {
	p := newPolling(t)
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	// someone-earlier assigned it first; owain assigned it again last.
	p.linear.assign("again", "ENG", "owain", p.now)
	p.poll(t)
	if listed := p.tickets(t); len(listed) != 1 {
		t.Fatalf("tickets = %+v", listed)
	}
}

func TestTheHourlyCapHoldsAnAssignmentForALaterPoll(t *testing.T) {
	p := newPolling(t)
	p.poller.MaxJobsPerHour = 1
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.assign("first", "ENG", "owain", p.now)
	p.linear.assign("second", "ENG", "owain", p.now.Add(time.Second))
	p.poll(t)
	if listed := p.tickets(t); len(listed) != 1 {
		t.Fatalf("tickets = %+v", listed)
	}
	p.now = p.now.Add(time.Hour)
	p.poll(t)
	if listed := p.tickets(t); len(listed) != 2 {
		t.Fatalf("the held assignment did not start: %+v", listed)
	}
}

func TestAPullRequestIsCommentedOnItsIssueOnce(t *testing.T) {
	p := newPolling(t)
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.assign("a1", "ENG", "owain", p.now)
	p.poll(t)
	job := p.tickets(t)[0]
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StatePRReady, PRNumber: 7}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.poll(t)
	if got := p.linear.news("a1"); len(got) != 1 || got[0] != "Machinist opened a pull request: https://github.com/owner/repo/pull/7" {
		t.Fatalf("comments = %v", got)
	}
}

func TestRateLimitsBackOffUpToFiveMinutes(t *testing.T) {
	p := newPolling(t)
	p.linear.limited = 4
	var waits []time.Duration
	ctx, cancel := context.WithCancel(t.Context())
	p.poller.sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if len(waits) == 6 {
			cancel()
			return false
		}
		return true
	}
	p.poller.Run(ctx)
	want := []time.Duration{2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute, time.Minute, time.Minute}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v", waits)
	}
	for index := range want {
		if waits[index] != want[index] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
	if !strings.Contains(p.log.String(), "rate limited; next poll in 2m0s") {
		t.Fatalf("log:\n%s", p.log)
	}
	if err := p.poller.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	p.linear.limited = 1
	if err := p.poller.Poll(t.Context()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestAPersonalAPIKeyIsSentAsItIs(t *testing.T) {
	p := newPolling(t)
	p.poller.Tracker.(*Linear).Token = "lin_api_key"
	p.poll(t)
	if p.linear.auth != "lin_api_key" {
		t.Fatalf("authorization = %q", p.linear.auth)
	}
}

// started polls until one job is going on issue a1, which owain assigned to
// the app, and returns it.
func (p *polling) started(t *testing.T) tickets.Ticket {
	t.Helper()
	return p.startedBy(t, p.linear.assign)
}

// delegated polls until one job is going on issue a1, which owain delegated
// to the app, and returns it.
func (p *polling) delegated(t *testing.T) tickets.Ticket {
	t.Helper()
	return p.startedBy(t, p.linear.delegate)
}

func (p *polling) startedBy(t *testing.T, give func(id, team, actor string, at time.Time)) tickets.Ticket {
	t.Helper()
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	give("a1", "ENG", "owain", p.now)
	p.poll(t)
	listed := p.tickets(t)
	if len(listed) != 1 {
		t.Fatalf("tickets = %+v", listed)
	}
	return listed[0]
}

func (p *polling) state(t *testing.T, id string) string {
	t.Helper()
	ticket, err := p.store.Ticket(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ticket.State
}

func TestPickingUpAnIssueMovesItToInProgressOnce(t *testing.T) {
	p := newPolling(t)
	p.started(t)
	if got := p.linear.stateOf("a1"); got != "s-progress" {
		t.Fatalf("state = %q", got)
	}
	p.poll(t)
	p.poll(t)
	if p.linear.moves != 1 {
		t.Fatalf("moves = %d", p.linear.moves)
	}
	if got := p.linear.comments["a1"]; len(got) != 1 || got[0] != acknowledgement {
		t.Fatalf("comments = %v", got)
	}
}

func TestAJobWithNoPullRequestHandsItsIssueBackWithTheResult(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateDone, Message: "Not enough to go on: which page has the broken toast?"}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.poll(t)
	if got := p.linear.stateOf("a1"); got != "s-todo" {
		t.Fatalf("state = %q", got)
	}
	want := "Machinist finished without a pull request: Not enough to go on: which page has the broken toast?\n\nReply here to give it more to go on, and it will run again."
	if got := p.linear.news("a1"); len(got) != 1 || got[0] != want {
		t.Fatalf("comments = %q", got)
	}
}

func TestAReplyRunsTheJobAgainWithTheComment(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateDone, Message: "Which page?"}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.reply("a1", appUser, "Machinist's own comment", p.now)
	p.linear.reply("a1", "stranger", "Delete everything", p.now.Add(time.Second))
	p.linear.reply("a1", "owain", "The checkout page.", p.now.Add(2*time.Second))
	p.poll(t)
	if got := p.state(t, job.ID); got != tickets.StateWorking {
		t.Fatalf("job state = %q", got)
	}
	entries, err := p.store.Entries(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	log := tickets.RenderLog(entries)
	if !strings.Contains(log, "The checkout page.") || strings.Contains(log, "Delete everything") || strings.Contains(log, "Machinist's own comment") {
		t.Fatalf("log:\n%s", log)
	}
	if got := p.linear.stateOf("a1"); got != "s-progress" {
		t.Fatalf("issue state = %q", got)
	}
	// Reading the same replies again starts nothing more.
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateDone, Message: "Fixed."}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	if got := p.state(t, job.ID); got != tickets.StateDone {
		t.Fatalf("job ran again on an old reply: %q", got)
	}
}

func TestAReplyOnADelegatedIssueRunsTheJobAgain(t *testing.T) {
	p := newPolling(t)
	job := p.delegated(t)
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateDone, Message: "Which page?"}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.reply("a1", "owain", "The checkout page.", p.now)
	p.poll(t)
	if got := p.state(t, job.ID); got != tickets.StateWorking {
		t.Fatalf("job state = %q", got)
	}
	entries, err := p.store.Entries(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if log := tickets.RenderLog(entries); !strings.Contains(log, "The checkout page.") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestAReplyWhileTheJobRunsWaitsForTheRunToEnd(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.reply("a1", "owain", "Also fix the footer.", p.now)
	p.poll(t)
	entries, err := p.store.Entries(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tickets.RenderLog(entries), "footer") {
		t.Fatal("a reply joined a running job")
	}
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StatePRReady, PRNumber: 7}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	if got := p.state(t, job.ID); got != tickets.StateWorking {
		t.Fatalf("job state = %q", got)
	}
	entries, _ = p.store.Entries(t.Context(), job.ID)
	if !strings.Contains(tickets.RenderLog(entries), "Also fix the footer.") {
		t.Fatal("the reply was lost")
	}
}

func TestAFailedJobMovesItsIssueBackWithTheReason(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed, Message: "The tests would not pass."}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.poll(t)
	if got := p.linear.stateOf("a1"); got != "s-todo" {
		t.Fatalf("state = %q", got)
	}
	if got := p.linear.news("a1"); len(got) != 1 || got[0] != "Machinist's job failed: The tests would not pass.\n\nReply here to tell it what to change, and it will run again." {
		t.Fatalf("comments = %v", got)
	}
}

func TestMovingOrUnassigningAnIssueCancelsItsJob(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeIssue)
		cancel bool
	}{
		{"unassigned", func(issue *fakeIssue) { issue.assignee = "" }, true},
		{"assigned to someone else", func(issue *fakeIssue) { issue.assignee = "owain" }, true},
		{"moved to Todo", func(issue *fakeIssue) { issue.state = "s-todo" }, true},
		{"moved to Done", func(issue *fakeIssue) { issue.state = "s-done" }, true},
		{"moved to In Review", func(issue *fakeIssue) { issue.state = "s-review" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := newPolling(t)
			job := p.started(t)
			p.poll(t)
			if got := p.state(t, job.ID); got != tickets.StateWorking {
				t.Fatalf("state before the change = %q", got)
			}
			p.linear.set("a1", test.change)
			p.poll(t)
			want := tickets.StateWorking
			if test.cancel {
				want = tickets.StateCancelled
			}
			if got := p.state(t, job.ID); got != want {
				t.Fatalf("state = %q, want %q", got, want)
			}
		})
	}
}

func TestRemovingTheDelegateCancelsItsJob(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeIssue)
		cancel bool
	}{
		{"delegate removed", func(issue *fakeIssue) { issue.delegate = "" }, true},
		{"delegated to another agent", func(issue *fakeIssue) { issue.delegate = "other-agent" }, true},
		{"assigned to another person", func(issue *fakeIssue) { issue.assignee = "someone-else" }, false},
		{"unassigned, still delegated", func(issue *fakeIssue) { issue.assignee = "" }, false},
		{"moved to Done", func(issue *fakeIssue) { issue.state = "s-done" }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := newPolling(t)
			job := p.delegated(t)
			p.poll(t)
			if got := p.state(t, job.ID); got != tickets.StateWorking {
				t.Fatalf("state before the change = %q", got)
			}
			p.linear.set("a1", test.change)
			p.poll(t)
			want := tickets.StateWorking
			if test.cancel {
				want = tickets.StateCancelled
			}
			if got := p.state(t, job.ID); got != want {
				t.Fatalf("state = %q, want %q", got, want)
			}
		})
	}
}

func TestAJobIsNotCancelledBeforeItsIssueReachedInProgress(t *testing.T) {
	p := newPolling(t)
	p.linear.brokenMoves = 1
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.assign("a1", "ENG", "owain", p.now)
	if err := p.poller.Poll(t.Context()); err == nil || !strings.Contains(err.Error(), "something broke") {
		t.Fatalf("err = %v", err)
	}
	job := p.tickets(t)[0]
	// The issue is still in Todo, because the move failed. That is not a
	// person moving it, so the job keeps going and the move is tried again.
	p.poll(t)
	if got := p.state(t, job.ID); got != tickets.StateWorking {
		t.Fatalf("state = %q", got)
	}
	if got := p.linear.stateOf("a1"); got != "s-progress" {
		t.Fatalf("issue state = %q", got)
	}
}

func TestAMergedPullRequestMovesItsIssueToDoneOnce(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StatePRReady, PRNumber: 7}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	if err := p.setPR(t, job.ID, tickets.PRMerged); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	moves := p.linear.moves
	p.poll(t)
	if got := p.linear.stateOf("a1"); got != "s-done" || p.linear.moves != moves {
		t.Fatalf("state = %q, moves = %d then %d", got, moves, p.linear.moves)
	}
	if got := p.linear.news("a1"); len(got) != 1 {
		t.Fatalf("comments = %v", got)
	}
}

func TestAClosedPullRequestIsCommentedOnItsIssue(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StatePRReady, PRNumber: 7}); err != nil {
		t.Fatal(err)
	}
	if err := p.setPR(t, job.ID, tickets.PRClosed); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.poll(t)
	want := []string{"Machinist opened a pull request: https://github.com/owner/repo/pull/7", "Machinist's pull request was closed without merging: https://github.com/owner/repo/pull/7"}
	if got := p.linear.news("a1"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("comments = %v", got)
	}
	if got := p.linear.stateOf("a1"); got != "s-progress" {
		t.Fatalf("state = %q", got)
	}
}

// setPR records what GitHub says about the job's pull request #7.
func (p *polling) setPR(t *testing.T, id, state string) error {
	t.Helper()
	listed, err := p.store.Ticket(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.store.SetPRState(t.Context(), listed, 7, state, p.now)
}

func TestRunningAMergedJobAgainMovesItsIssueBackToInProgress(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StatePRReady, PRNumber: 7}); err != nil {
		t.Fatal(err)
	}
	if err := p.setPR(t, job.ID, tickets.PRMerged); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	if got := p.linear.stateOf("a1"); got != "s-done" {
		t.Fatalf("state = %q", got)
	}
	if _, err := p.store.Rerun(t.Context(), job.ID, "owain"); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.poll(t)
	if got := p.linear.stateOf("a1"); got != "s-progress" {
		t.Fatalf("issue state = %q", got)
	}
	if got := p.state(t, job.ID); got != tickets.StateWorking {
		t.Fatalf("job state = %q", got)
	}
}

func TestRepliesAtTheSameMomentGoIntoOneRun(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	p.linear.reply("a1", "owain", "Comment from before the job.", job.CreatedAt.Add(-30*time.Second))
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateDone, Message: "Which page?"}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.reply("a1", "owain", "The checkout page.", p.now)
	p.linear.reply("a1", "owain", "And the cart.", p.now)
	p.poll(t)
	p.poll(t)
	entries, err := p.store.Entries(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	log := tickets.RenderLog(entries)
	if strings.Count(log, "The checkout page.") != 1 || strings.Count(log, "And the cart.") != 1 || strings.Contains(log, "before the job") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestRepliesWaitPastTheHourlyCap(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateDone, Message: "Which page?"}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.poller.MaxJobsPerHour = 1
	p.now = p.now.Add(time.Minute)
	p.linear.reply("a1", "owain", "The checkout page.", p.now)
	p.poll(t)
	if got := p.state(t, job.ID); got != tickets.StateDone {
		t.Fatalf("a reply ran past the cap: %q", got)
	}
	p.now = p.now.Add(time.Hour)
	p.poll(t)
	if got := p.state(t, job.ID); got != tickets.StateWorking {
		t.Fatalf("the reply was lost: %q", got)
	}
}

func TestAHugeReplyBacklogIsReadInShorterSpans(t *testing.T) {
	p := newPolling(t)
	job := p.started(t)
	if err := p.store.Apply(t.Context(), job.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateDone, Message: "Which page?"}); err != nil {
		t.Fatal(err)
	}
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.linear.reply("a1", "owain", "The checkout page.", p.now.Add(-30*time.Second))
	p.linear.mu.Lock()
	p.linear.endlessReplies = true
	p.linear.mu.Unlock()
	p.poll(t)
	p.now = p.now.Add(time.Minute)
	p.poll(t)
	if got := p.state(t, job.ID); got != tickets.StateWorking {
		t.Fatalf("the reply in the backlog was never read: %q", got)
	}
}
