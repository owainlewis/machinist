package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/ship"
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

func newRepoPRReady(t *testing.T, e *Engine, repo, key string, prNumber int) tickets.Ticket {
	t.Helper()
	ticket, _, err := e.Store.CreateTicket(t.Context(), tickets.NewTicket{
		Source: "api", Repo: repo, Title: "T " + repo, Body: "B", Agent: "default", CreatedBy: "alice", SourceKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	prev := e.FindPR
	e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: prNumber}, nil }
	prReady(t, e, ticket)
	e.FindPR = prev
	return ticket
}

func TestCheckPRsContinuesPastNonRateLimitFailureOnOtherRepos(t *testing.T) {
	e := newEngine(t)
	broken := newRepoPRReady(t, e, "owner/broken", "k1", 10)
	good := newRepoPRReady(t, e, "owner/good", "k2", 20)
	alsoBroken := newRepoPRReady(t, e, "owner/missing", "k3", 30)

	merged := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var checked []string
	e.FindPR = func(_ context.Context, repo, _ string) (*PR, error) {
		checked = append(checked, repo)
		switch repo {
		case "owner/broken":
			return nil, &ghclient.Error{Kind: ghclient.ErrorCommand, Operation: "find PR", Detail: "gh: Not Found (HTTP 404)"}
		case "owner/missing":
			return nil, &ghclient.Error{Kind: ghclient.ErrorAuth, Operation: "get a token for owner/missing", Detail: "the GitHub App is not installed on owner/missing"}
		default:
			return &PR{Number: 20, Merged: true, ChangedAt: merged}, nil
		}
	}

	err := e.CheckPRs(t.Context())
	if err == nil {
		t.Fatal("expected joined error from failing repositories")
	}
	if !strings.Contains(err.Error(), broken.ID) || !strings.Contains(err.Error(), alsoBroken.ID) {
		t.Fatalf("error should mention both failing jobs: %v", err)
	}
	if len(checked) != 3 {
		t.Fatalf("checked = %v, want all 3 repositories checked", checked)
	}
	if got := ticketOf(t, e, good.ID); got.State != tickets.StateDone || got.PRState != tickets.PRMerged {
		t.Fatalf("good repo PR was not updated: %+v", got)
	}
	if got := ticketOf(t, e, broken.ID); got.State != tickets.StatePRReady {
		t.Fatalf("broken repo PR unexpectedly changed: %+v", got)
	}
}

func TestCheckPRsStopsEarlyOnRateLimit(t *testing.T) {
	for name, rateErr := range map[string]error{
		"ghclient rate limit": &ghclient.Error{Kind: ghclient.ErrorRateLimit, Operation: "find PR", Detail: "API rate limit exceeded"},
		"plain rate limit":    errors.New("API rate limit exceeded (HTTP 429)"),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEngine(t)
			first := newRepoPRReady(t, e, "owner/first", "k1", 10)
			_ = newRepoPRReady(t, e, "owner/second", "k2", 20)

			var checked []string
			e.FindPR = func(_ context.Context, repo, _ string) (*PR, error) {
				checked = append(checked, repo)
				if repo == "owner/first" {
					return nil, rateErr
				}
				return &PR{Number: 20, Merged: true}, nil
			}

			err := e.CheckPRs(t.Context())
			if err == nil || !strings.Contains(err.Error(), first.ID) {
				t.Fatalf("err = %v", err)
			}
			if len(checked) != 1 || checked[0] != "owner/first" {
				t.Fatalf("checked = %v, want early stop after rate limit on owner/first", checked)
			}
		})
	}
}

func TestFailedJobWithOpenPRMergedByHumanTransitionsToDone(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
	github := &githubPRs{pr: PR{Number: 42}}
	e.FindPR = github.find

	// CI fails, so the job transitions to failed with an open PR.
	pull.checks = []ship.Check{{Name: "test", Bucket: "fail", Link: "https://ci.example.test/fail"}}
	checkPRs(t, e)
	if got := ticketOf(t, e, ticket.ID); got.State != tickets.StateFailed || got.PRState != tickets.PROpen || got.Shipping != "" {
		t.Fatalf("after check failure: %+v", got)
	}

	// Human fixes the branch and merges the PR manually on GitHub.
	now := time.Now().UTC()
	opened, reviewed, merged := now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Hour)
	var reviewCalls int
	e.Reviews = func(_ context.Context, repo string, number int, _ time.Time) (tickets.Reviews, error) {
		reviewCalls++
		if repo != "owner/repo" || number != 42 {
			t.Errorf("unexpected review lookup %s#%d", repo, number)
		}
		return tickets.Reviews{Count: 2, First: &reviewed}, nil
	}
	github.pr = PR{Number: 42, Merged: true, ChangedAt: merged, OpenedAt: opened}
	github.calls = 0

	checkPRs(t, e)
	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StateDone || got.PRState != tickets.PRMerged || got.PRStateAt == nil || !got.PRStateAt.Equal(merged) || got.MergedAt == nil || !got.MergedAt.Equal(merged) {
		t.Fatalf("after manual merge: %+v", got)
	}
	if reviewCalls != 1 {
		t.Fatalf("reviewCalls = %d, want 1", reviewCalls)
	}
	stats, err := e.Store.Stats(t.Context(), 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Cycle.Merged != 1 || stats.Cycle.Reviewed != 1 || stats.Touches.Reviews != 2 {
		t.Fatalf("stats: cycle = %+v, touches = %+v", stats.Cycle, stats.Touches)
	}

	// Polling stops once the PR is merged.
	calls := github.calls
	checkPRs(t, e)
	if github.calls != calls {
		t.Fatalf("merged PR on formerly failed job was checked again: %d -> %d", calls, github.calls)
	}
	open, err := e.Store.TicketsWithOpenPRs(t.Context())
	if err != nil || len(open) != 0 {
		t.Fatalf("TicketsWithOpenPRs() = %+v, err = %v", open, err)
	}
}

func TestFailedJobWithOpenPRClosedByHumanStaysFailed(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
	github := &githubPRs{pr: PR{Number: 42}}
	e.FindPR = github.find

	pull.checks = []ship.Check{{Name: "test", Bucket: "fail"}}
	checkPRs(t, e)
	if got := ticketOf(t, e, ticket.ID); got.State != tickets.StateFailed || got.PRState != tickets.PROpen {
		t.Fatalf("after check failure: %+v", got)
	}

	closed := time.Date(2026, 11, 26, 11, 0, 0, 0, time.UTC)
	github.pr = PR{Number: 42, Closed: true, ChangedAt: closed}
	github.calls = 0

	checkPRs(t, e)
	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StateFailed || got.PRState != tickets.PRClosed || got.PRStateAt == nil || !got.PRStateAt.Equal(closed) || got.MergedAt != nil {
		t.Fatalf("after manual close: %+v", got)
	}

	// Polling stops once the PR is closed.
	calls := github.calls
	checkPRs(t, e)
	if github.calls != calls {
		t.Fatalf("closed PR on failed job was checked again: %d -> %d", calls, github.calls)
	}
	open, err := e.Store.TicketsWithOpenPRs(t.Context())
	if err != nil || len(open) != 0 {
		t.Fatalf("TicketsWithOpenPRs() = %+v, err = %v", open, err)
	}
}

func TestCancelledJobWithOpenPRReconcilesMergedAndClosedWithoutAutoMerge(t *testing.T) {
	for name, outcome := range map[string]struct {
		pr        PR
		wantState string
		wantMerge bool
	}{
		"merged": {
			pr:        PR{Number: 42, Merged: true, ChangedAt: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)},
			wantState: tickets.PRMerged,
			wantMerge: true,
		},
		"closed": {
			pr:        PR{Number: 42, Closed: true, ChangedAt: time.Date(2026, 11, 26, 12, 5, 0, 0, time.UTC)},
			wantState: tickets.PRClosed,
			wantMerge: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
			github := &githubPRs{pr: PR{Number: 42}}
			e.FindPR = github.find
			pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}

			if _, err := e.Store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
				t.Fatal(err)
			}

			// While still open, CheckPRs does not auto-merge or call PullRequests.
			checkPRs(t, e)
			if pull.looks != 0 || len(pull.merged) != 0 || pull.updated != 0 {
				t.Fatalf("cancelled job touched ship client: looks=%d merged=%v updated=%d", pull.looks, pull.merged, pull.updated)
			}

			github.pr = outcome.pr
			github.calls = 0
			checkPRs(t, e)

			got := ticketOf(t, e, ticket.ID)
			if got.State != tickets.StateCancelled || got.PRState != outcome.wantState || got.PRStateAt == nil || !got.PRStateAt.Equal(outcome.pr.ChangedAt) || (got.MergedAt != nil) != outcome.wantMerge {
				t.Fatalf("after %s: %+v", name, got)
			}
			if outcome.wantMerge && !got.MergedAt.Equal(outcome.pr.ChangedAt) {
				t.Fatalf("merged_at = %v, want %v", got.MergedAt, outcome.pr.ChangedAt)
			}
			if pull.looks != 0 || len(pull.merged) != 0 || pull.updated != 0 {
				t.Fatalf("cancelled job touched ship client after settle: looks=%d merged=%v updated=%d", pull.looks, pull.merged, pull.updated)
			}

			// Polling stops once settled.
			calls := github.calls
			checkPRs(t, e)
			if github.calls != calls {
				t.Fatalf("settled PR on cancelled job was checked again: %d -> %d", calls, github.calls)
			}
		})
	}
}

func TestFailedAndCancelledJobsWithOpenPRNeverRunShipOrSteward(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, e *Engine, pull *fakePull, ticket tickets.Ticket)
		want  string
	}{
		{
			name: "failed",
			setup: func(t *testing.T, e *Engine, pull *fakePull, ticket tickets.Ticket) {
				pull.checks = []ship.Check{{Name: "test", Bucket: "fail"}}
				checkPRs(t, e)
				if got := ticketOf(t, e, ticket.ID); got.State != tickets.StateFailed {
					t.Fatalf("setup failed: got state %s", got.State)
				}
			},
			want: tickets.StateFailed,
		},
		{
			name: "cancelled",
			setup: func(t *testing.T, e *Engine, _ *fakePull, ticket tickets.Ticket) {
				if _, err := e.Store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
					t.Fatal(err)
				}
			},
			want: tickets.StateCancelled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
			github := &githubPRs{pr: PR{Number: 42}}
			e.FindPR = github.find

			tc.setup(t, e, pull, ticket)

			// Enable PR Steward and set up conditions that would trigger Merge,
			// UpdateBranch, and Steward on a pr_ready job.
			e.StewardRetries = 2
			github.calls = 0
			pull.looks = 0
			pull.merged = nil
			pull.updated = 0
			pull.pr.MergeStateStatus = "BEHIND"
			pull.checks = []ship.Check{{Name: "test", Bucket: "fail"}}

			checkPRs(t, e)
			if github.calls != 1 {
				t.Fatalf("FindPR calls on %s job = %d, want 1", tc.name, github.calls)
			}
			if pull.looks != 0 || len(pull.merged) != 0 || pull.updated != 0 {
				t.Fatalf("ship client called on %s job: looks=%d merged=%v updated=%d", tc.name, pull.looks, pull.merged, pull.updated)
			}
			got := ticketOf(t, e, ticket.ID)
			if got.State != tc.want || got.PRState != tickets.PROpen || got.StewardRetries != 0 {
				t.Fatalf("after CheckPRs on %s job: %+v", tc.name, got)
			}

			// Also verify with passing/clean checks (would auto-merge on pr_ready).
			pull.pr.MergeStateStatus = "CLEAN"
			pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
			checkPRs(t, e)
			if github.calls != 2 {
				t.Fatalf("FindPR calls on %s job = %d, want 2", tc.name, github.calls)
			}
			if pull.looks != 0 || len(pull.merged) != 0 || pull.updated != 0 {
				t.Fatalf("ship client called on %s job with green checks: looks=%d merged=%v updated=%d", tc.name, pull.looks, pull.merged, pull.updated)
			}
			got = ticketOf(t, e, ticket.ID)
			if got.State != tc.want || got.PRState != tickets.PROpen || got.StewardRetries != 0 {
				t.Fatalf("after second CheckPRs on %s job: %+v", tc.name, got)
			}
		})
	}
}

func TestFailedRerunAfterMergedPRDoesNotReattachHistoricalPR(t *testing.T) {
	e, github, ticket := jobWithOpenPR(t)

	// The first run's PR #42 merges.
	merged := time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)
	github.pr = PR{Number: 42, Merged: true, ChangedAt: merged, OpenedAt: merged.Add(-time.Hour)}
	checkPRs(t, e)
	if got := ticketOf(t, e, ticket.ID); got.State != tickets.StateDone || got.PRNumber != 42 || got.PRState != tickets.PRMerged {
		t.Fatalf("after merge: %+v", got)
	}

	// Rerun the job after PR #42 merged; the rerun fails without opening a new PR.
	// FindPR still falls back to historical merged PR #42 on the reused branch.
	if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	var reviewCalls int
	e.Reviews = func(context.Context, string, int, time.Time) (tickets.Reviews, error) {
		reviewCalls++
		return tickets.Reviews{Count: 1}, nil
	}
	req := assignRun(t, e)
	if err := e.Complete(t.Context(), req.RunID, protocol.Completion{State: tickets.RunFailed, Error: "tests failed"}); err != nil {
		t.Fatal(err)
	}

	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StateFailed || got.PRNumber != 0 || got.PRState != "" || got.MergedAt != nil {
		t.Fatalf("after failed rerun: %+v", got)
	}
	if reviewCalls != 0 {
		t.Fatalf("reviewCalls = %d, want 0", reviewCalls)
	}

	// Subsequent CheckPRs ticks must keep the job failed with pr_number == 0.
	github.calls = 0
	checkPRs(t, e)
	got = ticketOf(t, e, ticket.ID)
	if got.State != tickets.StateFailed || got.PRNumber != 0 || got.PRState != "" || got.MergedAt != nil {
		t.Fatalf("after CheckPRs: %+v", got)
	}
	if github.calls != 0 {
		t.Fatalf("CheckPRs queried GitHub %d times for failed rerun with no PR, want 0", github.calls)
	}
}

func TestNoChangeReplyAfterClosedOrMergedPRDoesNotReattachHistoricalPR(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   PR
	}{
		{
			name: "closed",
			pr:   PR{Number: 42, Closed: true, ChangedAt: time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)},
		},
		{
			name: "merged",
			pr:   PR{Number: 42, Merged: true, ChangedAt: time.Date(2026, 11, 20, 10, 30, 0, 0, time.UTC)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEngine(t)
			github := &githubPRs{pr: PR{Number: 42}}
			e.FindPR = github.find

			// Use an issue ticket so FirstPR would post a GitHub comment if triggered.
			ticket := newIssueTicket(t, e)
			prReady(t, e, ticket)

			// First PR closes or merges.
			github.pr = tc.pr
			checkPRs(t, e)
			beforeMsgs := messages(t, e, ticket.ID)
			if len(beforeMsgs) != 2 || beforeMsgs[1] != "Opened a PR: https://github.com/owner/repo/pull/42" {
				t.Fatalf("initial messages = %q", beforeMsgs)
			}

			// Reply to the job; the rerun finishes no_change without opening a new PR.
			if _, err := e.Store.Reply(t.Context(), ticket.ID, "alice", []tickets.Comment{{Body: "Please double-check this."}}); err != nil {
				t.Fatal(err)
			}
			var reviewCalls int
			e.Reviews = func(context.Context, string, int, time.Time) (tickets.Reviews, error) {
				reviewCalls++
				return tickets.Reviews{Count: 1}, nil
			}
			req := assignRun(t, e)
			if err := e.Complete(t.Context(), req.RunID, succeed(protocol.OutcomeNoChange, "Nothing to change")); err != nil {
				t.Fatal(err)
			}

			got := ticketOf(t, e, ticket.ID)
			if got.State != tickets.StateDone || got.PRNumber != 0 || got.PRState != "" || got.MergedAt != nil {
				t.Fatalf("after no_change reply: %+v", got)
			}
			if reviewCalls != 0 {
				t.Fatalf("reviewCalls = %d, want 0", reviewCalls)
			}

			// No duplicate FirstPR message should be emitted; only the no_change summary.
			afterMsgs := messages(t, e, ticket.ID)
			if len(afterMsgs) != len(beforeMsgs)+1 || afterMsgs[len(afterMsgs)-1] != "Nothing to change" {
				t.Fatalf("messages = %q, want one new summary message after %q", afterMsgs, beforeMsgs)
			}

			// The rerun's stored report must remain no_change (not relabelled to merged).
			runs, err := e.Store.Runs(t.Context(), ticket.ID)
			if err != nil || len(runs) != 2 || runs[1].Report == nil || runs[1].Report.Outcome != protocol.OutcomeNoChange {
				t.Fatalf("runs = %+v, err = %v", runs, err)
			}

			// CheckPRs must leave the finished job untouched.
			github.calls = 0
			checkPRs(t, e)
			got = ticketOf(t, e, ticket.ID)
			if got.State != tickets.StateDone || got.PRNumber != 0 || got.PRState != "" || got.MergedAt != nil {
				t.Fatalf("after CheckPRs: %+v", got)
			}
		})
	}
}

func TestRerunAfterEndedPRHandlesNewPRAndMultipleReruns(t *testing.T) {
	t.Run("opens new open PR", func(t *testing.T) {
		e := newEngine(t)
		github := &githubPRs{pr: PR{Number: 42}}
		e.FindPR = github.find
		ticket := newIssueTicket(t, e)
		prReady(t, e, ticket)

		github.pr = PR{Number: 42, Merged: true, ChangedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
		checkPRs(t, e)

		if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
			t.Fatal(err)
		}
		github.pr = PR{Number: 43}
		prReady(t, e, ticket)

		got := ticketOf(t, e, ticket.ID)
		if got.State != tickets.StatePRReady || got.PRNumber != 43 || got.PRState != tickets.PROpen {
			t.Fatalf("after new PR: %+v", got)
		}
		all := messages(t, e, ticket.ID)
		if len(all) != 3 || all[2] != "Opened a PR: https://github.com/owner/repo/pull/43" {
			t.Fatalf("messages = %q", all)
		}
	})

	t.Run("opens and merges new PR in same run", func(t *testing.T) {
		e, github, ticket := jobWithOpenPR(t)
		firstMerged := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
		github.pr = PR{Number: 42, Merged: true, ChangedAt: firstMerged}
		checkPRs(t, e)

		if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
			t.Fatal(err)
		}
		secondMerged := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		github.pr = PR{Number: 43, Merged: true, ChangedAt: secondMerged, OpenedAt: secondMerged.Add(-time.Hour)}
		var reviewedPR int
		e.Reviews = func(_ context.Context, _ string, number int, _ time.Time) (tickets.Reviews, error) {
			reviewedPR = number
			return tickets.Reviews{Count: 1}, nil
		}
		req := assignRun(t, e)
		if err := e.Complete(t.Context(), req.RunID, succeed(protocol.OutcomeNoChange, "Merged #43")); err != nil {
			t.Fatal(err)
		}
		got := ticketOf(t, e, ticket.ID)
		if got.State != tickets.StateDone || got.PRNumber != 43 || got.PRState != tickets.PRMerged || got.MergedAt == nil || !got.MergedAt.Equal(secondMerged) {
			t.Fatalf("after new merged PR: %+v", got)
		}
		if reviewedPR != 43 {
			t.Fatalf("reviewedPR = %d, want 43", reviewedPR)
		}
	})

	t.Run("failed rerun that opens new open PR keeps watching new PR", func(t *testing.T) {
		e, github, ticket := jobWithOpenPR(t)
		github.pr = PR{Number: 42, Merged: true, ChangedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
		checkPRs(t, e)

		if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
			t.Fatal(err)
		}
		github.pr = PR{Number: 43}
		req := assignRun(t, e)
		if err := e.Complete(t.Context(), req.RunID, protocol.Completion{State: tickets.RunFailed, Error: "ci failed"}); err != nil {
			t.Fatal(err)
		}
		got := ticketOf(t, e, ticket.ID)
		if got.State != tickets.StateFailed || got.PRNumber != 43 {
			t.Fatalf("after failed rerun with new open PR: %+v", got)
		}
		merged := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
		github.pr = PR{Number: 43, Merged: true, ChangedAt: merged}
		checkPRs(t, e)
		got = ticketOf(t, e, ticket.ID)
		if got.State != tickets.StateDone || got.PRNumber != 43 || got.PRState != tickets.PRMerged {
			t.Fatalf("after human merged #43: %+v", got)
		}
	})

	t.Run("multiple consecutive reruns without new PR never reattach historical PR", func(t *testing.T) {
		e, github, ticket := jobWithOpenPR(t)
		github.pr = PR{Number: 42, Merged: true, ChangedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
		checkPRs(t, e)

		// First rerun fails without opening a PR.
		if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
			t.Fatal(err)
		}
		req := assignRun(t, e)
		if err := e.Complete(t.Context(), req.RunID, protocol.Completion{State: tickets.RunFailed, Error: "boom"}); err != nil {
			t.Fatal(err)
		}
		if got := ticketOf(t, e, ticket.ID); got.State != tickets.StateFailed || got.PRNumber != 0 {
			t.Fatalf("after first rerun: %+v", got)
		}

		// Second rerun finishes no_change without opening a PR.
		if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
			t.Fatal(err)
		}
		req = assignRun(t, e)
		if err := e.Complete(t.Context(), req.RunID, succeed(protocol.OutcomeNoChange, "Still nothing")); err != nil {
			t.Fatal(err)
		}
		got := ticketOf(t, e, ticket.ID)
		if got.State != tickets.StateDone || got.PRNumber != 0 || got.PRState != "" || got.MergedAt != nil {
			t.Fatalf("after second rerun: %+v", got)
		}
	})

	t.Run("reopened closed PR is attached and can later merge", func(t *testing.T) {
		e, github, ticket := jobWithOpenPR(t)
		github.pr = PR{Number: 42, Closed: true, ChangedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
		checkPRs(t, e)

		if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
			t.Fatal(err)
		}
		// Someone reopens PR #42 on GitHub before the rerun completes.
		github.pr = PR{Number: 42}
		prReady(t, e, ticket)
		if got := ticketOf(t, e, ticket.ID); got.State != tickets.StatePRReady || got.PRNumber != 42 || got.PRState != tickets.PROpen {
			t.Fatalf("after reopened PR: %+v", got)
		}

		// Replying while #42 is open keeps it attached; merging during the run records it.
		if _, err := e.Store.Reply(t.Context(), ticket.ID, "alice", []tickets.Comment{{Body: "Merge it"}}); err != nil {
			t.Fatal(err)
		}
		merged := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
		github.pr = PR{Number: 42, Merged: true, ChangedAt: merged}
		req := assignRun(t, e)
		if err := e.Complete(t.Context(), req.RunID, succeed(protocol.OutcomeNoChange, "Merged reopened PR")); err != nil {
			t.Fatal(err)
		}
		got := ticketOf(t, e, ticket.ID)
		if got.State != tickets.StateDone || got.PRNumber != 42 || got.PRState != tickets.PRMerged || got.MergedAt == nil || !got.MergedAt.Equal(merged) {
			t.Fatalf("after merging reopened PR: %+v", got)
		}
	})
}
