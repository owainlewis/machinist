package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

const closedKey = "github:closed:42:2026-01-01T00:00:00Z"

func newIssueTicket(t *testing.T, e *Engine) tickets.Ticket {
	t.Helper()
	ticket, _, err := e.Store.CreateTicket(t.Context(), tickets.NewTicket{Source: "github", Repo: "owner/repo", IssueNumber: 3, Title: "Title", Body: "THE REQUEST", Agent: "default", CreatedBy: "alice", SourceKey: "github:comment:1"})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func withPR(e *Engine) {
	e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: 42}, nil }
}

// prReady runs a ticket to pr_ready.
func prReady(t *testing.T, e *Engine, ticket tickets.Ticket) {
	t.Helper()
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, succeed("pr_ready", "Opened", "")); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StatePRReady {
		t.Fatalf("state = %s", got)
	}
}

func messages(t *testing.T, e *Engine, id string) []string {
	t.Helper()
	entries, err := e.Store.Entries(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	bodies := []string{}
	for _, entry := range entries {
		if entry.Kind == tickets.EntryMessage {
			bodies = append(bodies, entry.Body)
		}
	}
	return bodies
}

func TestFirstPRPostsLinkAndQueuesEachReviewerOnce(t *testing.T) {
	e := newEngine(t)
	e.Factory.Reviewers = []factory.Reviewer{{Name: "review", Prompt: "REVIEW THIS"}, {Name: "security", Prompt: "CHECK SECURITY"}}
	withPR(e)
	ticket := newIssueTicket(t, e)
	prReady(t, e, ticket)
	all := messages(t, e, ticket.ID)
	if len(all) != 2 || all[1] != "Opened a PR: https://github.com/owner/repo/pull/42" {
		t.Fatalf("messages = %q", all)
	}
	reviewerRuns := func() []tickets.Run {
		runs, err := e.Store.Runs(t.Context(), ticket.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := []tickets.Run{}
		for _, run := range runs {
			if run.Kind == tickets.RunKindReviewer {
				found = append(found, run)
			}
		}
		return found
	}
	if runs := reviewerRuns(); len(runs) != 2 || runs[0].Reviewer != "review" || runs[0].Harness != "claude" {
		t.Fatalf("runs = %+v", runs)
	}

	request, err := e.Assign(t.Context(), "runner-1", []string{"claude"})
	if err != nil || request == nil {
		t.Fatalf("request = %v, err = %v", request, err)
	}
	for _, want := range []string{"REVIEW THIS", "owner/repo", "PR #42", `"outcome"`} {
		if !strings.Contains(request.Prompt, want) {
			t.Fatalf("missing %q in:\n%s", want, request.Prompt)
		}
	}
	if strings.Contains(request.Prompt, "THE PLAYBOOK") || strings.Contains(request.Prompt, "THE REQUEST") || len(request.AgentFiles) != 0 || request.Branch != ticket.Branch() {
		t.Fatalf("request = %+v", request)
	}
	if err := e.Complete(t.Context(), request.RunID, protocol.Completion{State: tickets.RunFailed, Error: "exit 1"}); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StatePRReady {
		t.Fatalf("a failed reviewer changed the state to %s", got)
	}

	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Change it", SourceKey: "c2"}); err != nil {
		t.Fatal(err)
	}
	second, err := e.Assign(t.Context(), "runner-1", []string{"claude"})
	if err != nil || second == nil || !strings.Contains(second.Prompt, "CHECK SECURITY") {
		t.Fatalf("request = %+v, err = %v", second, err)
	}
	if err := e.Complete(t.Context(), second.RunID, succeed("no_change", "Reviewed", "")); err != nil {
		t.Fatal(err)
	}
	third := assignRun(t, e)
	if err := e.Complete(t.Context(), third.RunID, succeed("pr_ready", "Fixed", "")); err != nil {
		t.Fatal(err)
	}
	if runs := reviewerRuns(); len(runs) != 2 {
		t.Fatalf("reviewers ran again: %+v", runs)
	}
	if all := messages(t, e, ticket.ID); len(all) != 2 {
		t.Fatalf("link posted twice: %q", all)
	}
}

func TestFirstPRWithNoIssuePostsNoLink(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	ticket := newTicket(t, e, "api")
	prReady(t, e, ticket)
	if all := messages(t, e, ticket.ID); len(all) != 0 {
		t.Fatalf("messages = %q", all)
	}
}

func TestLostReviewerIsNotRetriedAndMissingReviewerFails(t *testing.T) {
	e := newEngine(t)
	e.Factory.Reviewers = []factory.Reviewer{{Name: "review", Prompt: "REVIEW THIS"}}
	withPR(e)
	ticket := newIssueTicket(t, e)
	prReady(t, e, ticket)
	e.Factory.Reviewers = nil
	if request, err := e.Assign(t.Context(), "runner-1", []string{"claude"}); err != nil || request != nil {
		t.Fatalf("request = %v, err = %v", request, err)
	}
	runs, _ := e.Store.Runs(t.Context(), ticket.ID)
	if last := runs[len(runs)-1]; last.Kind != tickets.RunKindReviewer || last.Status != tickets.RunFailed {
		t.Fatalf("run = %+v", last)
	}
	if request, err := e.Assign(t.Context(), "runner-1", []string{"claude"}); err != nil || request != nil {
		t.Fatalf("retried: request = %v, err = %v", request, err)
	}
}

func TestReportNeedsInputWithPRStillStartsReviewers(t *testing.T) {
	e := newEngine(t)
	e.Factory.Reviewers = []factory.Reviewer{{Name: "review", Prompt: "REVIEW THIS"}}
	withPR(e)
	ticket := newIssueTicket(t, e)
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, succeed("needs_input", "Stuck", "Which?")); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Store.Ticket(t.Context(), ticket.ID)
	if got.State != tickets.StateNeedsYou || got.PRNumber != 42 {
		t.Fatalf("ticket = %+v", got)
	}
	runs, _ := e.Store.Runs(t.Context(), ticket.ID)
	if len(runs) != 2 || runs[1].Reviewer != "review" {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestPRMergedEndsAnyActiveTicket(t *testing.T) {
	mergedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for name, setup := range map[string]func(*testing.T, *Engine, tickets.Ticket) string{
		"working": func(t *testing.T, e *Engine, ticket tickets.Ticket) string {
			return assignRun(t, e).RunID
		},
		"needs you": func(t *testing.T, e *Engine, ticket tickets.Ticket) string {
			request := assignRun(t, e)
			if err := e.Complete(t.Context(), request.RunID, succeed("needs_input", "Stuck", "Which?")); err != nil {
				t.Fatal(err)
			}
			return ""
		},
		"pr ready": func(t *testing.T, e *Engine, ticket tickets.Ticket) string {
			prReady(t, e, ticket)
			return ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEngine(t)
			withPR(e)
			ticket := newTicket(t, e, "api")
			running := setup(t, e, ticket)
			before := len(messages(t, e, ticket.ID))
			if err := e.PRMerged(t.Context(), ticket, 2, mergedAt); err != nil {
				t.Fatal(err)
			}
			got, _ := e.Store.Ticket(t.Context(), ticket.ID)
			if got.State != tickets.StateDone || got.HumanCommits == nil || *got.HumanCommits != 2 || !got.MergedAt.Equal(mergedAt) {
				t.Fatalf("ticket = %+v", got)
			}
			if after := len(messages(t, e, ticket.ID)); after != before {
				t.Fatalf("merge posted a message")
			}
			if running != "" {
				if cancel, err := e.Heartbeat(t.Context(), running); err != nil || !cancel {
					t.Fatalf("cancel = %v, err = %v", cancel, err)
				}
			}
		})
	}
}

func TestPRClosedStopsWorkOnce(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	ticket := newTicket(t, e, "api")
	prReady(t, e, ticket)
	if err := e.PRClosed(t.Context(), ticket, closedKey); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
	if message := lastMessage(t, e, ticket.ID); message != "The PR was closed. Reply to continue or cancel." {
		t.Fatalf("message = %q", message)
	}
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Reopen it"}); err != nil {
		t.Fatal(err)
	}
	if err := e.PRClosed(t.Context(), ticket, closedKey); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("the same close stopped the reply: state = %s", got)
	}
	if err := e.PRClosed(t.Context(), ticket, "github:closed:42:later"); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("a new close was ignored: state = %s", got)
	}
}

func TestPRClosedDuringARunCancelsIt(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if err := e.PRClosed(t.Context(), ticket, closedKey); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
	if cancel, err := e.Heartbeat(t.Context(), request.RunID); err != nil || !cancel {
		t.Fatalf("cancel = %v, err = %v", cancel, err)
	}
	if err := e.Complete(t.Context(), request.RunID, protocol.Completion{State: tickets.RunCancelled}); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
}

func TestPRClosedOnQueuedRunCancelsIt(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.PRClosed(t.Context(), ticket, closedKey); err != nil {
		t.Fatal(err)
	}
	if request, err := e.Assign(t.Context(), "runner-1", []string{"claude"}); err != nil || request != nil {
		t.Fatalf("request = %v, err = %v", request, err)
	}
}

func TestPRClosedFailsAnAutomation(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "schedule:nightly")
	if err := e.PRClosed(t.Context(), ticket, closedKey); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateFailed {
		t.Fatalf("state = %s", got)
	}
}

func TestSignalsWhileNeedsYouWaitForTheNextRun(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, succeed("needs_input", "Stuck", "Which?")); err != nil {
		t.Fatal(err)
	}
	for key, kind := range map[string]string{"ci": tickets.EntryCI, "review": tickets.EntryReview} {
		if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: kind, Author: "bob", Body: "SIGNAL " + key, SourceKey: key}); err != nil {
			t.Fatal(err)
		}
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Use v2"}); err != nil {
		t.Fatal(err)
	}
	next := assignRun(t, e)
	for _, want := range []string{"SIGNAL ci", "SIGNAL review", "Use v2"} {
		if !strings.Contains(next.Prompt, want) {
			t.Fatalf("missing %q in:\n%s", want, next.Prompt)
		}
	}
}

func TestSignalOnPRReadyRunsUntilTheTurnLimit(t *testing.T) {
	e := newEngine(t)
	e.MaxTurns = 2
	withPR(e)
	ticket := newTicket(t, e, "api")
	prReady(t, e, ticket)

	ci := func(sha string) {
		t.Helper()
		if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryCI, Author: "github", Body: "CI failed on " + sha, SourceKey: "github:ci:" + sha}); err != nil {
			t.Fatal(err)
		}
	}
	ci("aaa")
	if got := state(t, e, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
	prReady(t, e, ticket)
	ci("bbb")
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
	message := lastMessage(t, e, ticket.ID)
	if !strings.Contains(message, "limit of 2 runs") || !strings.Contains(message, "CI failed on bbb") || !strings.Contains(message, "pr_ready: Opened") {
		t.Fatalf("message = %q", message)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if all := messages(t, e, ticket.ID); len(all) != 1 {
		t.Fatalf("messages = %q", all)
	}
	if request, err := e.Assign(t.Context(), "runner-1", []string{"claude"}); err != nil || request != nil {
		t.Fatalf("a run started past the limit: %v, %v", request, err)
	}
}

func TestInputDuringALastTurnStillEndsInOneMessage(t *testing.T) {
	e := newEngine(t)
	e.MaxTurns = 1
	withPR(e)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryCI, Author: "github", Body: "CI failed on aaa", SourceKey: "github:ci:aaa"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Complete(t.Context(), request.RunID, succeed("pr_ready", "Opened", "")); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
	if all := messages(t, e, ticket.ID); len(all) != 1 || !strings.Contains(all[0], "CI failed on aaa") {
		t.Fatalf("messages = %q", all)
	}
}

func TestInputDuringRunStartsTheNextRunAtOnce(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Also this", SourceKey: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Complete(t.Context(), request.RunID, succeed("pr_ready", "Opened", "")); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
	if next := assignRun(t, e); !strings.Contains(next.Prompt, "Also this") {
		t.Fatalf("prompt = %s", next.Prompt)
	}
}

func TestEndedTicketIgnoresEveryEvent(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	ticket := newTicket(t, e, "api")
	prReady(t, e, ticket)
	if _, err := e.Store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	before := len(messages(t, e, ticket.ID))
	if err := e.PRClosed(t.Context(), ticket, closedKey); err != nil {
		t.Fatal(err)
	}
	if err := e.PRMerged(t.Context(), ticket, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, added, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Hello"}); err != nil || added {
		t.Fatalf("added = %v, err = %v", added, err)
	}
	got, _ := e.Store.Ticket(t.Context(), ticket.ID)
	if got.State != tickets.StateCancelled || got.MergedAt != nil || len(messages(t, e, ticket.ID)) != before {
		t.Fatalf("ticket = %+v", got)
	}
}
