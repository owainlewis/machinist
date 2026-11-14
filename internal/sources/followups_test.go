package sources

import (
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/engine"
	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/tickets"
)

const prNumber = 42

func (f *fixture) ticketWithPR(t *testing.T, state string) tickets.Ticket {
	t.Helper()
	return f.setState(t, f.mention(t), state, prNumber)
}

func (f *fixture) state(t *testing.T, id string) string {
	t.Helper()
	ticket, err := f.store.Ticket(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ticket.State
}

func failedCheck(name string) []fakegh.Check {
	return []fakegh.Check{{Name: name, Status: "COMPLETED", Conclusion: "FAILURE"}}
}

func TestMergedPRCountsHumanCommitsAndEndsTheTicket(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticketWithPR(t, tickets.StatePRReady)
	f.github.SetPR(prNumber, fakegh.PR{State: "MERGED", MergedAt: "2026-07-01T10:00:00Z", HeadSHA: "abc"})
	f.github.SetCommits(prNumber, engine.BotEmail(bot), strings.ToUpper(engine.BotEmail(bot)), "alice@example.test")
	f.poll(t)
	got, err := f.store.Ticket(t.Context(), ticket.ID)
	if err != nil || got.State != tickets.StateDone || *got.HumanCommits != 1 || !got.MergedAt.Equal(time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("ticket = %+v, err = %v", got, err)
	}
	calls := len(f.github.Calls())
	f.poll(t)
	for _, call := range f.github.Calls()[calls:] {
		if strings.Contains(call, "graphql") {
			t.Fatalf("an ended ticket was polled: %s", call)
		}
	}
}

func TestClosedPRStopsOnceAndAReplyContinues(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticketWithPR(t, tickets.StatePRReady)
	f.github.SetPR(prNumber, fakegh.PR{State: "CLOSED", ClosedAt: "2026-07-01T10:00:00Z", HeadSHA: "abc"})
	f.poll(t)
	if got := f.state(t, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
	f.github.AddComment(fakegh.Comment{ID: 200, Author: "alice", Body: "reopen it", Number: 5})
	f.poll(t)
	f.poll(t)
	if got := f.state(t, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("the same close stopped the reply: state = %s", got)
	}
}

func TestChangesRequestedByAPermittedPersonFeedsTheTicket(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticketWithPR(t, tickets.StatePRReady)
	f.github.SetPR(prNumber, fakegh.PR{State: "OPEN", HeadSHA: "abc", Reviews: []fakegh.Review{{ID: "r1", Author: "alice", State: "CHANGES_REQUESTED"}}})
	f.poll(t)
	f.poll(t)
	if got := f.state(t, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
	count := 0
	for _, entry := range f.entries(t, ticket.ID) {
		if entry.Kind == tickets.EntryReview {
			count++
			if entry.Author != "alice" || !strings.Contains(entry.Body, "requested changes") {
				t.Fatalf("entry = %+v", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("review entries = %d", count)
	}
}

func TestReviewsThatDoNotCount(t *testing.T) {
	for name, reviews := range map[string][]fakegh.Review{
		"read access":     {{ID: "r1", Author: "reader", State: "CHANGES_REQUESTED"}},
		"the bot":         {{ID: "r1", Author: bot, State: "CHANGES_REQUESTED"}},
		"approved":        {{ID: "r1", Author: "alice", State: "APPROVED"}},
		"plain comment":   {{ID: "r1", Author: "alice", State: "COMMENTED"}},
		"later approved":  {{ID: "r1", Author: "alice", State: "CHANGES_REQUESTED"}, {ID: "r2", Author: "alice", State: "APPROVED"}},
		"later dismissed": {{ID: "r1", Author: "alice", State: "CHANGES_REQUESTED"}, {ID: "r2", Author: "alice", State: "DISMISSED"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ticket := f.ticketWithPR(t, tickets.StatePRReady)
			f.github.SetPR(prNumber, fakegh.PR{State: "OPEN", HeadSHA: "abc", Reviews: reviews})
			f.poll(t)
			if got := f.state(t, ticket.ID); got != tickets.StatePRReady {
				t.Fatalf("state = %s", got)
			}
		})
	}
}

func TestLaterCommentDoesNotCancelARequestForChanges(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticketWithPR(t, tickets.StatePRReady)
	f.github.SetPR(prNumber, fakegh.PR{State: "OPEN", HeadSHA: "abc", Reviews: []fakegh.Review{{ID: "r1", Author: "alice", State: "CHANGES_REQUESTED"}, {ID: "r2", Author: "alice", State: "COMMENTED"}}})
	f.poll(t)
	if got := f.state(t, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
}

func TestReviewAlreadySeenCostsNoPermissionCheck(t *testing.T) {
	f := newFixture(t)
	f.ticketWithPR(t, tickets.StatePRReady)
	f.github.SetPR(prNumber, fakegh.PR{State: "OPEN", HeadSHA: "abc", Reviews: []fakegh.Review{{ID: "r1", Author: "alice", State: "CHANGES_REQUESTED"}}})
	f.poll(t)
	calls := len(f.github.Calls())
	f.poll(t)
	for _, call := range f.github.Calls()[calls:] {
		if strings.Contains(call, "/permission") {
			t.Fatalf("permission checked again: %s", call)
		}
	}
}

func TestOneCIFailurePerCommit(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticketWithPR(t, tickets.StatePRReady)
	f.github.SetPR(prNumber, fakegh.PR{State: "OPEN", HeadSHA: "abcdef1234567890", Checks: failedCheck("test")})
	f.poll(t)
	if got := f.state(t, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
	if !hasEntry(f.entries(t, ticket.ID), tickets.EntryCI, "CI failed on abcdef1: test") {
		t.Fatalf("entries = %+v", f.entries(t, ticket.ID))
	}
	f.setState(t, ticket, tickets.StatePRReady, prNumber)
	f.poll(t)
	if got := f.state(t, ticket.ID); got != tickets.StatePRReady {
		t.Fatalf("the same commit counted twice: state = %s", got)
	}
	f.github.SetPR(prNumber, fakegh.PR{State: "OPEN", HeadSHA: "fedcba", Checks: failedCheck("test")})
	f.poll(t)
	if got := f.state(t, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("a new commit was ignored: state = %s", got)
	}
}

func TestCIWaitsForEveryCheckToFinish(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticketWithPR(t, tickets.StatePRReady)
	f.github.SetPR(prNumber, fakegh.PR{State: "OPEN", HeadSHA: "abc", Checks: []fakegh.Check{{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE"}, {Name: "lint", Status: "IN_PROGRESS"}}})
	f.poll(t)
	if got := f.state(t, ticket.ID); got != tickets.StatePRReady {
		t.Fatalf("state = %s", got)
	}
}

func TestCIFailureIsSkippedWhileWorkingAndRecordedAfter(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticketWithPR(t, tickets.StateWorking)
	f.github.SetPR(prNumber, fakegh.PR{State: "OPEN", HeadSHA: "abc", Checks: failedCheck("test")})
	f.poll(t)
	if hasEntry(f.entries(t, ticket.ID), tickets.EntryCI, "CI failed") {
		t.Fatal("recorded while working")
	}
	f.setState(t, ticket, tickets.StatePRReady, prNumber)
	f.poll(t)
	if !hasEntry(f.entries(t, ticket.ID), tickets.EntryCI, "CI failed") {
		t.Fatal("not recorded after the run")
	}
}

func TestCIFailureWhileNeedsYouStaysNeedsYou(t *testing.T) {
	f := newFixture(t)
	ticket := f.ticketWithPR(t, tickets.StateNeedsYou)
	f.github.SetPR(prNumber, fakegh.PR{State: "OPEN", HeadSHA: "abc", Checks: failedCheck("test")})
	f.poll(t)
	if got := f.state(t, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
	if !hasEntry(f.entries(t, ticket.ID), tickets.EntryCI, "CI failed") {
		t.Fatal("signal lost")
	}
}

func TestOneFailingPRDoesNotHoldUpOthers(t *testing.T) {
	f := newFixture(t)
	f.ticketWithPR(t, tickets.StatePRReady) // PR 42 has no data, so reading it fails.
	f.github.AddComment(fakegh.Comment{ID: 300, Author: "alice", Body: "@machinist-bot other", Number: 6})
	_ = f.source.Poll(t.Context())
	second := f.mustActive(t, 6)
	second = f.setState(t, second, tickets.StatePRReady, 43)
	f.github.SetPR(43, fakegh.PR{State: "OPEN", HeadSHA: "abc", Checks: failedCheck("test")})
	if err := f.source.Poll(t.Context()); err == nil || !strings.Contains(err.Error(), "read PR") {
		t.Fatalf("error = %v", err)
	}
	if got := f.state(t, second.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
}

func TestUnknownPRStateIsAnError(t *testing.T) {
	f := newFixture(t)
	f.ticketWithPR(t, tickets.StatePRReady)
	f.github.SetPR(prNumber, fakegh.PR{State: "ARCHIVED"})
	if err := f.source.Poll(t.Context()); err == nil || !strings.Contains(err.Error(), "ARCHIVED") {
		t.Fatalf("error = %v", err)
	}
}
