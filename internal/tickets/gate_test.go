package tickets

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func pauseAtGate(t *testing.T, store *Store, ticket Ticket, gate Gate, remainingMS *int64, withCheckpoint bool) (Ticket, Run) {
	t.Helper()
	run, err := store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
	if err != nil {
		t.Fatalf("QueueRun: %v", err)
	}
	claim, err := store.ClaimRun(t.Context(), "runner-1", []string{protocol.StepsHarness}, store.now().Add(time.Hour), func(cand Ticket) bool {
		return cand.ID == ticket.ID
	})
	if err != nil || claim == nil || claim.Run.ID != run.ID {
		t.Fatalf("ClaimRun = (%+v, %v)", claim, err)
	}
	var cp *protocol.Checkpoint
	if withCheckpoint {
		cp = &protocol.Checkpoint{
			FailedStep:   gate.Step,
			HeadSHA:      "deadbeef",
			BaseRef:      "origin/main",
			Patch:        []byte("patch"),
			Worktree:     []byte("wt"),
			ControlFiles: map[string]string{"spec.md": "the spec"},
		}
	}
	updated, transitioned, err := store.CompleteRun(t.Context(), RunCompletion{
		RunID:      run.ID,
		Status:     RunWaiting,
		Checkpoint: cp,
		Stages: []protocol.StageResult{
			{Name: "checkout", Kind: "step", State: RunSucceeded},
			{Name: gate.Step, Kind: "gate", State: RunWaiting},
		},
	}, func(Ticket, string) Transition {
		g := gate
		return Transition{
			State:                  StateWaiting,
			Gate:                   &g,
			RemainingTimeoutMillis: remainingMS,
		}
	})
	if err != nil || !transitioned {
		t.Fatalf("CompleteRun(RunWaiting) = (%+v, %v, %v)", updated, transitioned, err)
	}
	return updated, run
}

func TestGatePersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	ticket := createTicket(t, store, "api:gate-reopen")
	timeoutAt := now.Add(24 * time.Hour)
	remaining := int64(45 * time.Minute / time.Millisecond)
	paused, run := pauseAtGate(t, store, ticket, Gate{
		Step:      "approve-spec",
		Message:   "Build this spec?",
		Details:   "# Spec\nDetails here",
		TimeoutAt: &timeoutAt,
	}, &remaining, true)

	if paused.State != StateWaiting || paused.Gate == nil {
		t.Fatalf("paused = %+v", paused)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, err := reopened.Ticket(t.Context(), ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateWaiting {
		t.Fatalf("state = %q, want %q", got.State, StateWaiting)
	}
	if got.Gate == nil || got.Gate.Step != "approve-spec" || got.Gate.Message != "Build this spec?" || got.Gate.Details != "# Spec\nDetails here" {
		t.Fatalf("gate = %+v", got.Gate)
	}
	if got.Gate.WaitingSince == nil || !got.Gate.WaitingSince.Equal(now) {
		t.Fatalf("waiting_since = %v, want %v", got.Gate.WaitingSince, now)
	}
	if got.Gate.TimeoutAt == nil || !got.Gate.TimeoutAt.Equal(timeoutAt) {
		t.Fatalf("timeout_at = %v, want %v", got.Gate.TimeoutAt, timeoutAt)
	}
	if got.RemainingTimeoutMillis == nil || *got.RemainingTimeoutMillis != remaining {
		t.Fatalf("remaining_timeout_ms = %v, want %d", got.RemainingTimeoutMillis, remaining)
	}
	cpRunID, cp, err := reopened.LatestCheckpoint(t.Context(), ticket.ID)
	if err != nil || cpRunID != run.ID || cp == nil || cp.HeadSHA != "deadbeef" {
		t.Fatalf("LatestCheckpoint = (%q, %+v, %v)", cpRunID, cp, err)
	}
}

func TestApproveGateResumesWithCheckpointAndResetsTurns(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	ticket := createTicket(t, store, "api:approve")

	// Approving before waiting returns ErrNotWaiting.
	if _, err := store.ApproveGate(ctx, ticket.ID, "alice", "ok", "implement"); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("ApproveGate while working = %v, want ErrNotWaiting", err)
	}

	// Simulate an interrupted attempt before the gate so TurnsSinceHuman > 1.
	claim0 := claimRun(t, store, ticket)
	if _, _, err := store.CompleteRun(ctx, RunCompletion{RunID: claim0.Run.ID, Status: RunInterrupted}, completeTo(StateWorking)); err != nil {
		t.Fatal(err)
	}

	timeoutAt := now.Add(72 * time.Hour)
	remaining := int64(30 * time.Minute / time.Millisecond)
	_, gateRun := pauseAtGate(t, store, ticket, Gate{
		Step:      "approve-spec",
		Message:   "Build this spec?",
		Details:   "spec body",
		TimeoutAt: &timeoutAt,
	}, &remaining, true)

	if turns, err := store.TurnsSinceHuman(ctx, ticket.ID); err != nil || turns < 2 {
		t.Fatalf("TurnsSinceHuman before approve = (%d, %v), want >= 2", turns, err)
	}
	if err := store.MarkReported(ctx, ticket.ID, ReportGate); err != nil {
		t.Fatal(err)
	}

	// Empty nextStep returns ErrInvalidResumeStep.
	if _, err := store.ApproveGate(ctx, ticket.ID, "alice", "ship it", "   "); !errors.Is(err, ErrInvalidResumeStep) {
		t.Fatalf("ApproveGate empty nextStep = %v, want ErrInvalidResumeStep", err)
	}

	now = now.Add(5 * time.Minute)
	approved, err := store.ApproveGate(ctx, ticket.ID, "alice", "ship it", "implement")
	if err != nil {
		t.Fatalf("ApproveGate: %v", err)
	}
	if approved.State != StateWorking {
		t.Fatalf("state = %q, want %q", approved.State, StateWorking)
	}
	if approved.Gate == nil {
		t.Fatal("expected decision history in Gate, got nil")
	}
	if approved.Gate.Step != "" || approved.Gate.Message != "" || approved.Gate.Details != "" || approved.Gate.WaitingSince != nil || approved.Gate.TimeoutAt != nil {
		t.Fatalf("active wait fields not cleared: %+v", approved.Gate)
	}
	if approved.Gate.DecidedBy != "alice" || approved.Gate.Decision != GateDecisionApproved || approved.Gate.Reason != "ship it" || approved.Gate.DecidedAt == nil || !approved.Gate.DecidedAt.Equal(now) {
		t.Fatalf("decision history = %+v", approved.Gate)
	}
	if approved.RemainingTimeoutMillis == nil || *approved.RemainingTimeoutMillis != remaining {
		t.Fatalf("remaining_timeout_ms = %v, want %d", approved.RemainingTimeoutMillis, remaining)
	}

	// Human EntryComment was recorded and reset TurnsSinceHuman to 0.
	if turns, err := store.TurnsSinceHuman(ctx, ticket.ID); err != nil || turns != 0 {
		t.Fatalf("TurnsSinceHuman after approve = (%d, %v), want 0", turns, err)
	}
	entries, err := store.Entries(ctx, ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1]
	if last.Kind != EntryComment || last.Author != "alice" || last.Body != `Approved gate "approve-spec": ship it` {
		t.Fatalf("last entry = %+v", last)
	}

	// Next queued run inherits resume_from_step and checkpoint_run_id.
	nextRun, err := store.QueueRun(ctx, ticket.ID, protocol.StepsHarness)
	if err != nil {
		t.Fatal(err)
	}
	if nextRun.ResumeFromStep != "implement" || nextRun.CheckpointRunID != gateRun.ID {
		t.Fatalf("nextRun = %+v, want resume implement/%s", nextRun, gateRun.ID)
	}
}

func TestApproveGateRequiresCheckpoint(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	ticket := createTicket(t, store, "api:approve-no-cp")
	pauseAtGate(t, store, ticket, Gate{Step: "approve-spec", Message: "Build?"}, nil, false)

	if _, err := store.ApproveGate(ctx, ticket.ID, "alice", "", "implement"); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("ApproveGate without checkpoint = %v, want ErrNoCheckpoint", err)
	}
	got := reload(t, store, ticket.ID)
	if got.State != StateWaiting {
		t.Fatalf("ticket state changed on failed ApproveGate: %q", got.State)
	}
}

func TestDenyGateTransitionsToCancelledWithDecisionHistory(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 28, 11, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	t.Run("with reason", func(t *testing.T) {
		ticket := createTicket(t, store, "api:deny-reason")
		if _, err := store.DenyGate(ctx, ticket.ID, "alice", "nope"); !errors.Is(err, ErrNotWaiting) {
			t.Fatalf("DenyGate while working = %v, want ErrNotWaiting", err)
		}
		remaining := int64(60_000)
		pauseAtGate(t, store, ticket, Gate{Step: "approve-prod", Message: "Promote?"}, &remaining, true)

		now = now.Add(time.Minute)
		denied, err := store.DenyGate(ctx, ticket.ID, "alice", "metrics look bad")
		if err != nil {
			t.Fatalf("DenyGate: %v", err)
		}
		if denied.State != StateCancelled {
			t.Fatalf("state = %q, want %q", denied.State, StateCancelled)
		}
		if denied.RemainingTimeoutMillis != nil {
			t.Fatalf("remaining_timeout_ms = %v, want nil", denied.RemainingTimeoutMillis)
		}
		if denied.Gate == nil || denied.Gate.Step != "" || denied.Gate.WaitingSince != nil || denied.Gate.TimeoutAt != nil {
			t.Fatalf("active wait fields not cleared: %+v", denied.Gate)
		}
		if denied.Gate.DecidedBy != "alice" || denied.Gate.Decision != GateDecisionDenied || denied.Gate.Reason != "metrics look bad" || denied.Gate.DecidedAt == nil || !denied.Gate.DecidedAt.Equal(now) {
			t.Fatalf("decision history = %+v", denied.Gate)
		}
		msg, err := store.LatestMessage(ctx, ticket.ID)
		if err != nil || msg != "Denied by alice: metrics look bad" {
			t.Fatalf("LatestMessage = (%q, %v)", msg, err)
		}
	})

	t.Run("without reason", func(t *testing.T) {
		ticket := createTicket(t, store, "api:deny-no-reason")
		pauseAtGate(t, store, ticket, Gate{Step: "approve-prod", Message: "Promote?"}, nil, true)

		denied, err := store.DenyGate(ctx, ticket.ID, "bob", "")
		if err != nil {
			t.Fatalf("DenyGate: %v", err)
		}
		if denied.State != StateCancelled || denied.Gate == nil || denied.Gate.Decision != GateDecisionDenied || denied.Gate.DecidedBy != "bob" || denied.Gate.Reason != "" {
			t.Fatalf("denied = %+v", denied)
		}
		msg, err := store.LatestMessage(ctx, ticket.ID)
		if err != nil || msg != "Denied by bob." {
			t.Fatalf("LatestMessage = (%q, %v)", msg, err)
		}
	})
}

func TestCancelAndGuardsWhileWaiting(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	ticket := createTicket(t, store, "api:guards-waiting")
	timeoutAt := now.Add(time.Hour)
	remaining := int64(120_000)
	pauseAtGate(t, store, ticket, Gate{Step: "approve-spec", Message: "Build?", TimeoutAt: &timeoutAt}, &remaining, true)

	// Rerun, RerunFromStep, Reply, and DeleteTicket all return ErrStillWorking while waiting.
	if _, err := store.Rerun(ctx, ticket.ID, "alice"); !errors.Is(err, ErrStillWorking) {
		t.Fatalf("Rerun while waiting = %v, want ErrStillWorking", err)
	}
	if _, err := store.RerunFromStep(ctx, ticket.ID, "alice", "implement"); !errors.Is(err, ErrStillWorking) {
		t.Fatalf("RerunFromStep while waiting = %v, want ErrStillWorking", err)
	}
	if _, err := store.Reply(ctx, ticket.ID, "alice", []Comment{{Body: "hello"}}); !errors.Is(err, ErrStillWorking) {
		t.Fatalf("Reply while waiting = %v, want ErrStillWorking", err)
	}
	if err := store.DeleteTicket(ctx, ticket.ID); !errors.Is(err, ErrStillWorking) {
		t.Fatalf("DeleteTicket while waiting = %v, want ErrStillWorking", err)
	}

	// Cancel cancels a waiting job and clears active wait fields.
	cancelled, err := store.Cancel(ctx, ticket.ID, "carol")
	if err != nil {
		t.Fatalf("Cancel while waiting: %v", err)
	}
	if cancelled.State != StateCancelled {
		t.Fatalf("state = %q, want %q", cancelled.State, StateCancelled)
	}
	if cancelled.Gate != nil {
		t.Fatalf("expected nil Gate after cancel, got %+v", cancelled.Gate)
	}
	if cancelled.RemainingTimeoutMillis != nil {
		t.Fatalf("remaining_timeout_ms = %v, want nil", cancelled.RemainingTimeoutMillis)
	}
	msg, err := store.LatestMessage(ctx, ticket.ID)
	if err != nil || msg != "Cancelled by carol" {
		t.Fatalf("LatestMessage = (%q, %v)", msg, err)
	}
}

func TestWaitingIncludedInActiveUniqueIndexes(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	t.Run("issue", func(t *testing.T) {
		req1 := request("gh:issue:1")
		req1.IssueNumber = 42
		first, created, err := store.CreateTicket(ctx, req1)
		if err != nil || !created {
			t.Fatalf("create first = (%+v, %v, %v)", first, created, err)
		}
		pauseAtGate(t, store, first, Gate{Step: "approve", Message: "Go?"}, nil, true)

		req2 := request("gh:issue:2")
		req2.IssueNumber = 42
		dup, created, err := store.CreateTicket(ctx, req2)
		if err != nil || created || dup.ID != first.ID {
			t.Fatalf("second ticket for waiting issue = (%+v, %v, %v), want existing %s", dup, created, err, first.ID)
		}
		active, err := store.ActiveTicketFor(ctx, "owner/repo", 42)
		if err != nil || active.ID != first.ID {
			t.Fatalf("ActiveTicketFor waiting issue = (%+v, %v)", active, err)
		}
	})

	t.Run("schedule", func(t *testing.T) {
		req1 := request("sched:1")
		req1.Source = "schedule:deploy"
		first, created, err := store.CreateTicket(ctx, req1)
		if err != nil || !created {
			t.Fatalf("create first = (%+v, %v, %v)", first, created, err)
		}
		pauseAtGate(t, store, first, Gate{Step: "approve", Message: "Deploy?"}, nil, true)

		req2 := request("sched:2")
		req2.Source = "schedule:deploy"
		dup, created, err := store.CreateTicket(ctx, req2)
		if err != nil || created || dup.ID != first.ID {
			t.Fatalf("second ticket for waiting schedule = (%+v, %v, %v), want existing %s", dup, created, err, first.ID)
		}

		// A pr_ready ticket on the same schedule cannot return to working via AddInput while another is waiting.
		prReq := request("sched:pr")
		prReq.Source = "schedule:deploy-pr"
		prTicket, _, err := store.CreateTicket(ctx, prReq)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Apply(ctx, prTicket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 9}); err != nil {
			t.Fatal(err)
		}
		waitingReq := request("sched:waiting")
		waitingReq.Source = "schedule:deploy-pr"
		waitingTicket, created, err := store.CreateTicket(ctx, waitingReq)
		if err != nil || !created {
			t.Fatalf("create waiting schedule ticket = (%+v, %v, %v)", waitingTicket, created, err)
		}
		pauseAtGate(t, store, waitingTicket, Gate{Step: "approve", Message: "Deploy?"}, nil, true)

		if _, added, err := store.AddInput(ctx, prTicket.ID, Input{
			Kind:      EntryComment,
			Author:    "alice",
			Body:      "Follow up",
			SourceKey: "sched:pr:comment",
		}); !errors.Is(err, ErrStillWorking) || added {
			t.Fatalf("AddInput while another schedule ticket is waiting = (added=%v, err=%v), want (false, ErrStillWorking)", added, err)
		}
	})
}

func TestExpireGatesTransitionsExpiredOnlyAtomically(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	t0 := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return t0 })

	expiredTicket := createTicket(t, store, "api:expire-1")
	tExp := t0.Add(24 * time.Hour)
	pauseAtGate(t, store, expiredTicket, Gate{Step: "approve-prod", Message: "Promote?", TimeoutAt: &tExp}, nil, true)

	futureTicket := createTicket(t, store, "api:expire-future")
	tFuture := t0.Add(48 * time.Hour)
	pauseAtGate(t, store, futureTicket, Gate{Step: "approve-prod", Message: "Promote?", TimeoutAt: &tFuture}, nil, true)

	noTimeoutTicket := createTicket(t, store, "api:expire-none")
	pauseAtGate(t, store, noTimeoutTicket, Gate{Step: "approve-prod", Message: "Promote?"}, nil, true)

	// Sweep before deadline expires nothing.
	got, err := store.ExpireGates(ctx, t0.Add(23*time.Hour))
	if err != nil || len(got) != 0 {
		t.Fatalf("ExpireGates before deadline = (%+v, %v), want empty", got, err)
	}

	// Sweep at exact deadline expires only expiredTicket.
	got, err = store.ExpireGates(ctx, tExp)
	if err != nil || len(got) != 1 || got[0].ID != expiredTicket.ID {
		t.Fatalf("ExpireGates at deadline = (%+v, %v)", got, err)
	}
	if got[0].State != StateCancelled {
		t.Fatalf("expired state = %q, want %q", got[0].State, StateCancelled)
	}
	if got[0].Gate == nil || got[0].Gate.Decision != GateDecisionTimedOut || got[0].Gate.DecidedAt == nil || !got[0].Gate.DecidedAt.Equal(tExp) || got[0].Gate.WaitingSince != nil || got[0].Gate.TimeoutAt != nil {
		t.Fatalf("expired gate = %+v", got[0].Gate)
	}
	msg, err := store.LatestMessage(ctx, expiredTicket.ID)
	if err != nil || msg != "Gate timed out after 24h." {
		t.Fatalf("LatestMessage = (%q, %v), want %q", msg, err, "Gate timed out after 24h.")
	}

	// Other two tickets remain waiting.
	if reload(t, store, futureTicket.ID).State != StateWaiting {
		t.Fatal("futureTicket should still be waiting")
	}
	if reload(t, store, noTimeoutTicket.ID).State != StateWaiting {
		t.Fatal("noTimeoutTicket should still be waiting")
	}

	// Second sweep at same timestamp is idempotent.
	again, err := store.ExpireGates(ctx, tExp)
	if err != nil || len(again) != 0 {
		t.Fatalf("second ExpireGates = (%+v, %v), want empty", again, err)
	}
}

func TestConcurrentApproveAndExpireSingleWinner(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	t0 := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return t0 })

	ticket := createTicket(t, store, "api:gate-race")
	tExp := t0.Add(time.Hour)
	pauseAtGate(t, store, ticket, Gate{Step: "approve", Message: "Go?", TimeoutAt: &tExp}, nil, true)

	var approvedCount atomic.Int32
	var expiredCount atomic.Int32
	var wg sync.WaitGroup
	errCh := make(chan error, 4)

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := store.ApproveGate(ctx, ticket.ID, "alice", "go", "implement")
		if err == nil {
			approvedCount.Add(1)
		} else if !errors.Is(err, ErrNotWaiting) {
			errCh <- fmt.Errorf("ApproveGate: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		expired, err := store.ExpireGates(ctx, tExp)
		if err != nil {
			errCh <- fmt.Errorf("ExpireGates: %w", err)
			return
		}
		expiredCount.Add(int32(len(expired)))
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if approvedCount.Load()+expiredCount.Load() != 1 {
		t.Fatalf("approved=%d expired=%d, want exactly one winner", approvedCount.Load(), expiredCount.Load())
	}
}

func TestWaitingTicketCountsBoardAndStatsSuccessRate(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	// 1 succeeded ticket, 1 waiting ticket (via RunWaiting), 1 failed ticket, 1 active ticket.
	succeeded := createTicket(t, store, "api:stats-ok")
	timedRun(t, store, &now, succeeded, RunSucceeded, StateDone, 1, 5)

	waiting := createTicket(t, store, "api:stats-wait")
	pauseAtGate(t, store, waiting, Gate{Step: "approve", Message: "Go?"}, nil, true)

	failed := createTicket(t, store, "api:stats-fail")
	timedRun(t, store, &now, failed, RunFailed, StateFailed, 1, 2)

	_ = createTicket(t, store, "api:stats-active")

	// Stats: JobCounts.Waiting == 1 (not in Done), and RunWaiting does NOT count as failed in SuccessRate.
	stats, err := store.Stats(ctx, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	wantJobs := JobCounts{Total: 4, Active: 1, Waiting: 1, Done: 1, Failed: 1}
	if stats.Jobs != wantJobs {
		t.Fatalf("stats.Jobs = %+v, want %+v", stats.Jobs, wantJobs)
	}
	if stats.Runs.Succeeded != 1 || stats.Runs.Failed != 1 || stats.Runs.Total != 3 {
		t.Fatalf("stats.Runs = %+v, want total=3 succeeded=1 failed=1", stats.Runs)
	}
	if stats.SuccessRate == nil || *stats.SuccessRate != 0.5 {
		t.Fatalf("SuccessRate = %v, want 0.5 (RunWaiting must not count as failed)", stats.SuccessRate)
	}

	// TicketCounts and ListBoardTickets treat waiting as active/needs_person, NOT Done.
	page, err := store.ListBoardTickets(ctx, TicketFilter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := TicketCounts{Queued: 1, Waiting: 1, Done: 1, Failed: 1, NeedsPerson: 1}
	if page.Counts != wantCounts {
		t.Fatalf("board counts = %+v, want %+v", page.Counts, wantCounts)
	}
	// DoneLimit is 1, and there are 2 finished tickets (done + failed), so DoneTotal == 2.
	// The 2 active tickets (1 working + 1 waiting) must BOTH be returned uncapped!
	if page.DoneTotal == nil || *page.DoneTotal != 2 {
		t.Fatalf("DoneTotal = %v, want 2", page.DoneTotal)
	}
	foundWaiting := false
	for _, tk := range page.Tickets {
		if tk.ID == waiting.ID {
			foundWaiting = true
		}
	}
	if !foundWaiting {
		t.Fatalf("waiting ticket %s missing from ListBoardTickets: %+v", waiting.ID, page.Tickets)
	}
}
