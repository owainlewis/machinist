package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/ship"
	"github.com/owainlewis/machinist/internal/tickets"
)

// fakePull is one pull request on GitHub, as the control plane watches it.
type fakePull struct {
	pr       ship.PullRequest
	checks   []ship.Check
	merged   []string
	updated  int
	looks    int
	onChecks func()
}

func (f *fakePull) PullRequest(context.Context) (ship.PullRequest, error) {
	f.looks++
	return f.pr, nil
}
func (f *fakePull) Checks(context.Context) ([]ship.Check, error) {
	if f.onChecks != nil {
		f.onChecks()
	}
	return f.checks, nil
}
func (f *fakePull) Merge(_ context.Context, head string) error {
	f.merged = append(f.merged, head)
	return nil
}
func (f *fakePull) UpdateBranch(context.Context) error {
	f.updated++
	return nil
}

// handedOver runs a ticket whose run hands its pull request, #42, to the
// control plane, as machinist merge does.
func handedOver(t *testing.T, autoMerge bool, request *protocol.Ship) (*Engine, *fakePull, tickets.Ticket) {
	t.Helper()
	e := newEngine(t)
	e.AutoMerge = autoMerge
	withPR(e)
	pull := &fakePull{
		pr:     ship.PullRequest{Number: 42, URL: "https://github.com/owner/repo/pull/42", State: "OPEN", MergeStateStatus: "CLEAN", HeadRefOid: "sha1"},
		checks: []ship.Check{{Name: "test", Bucket: "pending"}},
	}
	e.PullRequests = func(repository string, number int) ship.GitHub {
		if repository != "owner/repo" || number != 42 {
			t.Errorf("watched %s#%d", repository, number)
		}
		return pull
	}
	ticket := newTicket(t, e, "api")
	run := assignRun(t, e)
	completion := succeed(protocol.OutcomeNoChange, "Merges once its checks pass: https://github.com/owner/repo/pull/42")
	completion.Ship = request
	if err := e.Complete(t.Context(), run.RunID, completion); err != nil {
		t.Fatal(err)
	}
	return e, pull, ticket
}

func checkPRs(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.CheckPRs(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// The run ends as soon as the pull request is handed over, so its runner slot
// is free, and the control plane merges it once its checks pass.
func TestAHandedOverPullRequestMergesOnceItsChecksPass(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
	runs, err := e.Store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != tickets.RunSucceeded {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	if got := ticketOf(t, e, ticket.ID); got.State != tickets.StatePRReady || got.Shipping == "" {
		t.Fatalf("ticket = %+v", got)
	}
	checkPRs(t, e)
	if got := ticketOf(t, e, ticket.ID); got.State != tickets.StatePRReady || len(pull.merged) != 0 {
		t.Fatalf("merged while a check runs: %+v, %v", got, pull.merged)
	}
	pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
	checkPRs(t, e)
	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StateDone || got.PRState != tickets.PRMerged || got.MergedAt == nil || got.Shipping != "" || len(pull.merged) != 1 || pull.merged[0] != "sha1" {
		t.Fatalf("ticket = %+v, merged %v", got, pull.merged)
	}
	if message := lastMessage(t, e, ticket.ID); message != "Merged: https://github.com/owner/repo/pull/42" {
		t.Fatalf("message = %q", message)
	}
}

// When the control plane auto-merges a PR, any human reviews submitted before
// the merge are recorded for the dashboard.
func TestAControlPlaneAutoMergeRecordsHumanReviews(t *testing.T) {
	e, pull, _ := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
	reviewed := time.Now().UTC().Add(-10 * time.Minute)
	e.Reviews = func(context.Context, string, int, time.Time) (tickets.Reviews, error) {
		return tickets.Reviews{Count: 1, First: &reviewed}, nil
	}
	pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
	checkPRs(t, e)
	stats, err := e.Store.Stats(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Cycle.Merged != 1 || stats.Cycle.Reviewed != 1 || stats.Touches.Reviews != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

// A failed check fails the job, even when the pull request would have stayed
// open, and nothing merges.
func TestAFailedCheckOnAHandedOverPullRequestFailsTheJob(t *testing.T) {
	for name, request := range map[string]protocol.Ship{
		"accepted":        {Verdict: ship.Accepted},
		"revision needed": {Verdict: ship.RevisionNeeded, Hold: "the review still asks for changes"},
	} {
		t.Run(name, func(t *testing.T) {
			e, pull, ticket := handedOver(t, true, &request)
			pull.checks = []ship.Check{{Name: "test", Bucket: "fail", Link: "https://ci.example.test/2"}}
			checkPRs(t, e)
			got := ticketOf(t, e, ticket.ID)
			if got.State != tickets.StateFailed || got.Shipping != "" || len(pull.merged) != 0 {
				t.Fatalf("ticket = %+v, merged %v", got, pull.merged)
			}
			if message := lastMessage(t, e, ticket.ID); message != `Left open: check "test" failed: https://ci.example.test/2. https://github.com/owner/repo/pull/42` {
				t.Fatalf("message = %q", message)
			}
		})
	}
}

// github.auto_merge on the control plane decides, whatever the step said.
func TestAHandedOverPullRequestStaysOpenWhenAutoMergeIsOff(t *testing.T) {
	e, pull, ticket := handedOver(t, false, &protocol.Ship{Verdict: ship.Accepted})
	pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
	checkPRs(t, e)
	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StatePRReady || got.Shipping != "" || len(pull.merged) != 0 {
		t.Fatalf("ticket = %+v, merged %v", got, pull.merged)
	}
	// The wait is over, so the pull request is no longer looked at.
	looks := pull.looks
	checkPRs(t, e)
	if pull.looks != looks {
		t.Fatalf("looked again: %d, then %d", looks, pull.looks)
	}
}

// Turning github.auto_merge off and restarting stops an in-flight pull request
// from merging once its checks pass.
func TestTurningAutoMergeOffStopsAnInFlightPullRequestFromMerging(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
	e.AutoMerge = false
	pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
	checkPRs(t, e)
	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StatePRReady || got.Shipping != "" || len(pull.merged) != 0 {
		t.Fatalf("ticket = %+v, merged %v", got, pull.merged)
	}
	if message := lastMessage(t, e, ticket.ID); message != "Left open: auto-merge is off. https://github.com/owner/repo/pull/42" {
		t.Fatalf("message = %q", message)
	}
}

func TestAPullRequestNotHandedOverIsOnlyWatched(t *testing.T) {
	e, pull, ticket := handedOver(t, true, nil)
	pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
	checkPRs(t, e)
	if got := ticketOf(t, e, ticket.ID); got.State != tickets.StatePRReady || got.Shipping != "" || pull.looks != 0 {
		t.Fatalf("ticket = %+v, looks %d", got, pull.looks)
	}
}

// A job asked to run again while its checks run stops waiting for them.
func TestRunningAJobAgainEndsTheWaitForItsChecks(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
	if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
	checkPRs(t, e)
	if got := ticketOf(t, e, ticket.ID); got.State != tickets.StateWorking || got.Shipping != "" || len(pull.merged) != 0 {
		t.Fatalf("ticket = %+v, merged %v", got, pull.merged)
	}
}

// A job cancelled or run again while GitHub is asked about its pull request
// is not looked at, so nothing merges on GitHub that the job no longer wants.
func TestAJobStoppedWhileItsPullRequestIsFoundIsNotMerged(t *testing.T) {
	for name, stop := range map[string]func(*Engine, string) error{
		"cancelled": func(e *Engine, id string) error {
			_, err := e.Store.Cancel(context.Background(), id, "alice")
			return err
		},
		"run again": func(e *Engine, id string) error {
			_, err := e.Store.Rerun(context.Background(), id, "alice")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
			pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
			e.FindPR = func(context.Context, string, string) (*PR, error) {
				if err := stop(e, ticket.ID); err != nil {
					t.Fatal(err)
				}
				return &PR{Number: 42}, nil
			}
			checkPRs(t, e)
			if pull.looks != 0 || len(pull.merged) != 0 {
				t.Fatalf("looked %d times, merged %v", pull.looks, pull.merged)
			}
		})
	}
}

// When GitHub reports a new pull request on the branch, the look that follows
// is still recorded, so the wait goes on from where it got to.
func TestALookAfterANewPullRequestResultIsKept(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
	e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: 43}, nil }
	e.PullRequests = func(string, int) ship.GitHub { return pull }
	pull.pr.Number, pull.pr.MergeStateStatus = 43, "BEHIND"
	pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
	checkPRs(t, e)
	got := ticketOf(t, e, ticket.ID)
	if got.PRNumber != 43 || !strings.Contains(got.Shipping, `"old_head":"sha1"`) || !strings.Contains(got.Shipping, `"updates":1`) {
		t.Fatalf("ticket = %+v", got)
	}
}

// A job cancelled or run again while LookOnce is fetching its checks must not
// merge or update the branch on GitHub.
func TestAJobStoppedWhileChecksAreFetchedIsNotMergedOrUpdated(t *testing.T) {
	cases := []struct {
		name      string
		mergeStat string
		stop      func(*Engine, string) error
		wantState string
	}{
		{
			name:      "cancelled before merge",
			mergeStat: "CLEAN",
			stop: func(e *Engine, id string) error {
				_, err := e.Store.Cancel(context.Background(), id, "alice")
				return err
			},
			wantState: tickets.StateCancelled,
		},
		{
			name:      "rerun before merge",
			mergeStat: "CLEAN",
			stop: func(e *Engine, id string) error {
				_, err := e.Store.Rerun(context.Background(), id, "alice")
				return err
			},
			wantState: tickets.StateWorking,
		},
		{
			name:      "cancelled before update branch",
			mergeStat: "BEHIND",
			stop: func(e *Engine, id string) error {
				_, err := e.Store.Cancel(context.Background(), id, "alice")
				return err
			},
			wantState: tickets.StateCancelled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
			pull.pr.MergeStateStatus = tc.mergeStat
			pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
			pull.onChecks = func() {
				if err := tc.stop(e, ticket.ID); err != nil {
					t.Fatal(err)
				}
			}
			checkPRs(t, e)
			got := ticketOf(t, e, ticket.ID)
			if got.State != tc.wantState || len(pull.merged) != 0 || pull.updated != 0 {
				t.Fatalf("ticket = %+v, merged=%v, updated=%d", got, pull.merged, pull.updated)
			}
		})
	}
}
