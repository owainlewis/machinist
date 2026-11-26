package engine

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/ship"
	"github.com/owainlewis/machinist/internal/tickets"
)

// fakePull is one pull request on GitHub, as the control plane watches it.
type fakePull struct {
	pr        ship.PullRequest
	checks    []ship.Check
	merged    []string
	mergeErr  error
	updateErr error
	updated   int
	looks     int
	onChecks  func()
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
	return f.mergeErr
}
func (f *fakePull) UpdateBranch(context.Context) error {
	f.updated++
	return f.updateErr
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
	stats, err := e.Store.Stats(t.Context(), 7, "")
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

// A transient merge error (like a gh timeout) keeps the ticket shipping so the
// next poll merges it once GitHub responds.
func TestATransientMergeFailureKeepsShippingAndRetries(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
	pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
	pull.mergeErr = &ship.TransientError{Err: errors.New("gh merge pull request failed (timeout): signal: killed")}

	if err := e.CheckPRs(t.Context()); err == nil {
		t.Fatal("CheckPRs returned nil, want transient merge error")
	}
	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StatePRReady || got.Shipping == "" || len(pull.merged) != 1 {
		t.Fatalf("after transient error: ticket = %+v, merged = %v", got, pull.merged)
	}

	pull.mergeErr = nil
	checkPRs(t, e)
	got = ticketOf(t, e, ticket.ID)
	if got.State != tickets.StateDone || got.PRState != tickets.PRMerged || got.Shipping != "" || len(pull.merged) != 2 {
		t.Fatalf("after retry: ticket = %+v, merged = %v", got, pull.merged)
	}
}

// If a transient merge error happens on the first look after a branch update,
// the adopted head commit is persisted so a subsequent unreviewed push is
// rejected instead of mistaken for Machinist's own branch update.
func TestATransientMergeFailureAfterBranchUpdateLocksTheNewHead(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
	pull.pr.MergeStateStatus = "BEHIND"
	pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
	checkPRs(t, e)
	if got := ticketOf(t, e, ticket.ID); !strings.Contains(got.Shipping, `"old_head":"sha1"`) {
		t.Fatalf("expected old_head after branch update: %+v", got)
	}

	// Branch update lands as sha2 with passing checks, but Merge times out.
	pull.pr.HeadRefOid = "sha2"
	pull.pr.MergeStateStatus = "CLEAN"
	pull.mergeErr = &ship.TransientError{Err: errors.New("gh merge pull request failed (timeout): signal: killed")}
	if err := e.CheckPRs(t.Context()); err == nil {
		t.Fatal("CheckPRs returned nil, want transient merge error")
	}
	got := ticketOf(t, e, ticket.ID)
	if strings.Contains(got.Shipping, `"old_head"`) || !strings.Contains(got.Shipping, `"head":"sha2"`) {
		t.Fatalf("normalized head was not persisted: %+v", got)
	}

	// An unreviewed commit sha3 is pushed before the retry.
	pull.pr.HeadRefOid = "sha3"
	pull.mergeErr = nil
	checkPRs(t, e)
	got = ticketOf(t, e, ticket.ID)
	if got.State != tickets.StatePRReady || got.Shipping != "" || len(pull.merged) != 1 {
		t.Fatalf("unreviewed commit sha3 should have been left open: ticket = %+v, merged = %v", got, pull.merged)
	}
	if message := lastMessage(t, e, ticket.ID); !strings.Contains(message, "commits pushed after the review") {
		t.Fatalf("message = %q", message)
	}
}

func TestMergeGateVerdictPersistsOnTicketFromShipRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		request  *protocol.Ship
		decision string
		risk     string
		reason   string
		merges   bool
	}{
		{
			name: "low risk approve",
			request: &protocol.Ship{
				Verdict:           ship.Accepted,
				Head:              "sha1",
				MergeGateDecision: ship.GateApprove,
				MergeGateRisk:     ship.RiskLow,
				MergeGateReason:   "Docs-only change.",
			},
			decision: ship.GateApprove,
			risk:     ship.RiskLow,
			reason:   "Docs-only change.",
			merges:   true,
		},
		{
			name: "medium risk held",
			request: &protocol.Ship{
				Verdict:           ship.Accepted,
				Hold:              "merge gate requires human review (medium risk): Touches API schema.",
				Head:              "sha1",
				MergeGateDecision: ship.GateApprove,
				MergeGateRisk:     ship.RiskMedium,
				MergeGateReason:   "Touches API schema.",
			},
			decision: ship.GateApprove,
			risk:     ship.RiskMedium,
			reason:   "Touches API schema.",
		},
		{
			name: "high risk held",
			request: &protocol.Ship{
				Verdict:           ship.Accepted,
				Hold:              "merge gate requires human review (high risk): Destructive migration.",
				Head:              "sha1",
				MergeGateDecision: ship.GateHumanReview,
				MergeGateRisk:     ship.RiskHigh,
				MergeGateReason:   "Destructive migration.",
			},
			decision: ship.GateHumanReview,
			risk:     ship.RiskHigh,
			reason:   "Destructive migration.",
		},
		{
			name:    "no merge gate",
			request: &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"},
			merges:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, pull, ticket := handedOver(t, true, tc.request)
			got := ticketOf(t, e, ticket.ID)
			if got.MergeGateDecision != tc.decision || got.MergeGateRisk != tc.risk || got.MergeGateReason != tc.reason {
				t.Fatalf("after handoff: ticket = %+v, want %s/%s/%q", got, tc.decision, tc.risk, tc.reason)
			}

			pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
			checkPRs(t, e)
			after := ticketOf(t, e, ticket.ID)
			if tc.merges && after.State != tickets.StateDone {
				t.Fatalf("expected merged done state, got %+v", after)
			}
			if !tc.merges && after.State != tickets.StatePRReady {
				t.Fatalf("expected held pr_ready state, got %+v", after)
			}
			if after.MergeGateDecision != tc.decision || after.MergeGateRisk != tc.risk || after.MergeGateReason != tc.reason {
				t.Fatalf("after checks: ticket = %+v, want %s/%s/%q", after, tc.decision, tc.risk, tc.reason)
			}
		})
	}
}

type recordingNotifier struct {
	notified []tickets.Ticket
}

func (r *recordingNotifier) Notify(ticket tickets.Ticket) {
	r.notified = append(r.notified, ticket)
}

func TestPRStewardRequeuesFailedCheckAndMergesWithoutCountingHumanReruns(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
	log := &bytes.Buffer{}
	notifier := &recordingNotifier{}
	e.Log = log
	e.Notify = notifier
	e.StewardRetries = 2

	pull.checks = []ship.Check{{Name: "Linux checks", Bucket: "fail", Link: "https://ci.example.test/99"}}
	checkPRs(t, e)

	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StateWorking || got.Shipping != "" || got.StewardRetries != 1 || got.PRNumber != 42 {
		t.Fatalf("after steward requeue: ticket = %+v", got)
	}
	if len(notifier.notified) != 0 {
		t.Fatalf("steward requeue should not notify a person: %+v", notifier.notified)
	}
	if !strings.Contains(log.String(), "machinist: job "+ticket.ID+": pull request #42: steward attempt 1/2:") {
		t.Fatalf("log = %q", log.String())
	}

	// Next agent run runs on the same branch and sees actionable steward instructions.
	run := assignRun(t, e)
	if run.Branch != ticket.Branch() {
		t.Fatalf("branch = %q, want %q", run.Branch, ticket.Branch())
	}
	if !strings.Contains(run.Prompt, `PR Steward (attempt 1/2): check "Linux checks" failed (https://ci.example.test/99).`) ||
		!strings.Contains(run.Prompt, "origin/HEAD") ||
		!strings.Contains(run.Prompt, "review and merge-gate") {
		t.Fatalf("prompt = %q", run.Prompt)
	}

	// Steward run fixes the failure and hands off new head sha2.
	pull.pr.HeadRefOid = "sha2"
	pull.checks = []ship.Check{{Name: "Linux checks", Bucket: "pass"}}
	completion := succeed(protocol.OutcomeNoChange, "Fixed failing check")
	completion.Ship = &protocol.Ship{Verdict: ship.Accepted, Head: "sha2"}
	if err := e.Complete(t.Context(), run.RunID, completion); err != nil {
		t.Fatal(err)
	}
	checkPRs(t, e)

	final := ticketOf(t, e, ticket.ID)
	if final.State != tickets.StateDone || final.PRState != tickets.PRMerged || len(pull.merged) != 1 || pull.merged[0] != "sha2" {
		t.Fatalf("final = %+v, merged = %v", final, pull.merged)
	}
	stats, err := e.Store.Stats(t.Context(), 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Touches.Reruns != 0 || stats.Touches.Replies != 0 {
		t.Fatalf("steward retries inflated human touches: %+v", stats.Touches)
	}
}

func TestPRStewardRequeuesMergeConflicts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mergeState string
		checks     []ship.Check
		updateErr  error
	}{
		{
			name:       "DIRTY merge state",
			mergeState: "DIRTY",
			checks:     []ship.Check{{Name: "test", Bucket: "pending"}},
		},
		{
			name:       "UpdateBranch conflict error",
			mergeState: "BEHIND",
			checks:     []ship.Check{{Name: "test", Bucket: "pass"}},
			updateErr:  errors.New("merge conflict between base and head"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
			log := &bytes.Buffer{}
			e.Log = log
			e.StewardRetries = 2
			pull.pr.MergeStateStatus = tc.mergeState
			pull.checks = tc.checks
			pull.updateErr = tc.updateErr

			checkPRs(t, e)

			got := ticketOf(t, e, ticket.ID)
			if got.State != tickets.StateWorking || got.Shipping != "" || got.StewardRetries != 1 {
				t.Fatalf("ticket = %+v", got)
			}
			if !strings.Contains(log.String(), "steward attempt 1/2: merge conflict") {
				t.Fatalf("log = %q", log.String())
			}
			run := assignRun(t, e)
			if run.Branch != ticket.Branch() || !strings.Contains(run.Prompt, "PR Steward (attempt 1/2): merge conflict") || !strings.Contains(run.Prompt, "origin/HEAD") {
				t.Fatalf("run = %+v", run)
			}

			// Resolve conflict and merge cleanly.
			pull.pr.HeadRefOid = "sha2"
			pull.pr.MergeStateStatus = "CLEAN"
			pull.updateErr = nil
			pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
			completion := succeed(protocol.OutcomeNoChange, "Resolved conflict")
			completion.Ship = &protocol.Ship{Verdict: ship.Accepted, Head: "sha2"}
			if err := e.Complete(t.Context(), run.RunID, completion); err != nil {
				t.Fatal(err)
			}
			checkPRs(t, e)

			final := ticketOf(t, e, ticket.ID)
			if final.State != tickets.StateDone || final.PRState != tickets.PRMerged || len(pull.merged) != 1 || pull.merged[0] != "sha2" {
				t.Fatalf("final = %+v, merged = %v", final, pull.merged)
			}
		})
	}
}

func TestPRStewardExhaustionFallsBackToFailedOrLeftOpen(t *testing.T) {
	t.Run("CI failure exhausts retries and fails", func(t *testing.T) {
		e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
		notifier := &recordingNotifier{}
		e.Notify = notifier
		e.StewardRetries = 2
		pull.checks = []ship.Check{{Name: "test", Bucket: "fail", Link: "https://ci.example.test/fail"}}

		for attempt := 1; attempt <= 2; attempt++ {
			checkPRs(t, e)
			got := ticketOf(t, e, ticket.ID)
			if got.State != tickets.StateWorking || got.StewardRetries != attempt {
				t.Fatalf("attempt %d: ticket = %+v", attempt, got)
			}
			run := assignRun(t, e)
			head := "sha" + string(rune('1'+attempt))
			pull.pr.HeadRefOid = head
			completion := succeed(protocol.OutcomeNoChange, "Tried fix")
			completion.Ship = &protocol.Ship{Verdict: ship.Accepted, Head: head}
			if err := e.Complete(t.Context(), run.RunID, completion); err != nil {
				t.Fatal(err)
			}
		}

		// Third failure exhausts the 2/2 cap and marks the ticket failed.
		checkPRs(t, e)
		got := ticketOf(t, e, ticket.ID)
		if got.State != tickets.StateFailed || got.Shipping != "" || got.StewardRetries != 2 {
			t.Fatalf("after exhaustion: ticket = %+v", got)
		}
		if msg := lastMessage(t, e, ticket.ID); !strings.Contains(msg, `check "test" failed`) || !strings.Contains(msg, "PR Steward exhausted 2/2 retries") {
			t.Fatalf("message = %q", msg)
		}
		if len(notifier.notified) != 1 || notifier.notified[0].State != tickets.StateFailed {
			t.Fatalf("notified = %+v", notifier.notified)
		}
	})

	t.Run("merge conflict exhausts retries and leaves PR open", func(t *testing.T) {
		e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
		notifier := &recordingNotifier{}
		e.Notify = notifier
		e.StewardRetries = 1
		pull.pr.MergeStateStatus = "DIRTY"

		checkPRs(t, e)
		run := assignRun(t, e)
		pull.pr.HeadRefOid = "sha2"
		completion := succeed(protocol.OutcomeNoChange, "Tried rebase")
		completion.Ship = &protocol.Ship{Verdict: ship.Accepted, Head: "sha2"}
		if err := e.Complete(t.Context(), run.RunID, completion); err != nil {
			t.Fatal(err)
		}

		// Second conflict exhausts 1/1 and leaves PR open for a person.
		checkPRs(t, e)
		got := ticketOf(t, e, ticket.ID)
		if got.State != tickets.StatePRReady || got.Shipping != "" || got.StewardRetries != 1 {
			t.Fatalf("after exhaustion: ticket = %+v", got)
		}
		if msg := lastMessage(t, e, ticket.ID); !strings.Contains(msg, "merge conflicts") || !strings.Contains(msg, "PR Steward exhausted 1/1 retries") {
			t.Fatalf("message = %q", msg)
		}
		if len(notifier.notified) != 1 || notifier.notified[0].State != tickets.StatePRReady {
			t.Fatalf("notified = %+v", notifier.notified)
		}
	})
}

func TestPRStewardDisabledWhenZero(t *testing.T) {
	t.Run("CI failure fails immediately", func(t *testing.T) {
		e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
		e.StewardRetries = 0
		pull.checks = []ship.Check{{Name: "test", Bucket: "fail", Link: "https://ci.example.test/2"}}
		checkPRs(t, e)
		got := ticketOf(t, e, ticket.ID)
		if got.State != tickets.StateFailed || got.StewardRetries != 0 {
			t.Fatalf("ticket = %+v", got)
		}
		if msg := lastMessage(t, e, ticket.ID); strings.Contains(msg, "PR Steward") {
			t.Fatalf("unexpected steward text when disabled: %q", msg)
		}
	})

	t.Run("merge conflict leaves open immediately", func(t *testing.T) {
		e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
		e.StewardRetries = 0
		pull.pr.MergeStateStatus = "DIRTY"
		checkPRs(t, e)
		got := ticketOf(t, e, ticket.ID)
		if got.State != tickets.StatePRReady || got.Shipping != "" || got.StewardRetries != 0 {
			t.Fatalf("ticket = %+v", got)
		}
		if msg := lastMessage(t, e, ticket.ID); strings.Contains(msg, "PR Steward") {
			t.Fatalf("unexpected steward text when disabled: %q", msg)
		}
	})
}

func TestPRStewardNeverTriggersOnHumanCommits(t *testing.T) {
	e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"})
	e.StewardRetries = 2
	// A person pushed commit sha9 on top of the reviewed head, and the branch is DIRTY with failing checks.
	pull.pr.HeadRefOid = "sha9"
	pull.pr.MergeStateStatus = "DIRTY"
	pull.checks = []ship.Check{{Name: "test", Bucket: "fail"}}

	checkPRs(t, e)
	got := ticketOf(t, e, ticket.ID)
	if got.State != tickets.StatePRReady || got.Shipping != "" || got.StewardRetries != 0 {
		t.Fatalf("human commits triggered steward: ticket = %+v", got)
	}
	if msg := lastMessage(t, e, ticket.ID); !strings.Contains(msg, "commits pushed after the review") {
		t.Fatalf("message = %q", msg)
	}
}

func TestPRStewardPreservesRetryCapAcrossControlPlaneRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tickets.db")
	pull := &fakePull{
		pr:     ship.PullRequest{Number: 42, URL: "https://github.com/owner/repo/pull/42", State: "OPEN", MergeStateStatus: "CLEAN", HeadRefOid: "sha1"},
		checks: []ship.Check{{Name: "test", Bucket: "fail", Link: "https://ci.example.test/1"}},
	}
	makeEngine := func(store *tickets.Store) *Engine {
		e := New(Options{
			Store:          store,
			Factory:        &factory.Factory{Presets: map[string]factory.Preset{"default": testPreset()}},
			RunTimeout:     time.Hour,
			Handle:         "machinist-bot",
			AutoMerge:      true,
			StewardRetries: 2,
			FindPR:         func(context.Context, string, string) (*PR, error) { return &PR{Number: 42}, nil },
			PullRequests:   func(string, int) ship.GitHub { return pull },
		})
		return e
	}

	store1, err := tickets.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	e1 := makeEngine(store1)
	ticket := newTicket(t, e1, "api")
	run1 := assignRun(t, e1)
	c1 := succeed(protocol.OutcomeNoChange, "Opened PR")
	c1.Ship = &protocol.Ship{Verdict: ship.Accepted, Head: "sha1"}
	if err := e1.Complete(t.Context(), run1.RunID, c1); err != nil {
		t.Fatal(err)
	}

	// Steward attempt 1/2 before restart.
	checkPRs(t, e1)
	run2 := assignRun(t, e1)
	pull.pr.HeadRefOid = "sha2"
	c2 := succeed(protocol.OutcomeNoChange, "Steward fix 1")
	c2.Ship = &protocol.Ship{Verdict: ship.Accepted, Head: "sha2"}
	if err := e1.Complete(t.Context(), run2.RunID, c2); err != nil {
		t.Fatal(err)
	}
	if err := store1.Close(); err != nil {
		t.Fatal(err)
	}

	// Control plane restarts with a fresh Store and Engine on the same SQLite DB.
	store2, err := tickets.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	e2 := makeEngine(store2)

	// Steward attempt 2/2 after restart.
	checkPRs(t, e2)
	got := ticketOf(t, e2, ticket.ID)
	if got.State != tickets.StateWorking || got.StewardRetries != 2 {
		t.Fatalf("after restart attempt 2: ticket = %+v", got)
	}
	run3 := assignRun(t, e2)
	pull.pr.HeadRefOid = "sha3"
	c3 := succeed(protocol.OutcomeNoChange, "Steward fix 2")
	c3.Ship = &protocol.Ship{Verdict: ship.Accepted, Head: "sha3"}
	if err := e2.Complete(t.Context(), run3.RunID, c3); err != nil {
		t.Fatal(err)
	}

	// Next failure hits the persisted 2/2 cap and fails instead of looping.
	checkPRs(t, e2)
	got = ticketOf(t, e2, ticket.ID)
	if got.State != tickets.StateFailed || got.StewardRetries != 2 {
		t.Fatalf("after restart cap: ticket = %+v", got)
	}
	if msg := lastMessage(t, e2, ticket.ID); !strings.Contains(msg, "PR Steward exhausted 2/2 retries") {
		t.Fatalf("message = %q", msg)
	}
}
