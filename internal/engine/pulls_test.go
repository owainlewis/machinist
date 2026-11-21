package engine

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

// githubPRs answers FindPR from a map of PR states, and counts the calls.
type githubPRs struct {
	pr    PR
	calls int
	err   error
}

func (g *githubPRs) find(_ context.Context, _, _ string) (*PR, error) {
	g.calls++
	if g.err != nil {
		return nil, g.err
	}
	pr := g.pr
	return &pr, nil
}

func jobWithOpenPR(t *testing.T) (*Engine, *githubPRs, tickets.Ticket) {
	t.Helper()
	e := newEngine(t)
	github := &githubPRs{pr: PR{Number: 42}}
	e.FindPR = github.find
	ticket := newTicket(t, e, "api")
	prReady(t, e, ticket)
	github.calls = 0
	return e, github, ticket
}

func ticketOf(t *testing.T, e *Engine, id string) tickets.Ticket {
	t.Helper()
	ticket, err := e.Store.Ticket(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestAMergedPRIsRecordedAndThenNoLongerChecked(t *testing.T) {
	e, github, ticket := jobWithOpenPR(t)
	if got := ticketOf(t, e, ticket.ID); got.PRState != tickets.PROpen || got.PRStateAt == nil {
		t.Fatalf("a job that opened a PR: %+v", got)
	}
	if err := e.CheckPRs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := ticketOf(t, e, ticket.ID); got.State != tickets.StatePRReady || got.PRState != tickets.PROpen {
		t.Fatalf("still open: %+v", got)
	}
	merged := time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)
	github.pr = PR{Number: 42, Merged: true, ChangedAt: merged}
	if err := e.CheckPRs(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StateDone || got.PRState != tickets.PRMerged || !got.PRStateAt.Equal(merged) || got.MergedAt == nil || !got.MergedAt.Equal(merged) {
		t.Fatalf("merged: %+v", got)
	}
	calls := github.calls
	if err := e.CheckPRs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if github.calls != calls {
		t.Fatalf("a merged PR was checked again: %d calls, then %d", calls, github.calls)
	}
}

func TestAClosedPRIsRecordedWithoutAMerge(t *testing.T) {
	e, github, ticket := jobWithOpenPR(t)
	github.pr = PR{Number: 42, Closed: true}
	if err := e.CheckPRs(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StateDone || got.PRState != tickets.PRClosed || got.PRStateAt == nil || got.MergedAt != nil {
		t.Fatalf("closed: %+v", got)
	}
}

func TestPRChecksBackOffOnErrorsUpToFiveMinutes(t *testing.T) {
	e, github, _ := jobWithOpenPR(t)
	github.err = errors.New("rate limited")
	log := &bytes.Buffer{}
	e.Log = log
	var waits []time.Duration
	ctx, cancel := context.WithCancel(t.Context())
	e.sleep = func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if len(waits) == 6 {
			github.err = nil
		}
		if len(waits) == 8 {
			cancel()
			return false
		}
		return true
	}
	e.runPRChecks(ctx)
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute, 5 * time.Minute, time.Minute, time.Minute}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v", waits)
	}
	for index := range want {
		if waits[index] != want[index] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
	if !strings.Contains(log.String(), "rate limited; next check in 2m0s") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestARerunJobIsNotMovedByAnOldPRCheck(t *testing.T) {
	e, github, ticket := jobWithOpenPR(t)
	if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	github.pr = PR{Number: 42, Merged: true}
	if err := e.CheckPRs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if github.calls != 0 || state(t, e, ticket.ID) != tickets.StateWorking {
		t.Fatalf("calls = %d, state = %s", github.calls, state(t, e, ticket.ID))
	}
}
