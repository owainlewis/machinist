package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

// maxBackoff caps how long a failing tracker waits between polls.
const maxBackoff = 5 * time.Minute

// ErrRateLimited means the tracker asked Machinist to slow down.
var ErrRateLimited = errors.New("rate limited")

// ErrTooManyReplies means more replies arrived than one read returns. The
// poller asks again for a shorter span of time.
var ErrTooManyReplies = errors.New("too many replies to read at once")

// Assignment is an issue that was handed to Machinist in a tracker.
type Assignment struct {
	// Key is stable for the issue, such as linear:<issue id>. One issue starts
	// one job, ever.
	Key string
	// Issue is the tracker's ID for the issue, used to comment on it.
	Issue      string
	Identifier string
	Title      string
	Body       string
	URL        string
	Team       string
	// Project and ProjectID name the issue's project. Both are empty when it
	// has none.
	Project   string
	ProjectID string
	// Actor is the user who made the assignment. Empty means it could not be
	// found, and the assignment is ignored.
	Actor string
	// GivenAt is when the issue was last handed to Machinist. It is zero when
	// that could not be found.
	GivenAt   time.Time
	UpdatedAt time.Time
}

// Reply is a comment a person left on an issue that Machinist has a job for.
type Reply struct {
	// Key is the issue's assignment key, such as linear:<issue id>.
	Key string
	// ID is the tracker's ID for the comment. One comment is added once.
	ID        string
	Issue     string
	Author    string
	Body      string
	CreatedAt time.Time
}

// IssueState is what Machinist needs to know about an issue with a job going.
type IssueState struct {
	// Assigned is whether the issue is still assigned to Machinist.
	Assigned bool
	// Active is whether the issue is still in a started state, such as In
	// Progress.
	Active bool
}

// Tracker is one place work comes from, such as Linear. Machinist, not the
// agent, writes to it: the agent reads issue text that anyone in the tracker
// may have written, so it never gets a way to change the tracker.
type Tracker interface {
	// Name is the tracker's ticket source, such as linear.
	Name() string
	// Assignments returns issues assigned to Machinist and updated after
	// since, oldest first.
	Assignments(ctx context.Context, since time.Time) ([]Assignment, error)
	// Start moves an issue to In Progress and says Machinist is on it.
	Start(ctx context.Context, issue string) error
	// Replies returns every comment left by people, not Machinist, on issues
	// assigned to Machinist, created after since and, unless until is zero,
	// no later than until, oldest first. It fails with ErrTooManyReplies
	// rather than return part of them.
	Replies(ctx context.Context, since, until time.Time) ([]Reply, error)
	// Return moves an issue back out of In Progress with a comment, because
	// the job ended with no pull request and the next move is a person's.
	Return(ctx context.Context, issue, comment string) error
	// Comment posts body on an issue.
	Comment(ctx context.Context, issue, body string) error
	// Fail moves an issue back out of In Progress and says why.
	Fail(ctx context.Context, issue, reason string) error
	// Finish moves an issue to Done, because its pull request merged.
	Finish(ctx context.Context, issue string) error
	// States reads issues by ID. An issue missing from the answer is gone.
	States(ctx context.Context, issues []string) (map[string]IssueState, error)
}

// TrackerRules decide which assignments start jobs, and where.
type TrackerRules struct {
	// AllowedUsers may send work. An assignment by anyone else is ignored.
	AllowedUsers []string
	Preset       string
	// Projects maps a project name or ID to a repository. A project's mapping
	// wins over its team's.
	Projects map[string]string
	// Teams maps a team key to a repository, for issues whose project is not
	// mapped.
	Teams map[string]string
}

// Route returns the repository an assignment runs in, or the reason it has
// none.
func (rules TrackerRules) Route(assignment Assignment) (string, string) {
	// An ID is exact. A name matches in any case.
	if repository, ok := rules.Projects[assignment.ProjectID]; ok && assignment.ProjectID != "" {
		return repository, ""
	}
	for project, repository := range rules.Projects {
		if assignment.Project != "" && strings.EqualFold(project, assignment.Project) {
			return repository, ""
		}
	}
	for team, repository := range rules.Teams {
		if assignment.Team != "" && strings.EqualFold(team, assignment.Team) {
			return repository, ""
		}
	}
	if assignment.Project == "" {
		return "", fmt.Sprintf("team %q is not connected", assignment.Team)
	}
	return "", fmt.Sprintf("project %q and team %q are not connected", assignment.Project, assignment.Team)
}

// Decide returns the job an assignment starts, or the reason it starts none.
func (rules TrackerRules) Decide(assignment Assignment) (start, string) {
	repository, reason := rules.Route(assignment)
	if reason != "" {
		return start{}, reason
	}
	if assignment.Actor == "" {
		return start{}, "who assigned it is not known"
	}
	if !slices.Contains(rules.AllowedUsers, assignment.Actor) {
		return start{}, fmt.Sprintf("user %q may not start jobs", assignment.Actor)
	}
	return start{key: assignment.Key, preset: rules.Preset, repo: repository, prompt: trackerPrompt(assignment)}, ""
}

func trackerPrompt(assignment Assignment) string {
	prompt := strings.TrimSpace(assignment.Title)
	if body := strings.TrimSpace(assignment.Body); body != "" {
		prompt += "\n\n" + body
	}
	if assignment.Identifier != "" || assignment.URL != "" {
		prompt += fmt.Sprintf("\n\nIssue %s: %s", assignment.Identifier, assignment.URL)
	}
	return prompt
}

// Poller asks a tracker for new assignments on an interval and starts their
// jobs. Nothing calls Machinist from outside, so it needs no public route.
type Poller struct {
	Tracker Tracker
	Rules   TrackerRules
	Intake  *Intake
	Store   *tickets.Store
	// Interval is the time between polls while the tracker answers.
	Interval time.Duration
	// MaxJobsPerHour caps the jobs the tracker can start in any hour.
	MaxJobsPerHour int
	Log            io.Writer
	now            func() time.Time
	sleep          func(context.Context, time.Duration) bool

	mu      sync.Mutex
	started []time.Time
}

// Run polls at once, then after every interval until ctx ends. After a
// failure it waits twice as long as before, up to five minutes.
func (p *Poller) Run(ctx context.Context) {
	wait := p.Interval
	for {
		err := p.Poll(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			wait = min(max(wait*2, p.Interval), maxBackoff)
			fmt.Fprintf(p.log(), "machinist: %s: %v; next poll in %s\n", p.Tracker.Name(), err, wait)
		default:
			wait = p.Interval
		}
		if !p.wait(ctx, wait) {
			return
		}
	}
}

func (p *Poller) wait(ctx context.Context, d time.Duration) bool {
	if p.sleep != nil {
		return p.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *Poller) log() io.Writer {
	if p.Log == nil {
		return io.Discard
	}
	return p.Log
}

func (p *Poller) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// Poll starts a job for each new assignment, then tells the tracker what
// happened to each job. The cursor moves past an assignment only once it is
// handled, so a restart neither misses nor repeats one.
func (p *Poller) Poll(ctx context.Context) error {
	name := p.Tracker.Name()
	since, found, err := p.Store.Cursor(ctx, name, "")
	if err != nil {
		return err
	}
	if !found {
		// The first poll starts from now, so turning the tracker on does not
		// start a job for every issue it was ever given.
		since = p.clock()
		if err := p.Store.SetCursor(ctx, name, "", since); err != nil {
			return err
		}
	}
	assignments, err := p.Tracker.Assignments(ctx, since)
	if err != nil {
		return err
	}
	started := 0
	for _, assignment := range assignments {
		outcome, err := p.handle(ctx, assignment)
		if err != nil {
			return err
		}
		if outcome == handledLater {
			break
		}
		if outcome == handledStarted {
			started++
		}
		if assignment.UpdatedAt.After(since) {
			since = assignment.UpdatedAt
			if err := p.Store.SetCursor(ctx, name, "", since); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(p.log(), "machinist: %s: %d assignments, %d jobs started\n", name, len(assignments), started)
	// A tracker that will not answer about replies must not stop the news
	// about every job.
	return errors.Join(p.replies(ctx), p.sync(ctx))
}

// repliesCursor keeps how far the tracker's replies have been read, beside
// the assignments cursor.
const repliesCursor = "#replies"

// replies runs a job again for each comment an allowed person left on its
// issue after the job started, with the comment added to the job's log. All
// of one job's new replies go into one run. A reply to a job that is still
// running waits, with every reply after it, until the run ends.
func (p *Poller) replies(ctx context.Context) error {
	name := p.Tracker.Name()
	since, found, err := p.Store.Cursor(ctx, name, repliesCursor)
	if err != nil {
		return err
	}
	if !found {
		return p.Store.SetCursor(ctx, name, repliesCursor, p.clock())
	}
	// Too many replies at once: ask for a shorter span, so the cursor still
	// moves forward.
	var until time.Time
	replies, err := p.Tracker.Replies(ctx, since, until)
	for span := p.clock().Sub(since); errors.Is(err, ErrTooManyReplies) && span > time.Second; {
		span /= 2
		until = since.Add(span)
		replies, err = p.Tracker.Replies(ctx, since, until)
	}
	if err != nil {
		return err
	}
	// The cursor moves to just before the first reply left waiting, so it is
	// read again. Replies read twice are skipped by their comment key.
	cursor := maxTime(since, until)
	var order []string
	batches := map[string][]tickets.Comment{}
	first := map[string]time.Time{}
	for _, reply := range replies {
		ticket, err := p.Store.TicketBySourceKey(ctx, reply.Key)
		if errors.Is(err, tickets.ErrNotFound) {
			cursor = maxTime(cursor, reply.CreatedAt)
			continue
		}
		if err != nil {
			return err
		}
		key := name + "-comment:" + reply.ID
		if _, err := p.Store.TicketBySourceKey(ctx, key); err == nil {
			cursor = maxTime(cursor, reply.CreatedAt)
			continue
		} else if !errors.Is(err, tickets.ErrNotFound) {
			return err
		}
		switch {
		case !reply.CreatedAt.After(ticket.CreatedAt):
			// Discussion from before Machinist had the issue.
		case !slices.Contains(p.Rules.AllowedUsers, reply.Author):
			fmt.Fprintf(p.log(), "machinist: %s reply on job %s ignored: user %q may not start jobs\n", name, ticket.ID, reply.Author)
		case ticket.State == tickets.StateWorking:
			cursor = minTime(cursor, reply.CreatedAt.Add(-time.Nanosecond))
			return p.runReplies(ctx, order, batches, first, cursor)
		default:
			if _, seen := batches[ticket.ID]; !seen {
				order = append(order, ticket.ID)
				first[ticket.ID] = reply.CreatedAt
			}
			batches[ticket.ID] = append(batches[ticket.ID], tickets.Comment{Body: strings.TrimSpace(reply.Body), Key: key})
		}
		cursor = maxTime(cursor, reply.CreatedAt)
	}
	return p.runReplies(ctx, order, batches, first, cursor)
}

// runReplies runs each job with its replies. A job that cannot run now,
// because the hourly cap is reached or it was started another way meanwhile,
// keeps its replies for a later poll: the cursor moves back before them.
func (p *Poller) runReplies(ctx context.Context, order []string, batches map[string][]tickets.Comment, first map[string]time.Time, cursor time.Time) error {
	name := p.Tracker.Name()
	for _, ticketID := range order {
		wait := func(reason string) {
			cursor = minTime(cursor, first[ticketID].Add(-time.Nanosecond))
			fmt.Fprintf(p.log(), "machinist: %s replies on job %s wait: %s\n", name, ticketID, reason)
		}
		if !p.allow(p.clock()) {
			wait(fmt.Sprintf("%d jobs started in the last hour", p.MaxJobsPerHour))
			continue
		}
		_, err := p.Store.Reply(ctx, ticketID, name, batches[ticketID])
		switch {
		case errors.Is(err, tickets.ErrStillWorking):
			p.unallow()
			wait("the job is running")
		case err != nil:
			return err
		default:
			fmt.Fprintf(p.log(), "machinist: %s replies ran job %s again\n", name, ticketID)
		}
	}
	return p.Store.SetCursor(ctx, name, repliesCursor, cursor)
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

type handled int

const (
	handledLater handled = iota
	handledStarted
	handledSkipped
)

// handle starts an assignment's job. handledLater means the hourly cap is
// reached and the assignment waits for a later poll.
func (p *Poller) handle(ctx context.Context, assignment Assignment) (handled, error) {
	name := p.Tracker.Name()
	// Only an allowed person hears that an issue maps to no repository, so
	// anyone else who can hand Machinist an issue learns nothing of its config.
	if _, reason := p.Rules.Route(assignment); reason != "" && slices.Contains(p.Rules.AllowedUsers, assignment.Actor) {
		fmt.Fprintf(p.log(), "machinist: %s issue %s ignored: %s\n", name, assignment.Identifier, reason)
		return handledSkipped, p.unrouted(ctx, assignment, reason)
	}
	job, reason := p.Rules.Decide(assignment)
	if reason != "" {
		fmt.Fprintf(p.log(), "machinist: %s issue %s ignored: %s\n", name, assignment.Identifier, reason)
		return handledSkipped, nil
	}
	if _, err := p.Store.TicketBySourceKey(ctx, job.key); err == nil {
		return handledSkipped, nil
	} else if !errors.Is(err, tickets.ErrNotFound) {
		return handledLater, err
	}
	if !p.allow(p.clock()) {
		fmt.Fprintf(p.log(), "machinist: %s issue %s waits: %d jobs started in the last hour\n", name, assignment.Identifier, p.MaxJobsPerHour)
		return handledLater, nil
	}
	job.source, job.by = name, name
	ticket, _, err := p.Intake.start(ctx, job)
	if errors.Is(err, ErrInvalidRequest) {
		fmt.Fprintf(p.log(), "machinist: %s issue %s ignored: %v\n", name, assignment.Identifier, err)
		return handledSkipped, nil
	}
	if err != nil {
		return handledLater, err
	}
	fmt.Fprintf(p.log(), "machinist: %s issue %s started job %s\n", name, assignment.Identifier, ticket.ID)
	return handledStarted, nil
}

// unroutedCursor prefixes the cursor that records when Machinist last told an
// issue it maps to no repository.
const unroutedCursor = "#unrouted:"

// unrouted comments on an issue that maps to no repository, once each time it
// is handed to Machinist, so later edits to the issue do not repeat it. The
// comment is only an explanation, so a tracker that refuses it is logged and
// does not stop the poll; the comment is tried again the next time the issue
// is handed over.
func (p *Poller) unrouted(ctx context.Context, assignment Assignment, reason string) error {
	name := p.Tracker.Name()
	told, found, err := p.Store.Cursor(ctx, name, unroutedCursor+assignment.Issue)
	if err != nil {
		return err
	}
	if found && !assignment.GivenAt.After(told) {
		return nil
	}
	comment := "Machinist cannot start a job for this issue: its " + reason + " to a repository.\n\n" +
		"Map the project in `linear.projects` or the team in `linear.teams` in Machinist's config, then hand the issue to Machinist again."
	if err := p.Tracker.Comment(ctx, assignment.Issue, comment); err != nil {
		fmt.Fprintf(p.log(), "machinist: %s issue %s: comment failed: %v\n", name, assignment.Identifier, err)
		return nil
	}
	return p.Store.SetCursor(ctx, name, unroutedCursor+assignment.Issue, assignment.GivenAt)
}

// allow counts a job against the hourly cap, if there is room.
func (p *Poller) allow(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.started[:0]
	for _, at := range p.started {
		if now.Sub(at) < time.Hour {
			kept = append(kept, at)
		}
	}
	p.started = kept
	if p.MaxJobsPerHour > 0 && len(p.started) >= p.MaxJobsPerHour {
		return false
	}
	p.started = append(p.started, now)
	return true
}

// unallow takes back the newest job counted against the hourly cap.
func (p *Poller) unallow() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.started) > 0 {
		p.started = p.started[:len(p.started)-1]
	}
}

// maxReason bounds the failure reason Machinist writes on an issue.
const maxReason = 500

// sync tells the tracker what happened to each job, once: In Progress when it
// starts, the pull request when there is one, Done when it merges, a comment
// when it closes, and the reason when the job fails.
// A job whose issue has left In Progress or Machinist is cancelled.
func (p *Poller) sync(ctx context.Context) error {
	name := p.Tracker.Name()
	jobs, err := p.Store.TrackerJobs(ctx, name)
	if err != nil {
		return err
	}
	var failures []error
	watched := map[string]tickets.TrackerJob{}
	for _, job := range jobs {
		issue, ok := strings.CutPrefix(job.SourceKey, name+":")
		if !ok {
			continue
		}
		if err := p.syncJob(ctx, issue, job); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", job.SourceKey, err))
			continue
		}
		// Only a job whose issue Machinist moved to In Progress on an
		// earlier poll is watched, so a slow write is never read as a move.
		if job.State == tickets.StateWorking && job.Reported[tickets.ReportStarted] {
			watched[issue] = job
		}
	}
	if err := p.cancelMoved(ctx, watched); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (p *Poller) syncJob(ctx context.Context, issue string, job tickets.TrackerJob) error {
	report := func(kind string, write func() error) error {
		if job.Reported[kind] {
			return nil
		}
		if err := write(); err != nil {
			return err
		}
		return p.Store.MarkReported(ctx, job.TicketID, kind)
	}
	if job.State == tickets.StateWorking {
		if err := report(tickets.ReportStarted, func() error { return p.Tracker.Start(ctx, issue) }); err != nil {
			return fmt.Errorf("move to In Progress: %w", err)
		}
	}
	if job.PRNumber > 0 {
		link := fmt.Sprintf("https://github.com/%s/pull/%d", job.Repo, job.PRNumber)
		if err := report(tickets.ReportPR, func() error { return p.Tracker.Comment(ctx, issue, "Machinist opened a pull request: "+link) }); err != nil {
			return fmt.Errorf("comment the pull request: %w", err)
		}
	}
	switch job.PRState {
	case tickets.PRMerged:
		if err := report(tickets.ReportMerged, func() error { return p.Tracker.Finish(ctx, issue) }); err != nil {
			return fmt.Errorf("move to Done: %w", err)
		}
	case tickets.PRClosed:
		link := fmt.Sprintf("https://github.com/%s/pull/%d", job.Repo, job.PRNumber)
		if err := report(tickets.ReportClosed, func() error {
			return p.Tracker.Comment(ctx, issue, "Machinist's pull request was closed without merging: "+link)
		}); err != nil {
			return fmt.Errorf("comment the closed pull request: %w", err)
		}
	}
	if job.State == tickets.StateDone && job.PRNumber == 0 {
		result, err := p.Store.LatestMessage(ctx, job.TicketID)
		if err != nil {
			return err
		}
		comment := "Machinist finished without a pull request: " + clip(result, "Every step passed.") + "\n\nReply here to give it more to go on, and it will run again."
		if err := report(tickets.ReportFinished, func() error { return p.Tracker.Return(ctx, issue, comment) }); err != nil {
			return fmt.Errorf("report the result: %w", err)
		}
	}
	if job.State == tickets.StateFailed {
		reason, err := p.Store.LatestMessage(ctx, job.TicketID)
		if err != nil {
			return err
		}
		reason = clip(reason, "The job failed.")
		if err := report(tickets.ReportFailed, func() error { return p.Tracker.Fail(ctx, issue, reason) }); err != nil {
			return fmt.Errorf("report the failure: %w", err)
		}
	}
	return nil
}

// clip bounds text Machinist writes on an issue, or gives fallback if empty.
func clip(text, fallback string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	if runes := []rune(text); len(runes) > maxReason {
		return string(runes[:maxReason-1]) + "…"
	}
	return text
}

// cancelMoved cancels each job whose issue was unassigned from Machinist or
// moved out of In Progress.
func (p *Poller) cancelMoved(ctx context.Context, watched map[string]tickets.TrackerJob) error {
	if len(watched) == 0 {
		return nil
	}
	issues := make([]string, 0, len(watched))
	for issue := range watched {
		issues = append(issues, issue)
	}
	slices.Sort(issues)
	states, err := p.Tracker.States(ctx, issues)
	if err != nil {
		return err
	}
	name := p.Tracker.Name()
	for _, issue := range issues {
		state, found := states[issue]
		if found && state.Assigned && state.Active {
			continue
		}
		job := watched[issue]
		if _, err := p.Store.Cancel(ctx, job.TicketID, name); err != nil && !errors.Is(err, tickets.ErrTicketEnded) {
			return err
		}
		fmt.Fprintf(p.log(), "machinist: %s issue %s left In Progress or Machinist; cancelled job %s\n", name, issue, job.TicketID)
	}
	return nil
}
