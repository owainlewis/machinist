package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestAMergedPRRecordsWhenItOpenedAndWhoReviewedIt(t *testing.T) {
	e, github, ticket := jobWithOpenPR(t)
	now := time.Now().UTC()
	opened, reviewed, merged := now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Hour)
	var asked []string
	e.Reviews = func(_ context.Context, repository string, number int, _ time.Time) (tickets.Reviews, error) {
		asked = append(asked, fmt.Sprintf("%s#%d", repository, number))
		return tickets.Reviews{Count: 2, First: &reviewed}, nil
	}
	github.pr = PR{Number: 42, OpenedAt: opened}
	if err := e.CheckPRs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 0 {
		t.Fatalf("an open PR's reviews were read: %v", asked)
	}
	github.pr = PR{Number: 42, Merged: true, ChangedAt: merged, OpenedAt: opened}
	if err := e.CheckPRs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "owner/repo#42" {
		t.Fatalf("reviews read for %v", asked)
	}
	stats, err := e.Store.Stats(t.Context(), 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Cycle.Merged != 1 || stats.Cycle.Reviewed != 1 || *stats.Cycle.ToReview.Median != time.Hour.Milliseconds() || *stats.Cycle.ReviewToMerge.Median != time.Hour.Milliseconds() || stats.Touches.Reviews != 2 {
		t.Fatalf("stats for %s: cycle = %+v, touches = %+v", ticket.ID, stats.Cycle, stats.Touches)
	}
}

func TestAPRMergedByTheRunRecordsItsReviews(t *testing.T) {
	e := newEngine(t)
	opened := time.Now().UTC().Add(-time.Hour)
	e.FindPR = func(context.Context, string, string) (*PR, error) {
		return &PR{Number: 42, Merged: true, ChangedAt: time.Now().UTC(), OpenedAt: opened}, nil
	}
	e.Reviews = func(context.Context, string, int, time.Time) (tickets.Reviews, error) {
		return tickets.Reviews{Count: 1, First: &opened}, nil
	}
	newTicket(t, e, "api")
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, succeed("no_change", "Merged")); err != nil {
		t.Fatal(err)
	}
	stats, err := e.Store.Stats(t.Context(), 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Cycle.Merged != 1 || stats.Cycle.Reviewed != 1 || stats.Touches.Reviews != 1 || stats.PRs.Opened != 1 {
		t.Fatalf("cycle = %+v, touches = %+v, prs = %+v", stats.Cycle, stats.Touches, stats.PRs)
	}
}

func TestAMergedPRWaitsWhenItsReviewsCannotBeRead(t *testing.T) {
	e, github, ticket := jobWithOpenPR(t)
	e.Reviews = func(context.Context, string, int, time.Time) (tickets.Reviews, error) {
		return tickets.Reviews{}, errors.New("rate limited")
	}
	github.pr = PR{Number: 42, Merged: true}
	if err := e.CheckPRs(t.Context()); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
	if got := ticketOf(t, e, ticket.ID); got.State != tickets.StatePRReady {
		t.Fatalf("the job moved on without its reviews: %+v", got)
	}
}
