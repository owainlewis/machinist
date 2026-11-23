package engine

import (
	"context"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/ship"
	"github.com/owainlewis/machinist/internal/tickets"
)

// fakePull is one pull request on GitHub, as the control plane watches it.
type fakePull struct {
	pr     ship.PullRequest
	checks []ship.Check
	merged []string
	looks  int
}

func (f *fakePull) PullRequest(context.Context) (ship.PullRequest, error) {
	f.looks++
	return f.pr, nil
}
func (f *fakePull) Checks(context.Context) ([]ship.Check, error) { return f.checks, nil }
func (f *fakePull) Merge(_ context.Context, head string) error {
	f.merged = append(f.merged, head)
	return nil
}
func (f *fakePull) UpdateBranch(context.Context) error { return nil }

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
