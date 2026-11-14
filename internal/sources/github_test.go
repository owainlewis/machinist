package sources

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/tickets"
)

const bot = "machinist-bot"

type fixture struct {
	github *fakegh.GitHub
	store  *tickets.Store
	source *GitHub
	log    *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	github := fakegh.Install(t, bot)
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := &bytes.Buffer{}
	source := NewGitHub(Options{
		Client: ghclient.New(),
		Store:  store,
		Engine: engine.New(engine.Options{Store: store, Factory: &factory.Factory{Agents: map[string]factory.Agent{"default": {Name: "default", Harness: "claude"}}}, MaxTurns: 3, Handle: bot}),
		Handle: bot, Repositories: []string{"owner/repo"}, DefaultAgent: "default", Log: log,
	})
	github.SetPermission("alice", "write")
	github.SetPermission("reader", "read")
	f := &fixture{github: github, store: store, source: source, log: log}
	f.poll(t) // The first poll only sets the cursor.
	return f
}

func (f *fixture) poll(t *testing.T) {
	t.Helper()
	if err := f.source.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) active(t *testing.T, number int) (tickets.Ticket, bool) {
	t.Helper()
	ticket, err := f.store.ActiveTicketFor(t.Context(), "owner/repo", number)
	if errors.Is(err, tickets.ErrNotFound) {
		return tickets.Ticket{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return ticket, true
}

func (f *fixture) mustActive(t *testing.T, number int) tickets.Ticket {
	t.Helper()
	ticket, found := f.active(t, number)
	if !found {
		t.Fatalf("no active ticket on #%d", number)
	}
	return ticket
}

func (f *fixture) entries(t *testing.T, id string) []tickets.Entry {
	t.Helper()
	entries, err := f.store.Entries(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func hasEntry(entries []tickets.Entry, kind, text string) bool {
	for _, entry := range entries {
		if entry.Kind == kind && strings.Contains(entry.Body, text) {
			return true
		}
	}
	return false
}

// setState puts a ticket in a state, as earlier runs would have.
func (f *fixture) setState(t *testing.T, ticket tickets.Ticket, state string, pr int) tickets.Ticket {
	t.Helper()
	current, err := f.store.Ticket(t.Context(), ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Apply(t.Context(), ticket.ID, current.State, tickets.Transition{State: state, PRNumber: pr}); err != nil {
		t.Fatal(err)
	}
	updated, err := f.store.Ticket(t.Context(), ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func (f *fixture) mention(t *testing.T) tickets.Ticket {
	t.Helper()
	f.github.AddComment(fakegh.Comment{ID: 100, Author: "alice", Body: "@machinist-bot please fix this", Number: 5})
	f.poll(t)
	return f.mustActive(t, 5)
}

func TestFirstPollSetsTheCursorAndIgnoresOldMentions(t *testing.T) {
	github := fakegh.Install(t, bot)
	github.SetPermission("alice", "write")
	github.AddComment(fakegh.Comment{ID: 1, Author: "alice", Body: "@machinist-bot old", Number: 5})
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := NewGitHub(Options{Client: ghclient.New(), Store: store, Engine: engine.New(engine.Options{Store: store}), Handle: bot, Repositories: []string{"owner/repo"}, DefaultAgent: "default"})
	if err := source.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActiveTicketFor(t.Context(), "owner/repo", 5); !errors.Is(err, tickets.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
	if _, found, _ := store.Cursor(t.Context(), commentsCursor, "owner/repo"); !found {
		t.Fatal("cursor not set")
	}
	for _, call := range github.Calls() {
		if strings.Contains(call, "issues/comments") {
			t.Fatalf("first poll read comments: %s", call)
		}
	}
}

func TestLaterPollReadsBackFiveMinutes(t *testing.T) {
	f := newFixture(t)
	cursor := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := f.store.SetCursor(t.Context(), commentsCursor, "owner/repo", cursor); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	calls := f.github.Calls()
	if last := calls[len(calls)-1]; !strings.Contains(last, "since=2026-06-01T11%3A55%3A00Z") {
		t.Fatalf("call = %s", last)
	}
	moved, _, _ := f.store.Cursor(t.Context(), commentsCursor, "owner/repo")
	if !moved.After(cursor) {
		t.Fatalf("cursor = %v", moved)
	}
}

func TestMentionCreatesOneTicket(t *testing.T) {
	f := newFixture(t)
	f.github.SetIssue(5, "Broken login", "Login fails on Safari")
	ticket := f.mention(t)
	if ticket.Source != "github" || ticket.IssueNumber != 5 || ticket.Title != "Broken login" || ticket.Agent != "default" || ticket.CreatedBy != "alice" || ticket.State != tickets.StateWorking {
		t.Fatalf("ticket = %+v", ticket)
	}
	entries := f.entries(t, ticket.ID)
	for _, want := range []string{"Broken login", "Login fails on Safari", "@machinist-bot please fix this", "alice"} {
		if !strings.Contains(entries[0].Body, want) {
			t.Fatalf("missing %q in %q", want, entries[0].Body)
		}
	}
	f.poll(t)
	f.poll(t)
	if got := f.entries(t, ticket.ID); len(got) != len(entries) {
		t.Fatalf("entries grew: %+v", got)
	}
}

func TestCommentAfterTheTicketEndedDoesNotReopenIt(t *testing.T) {
	f := newFixture(t)
	ticket := f.mention(t)
	if _, err := f.store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	f.poll(t)
	if _, found := f.active(t, 5); found {
		t.Fatal("a re-read comment reopened the ticket")
	}
}

func TestNewMentionAfterTheTicketEndedStartsANewOne(t *testing.T) {
	f := newFixture(t)
	first := f.mention(t)
	if _, err := f.store.Cancel(t.Context(), first.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	f.github.AddComment(fakegh.Comment{ID: 101, Author: "alice", Body: "@machinist-bot try again", Number: 5})
	f.poll(t)
	if second := f.mustActive(t, 5); second.ID == first.ID {
		t.Fatal("same ticket")
	}
}

func TestMentionWithoutWriteAccessIsLoggedOnly(t *testing.T) {
	f := newFixture(t)
	for id, author := range map[int64]string{1: "reader", 2: "stranger"} {
		f.github.AddComment(fakegh.Comment{ID: id, Author: author, Body: "@machinist-bot do it", Number: 5})
	}
	f.poll(t)
	if _, found := f.active(t, 5); found {
		t.Fatal("ticket created")
	}
	if len(f.github.Posted()) != 0 {
		t.Fatalf("posted = %+v", f.github.Posted())
	}
	if !strings.Contains(f.log.String(), "no write access") {
		t.Fatalf("log = %q", f.log.String())
	}
}

func TestBotCommentsNeverCount(t *testing.T) {
	f := newFixture(t)
	f.github.SetPermission(bot, "write")
	f.github.AddComment(fakegh.Comment{ID: 1, Author: bot, Body: "@machinist-bot do it", Number: 5})
	f.github.AddComment(fakegh.Comment{ID: 2, Author: "Machinist-Bot", Body: "@machinist-bot case differs", Number: 6})
	f.poll(t)
	for _, number := range []int{5, 6} {
		if _, found := f.active(t, number); found {
			t.Fatalf("ticket created on #%d", number)
		}
	}
}

func TestMentionMatching(t *testing.T) {
	f := newFixture(t)
	for text, want := range map[string]bool{
		"@machinist-bot go":     true,
		"please @Machinist-Bot": true,
		"(@machinist-bot)":      true,
		"@machinist-bot, go":    true,
		"@machinist-bot-2 go":   false,
		"x@machinist-bot.com":   false,
		"machinist-bot go":      false,
		"foo/@machinist-bot":    false,
	} {
		if got := f.source.mention.MatchString(text); got != want {
			t.Errorf("%q: got %v, want %v", text, got, want)
		}
	}
}

func TestMentionOnAPRWithNoTicketIsLoggedAndIgnored(t *testing.T) {
	f := newFixture(t)
	f.github.AddComment(fakegh.Comment{ID: 1, Author: "alice", Body: "@machinist-bot review", Number: 9, OnPR: true})
	f.poll(t)
	if _, found := f.active(t, 9); found {
		t.Fatal("ticket created")
	}
	if !strings.Contains(f.log.String(), "no ticket") {
		t.Fatalf("log = %q", f.log.String())
	}
}

func TestPlainCommentWithNoTicketCostsNoGitHubCalls(t *testing.T) {
	f := newFixture(t)
	f.github.AddComment(fakegh.Comment{ID: 1, Author: "alice", Body: "just chatting", Number: 5})
	f.poll(t)
	for _, call := range f.github.Calls() {
		if strings.Contains(call, "/permission") {
			t.Fatalf("permission checked: %s", call)
		}
	}
}

func TestCommentRoutingOnActiveTicket(t *testing.T) {
	for name, test := range map[string]struct {
		state  string
		body   string
		author string
		added  bool
		next   string
	}{
		"mention while pr ready":      {tickets.StatePRReady, "@machinist-bot change it", "alice", true, tickets.StateWorking},
		"mention while working":       {tickets.StateWorking, "@machinist-bot also this", "alice", true, tickets.StateWorking},
		"plain comment on needs you":  {tickets.StateNeedsYou, "use postgres", "alice", true, tickets.StateWorking},
		"plain comment while ready":   {tickets.StatePRReady, "nice work", "alice", false, tickets.StatePRReady},
		"plain comment while working": {tickets.StateWorking, "nice work", "alice", false, tickets.StateWorking},
		"reader on needs you":         {tickets.StateNeedsYou, "use postgres", "reader", false, tickets.StateNeedsYou},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ticket := f.mention(t)
			ticket = f.setState(t, ticket, test.state, 0)
			f.github.AddComment(fakegh.Comment{ID: 200, Author: test.author, Body: test.body, Number: 5})
			f.poll(t)
			got := f.mustActive(t, 5)
			if got.ID != ticket.ID || got.State != test.next {
				t.Fatalf("ticket = %+v", got)
			}
			if added := hasEntry(f.entries(t, ticket.ID), tickets.EntryComment, test.body); added != test.added {
				t.Fatalf("added = %v", added)
			}
		})
	}
}

func TestPlainCommentMadeBeforeAQuestionIsNotItsAnswer(t *testing.T) {
	f := newFixture(t)
	ticket := f.mention(t)
	f.github.AddComment(fakegh.Comment{ID: 200, Author: "alice", Body: "nice work", Number: 5})
	f.poll(t)
	ticket = f.setState(t, ticket, tickets.StateNeedsYou, 0)
	f.poll(t) // The read-back shows comment 200 again.
	if got := f.mustActive(t, 5); got.State != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got.State)
	}
	if hasEntry(f.entries(t, ticket.ID), tickets.EntryComment, "nice work") {
		t.Fatal("an earlier comment was taken as the answer")
	}
	f.github.AddComment(fakegh.Comment{ID: 201, Author: "alice", Body: "use postgres", Number: 5})
	f.poll(t)
	if got := f.mustActive(t, 5); got.State != tickets.StateWorking {
		t.Fatalf("state = %s", got.State)
	}
}

func TestCommentOnTheTicketsPRRoutes(t *testing.T) {
	f := newFixture(t)
	ticket := f.setState(t, f.mention(t), tickets.StatePRReady, 42)
	f.github.SetPR(42, fakegh.PR{State: "OPEN", HeadSHA: "abc"})
	f.github.AddComment(fakegh.Comment{ID: 200, Author: "alice", Body: "@machinist-bot rename it", Number: 42, OnPR: true})
	f.poll(t)
	if got := f.mustActive(t, 5); got.ID != ticket.ID || got.State != tickets.StateWorking {
		t.Fatalf("ticket = %+v", got)
	}
}

func TestFailedPollDoesNotMoveTheCursor(t *testing.T) {
	f := newFixture(t)
	before, _, _ := f.store.Cursor(t.Context(), commentsCursor, "owner/repo")
	f.github.Fail("gh: Bad credentials (HTTP 401)")
	err := f.source.Poll(t.Context())
	if err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("error = %v", err)
	}
	after, _, _ := f.store.Cursor(t.Context(), commentsCursor, "owner/repo")
	if !after.Equal(before) {
		t.Fatalf("cursor moved from %v to %v", before, after)
	}
}

func TestPermissionFailureRetriesTheComment(t *testing.T) {
	f := newFixture(t)
	f.github.AddComment(fakegh.Comment{ID: 1, Author: "alice", Body: "@machinist-bot do it", Number: 5})
	before, _, _ := f.store.Cursor(t.Context(), commentsCursor, "owner/repo")
	f.github.FailOn("/permission", "gh: boom (HTTP 500)")
	if err := f.source.Poll(t.Context()); err == nil {
		t.Fatal("failure hidden")
	}
	f.github.Recover()
	after, _, _ := f.store.Cursor(t.Context(), commentsCursor, "owner/repo")
	if !after.Equal(before) {
		t.Fatal("cursor moved past a comment that was not handled")
	}
	f.poll(t)
	f.mustActive(t, 5)
}
