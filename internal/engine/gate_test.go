package engine

import (
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/tickets"
)

func TestTickExpiresTimedOutGates(t *testing.T) {
	e := newEngine(t)
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	e.Store.SetClock(func() time.Time { return now })

	expired := newTicket(t, e, "api-expired")
	future := newTicket(t, e, "api-future")
	noTimeout := newTicket(t, e, "api-no-timeout")

	tExp := now.Add(time.Hour)
	tFuture := now.Add(3 * time.Hour)
	if _, _, err := e.Store.Apply(t.Context(), expired.ID, tickets.StateWorking, tickets.Transition{
		State: tickets.StateWaiting,
		Gate:  &tickets.Gate{Step: "approve", Message: "Go?", TimeoutAt: &tExp},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Store.Apply(t.Context(), future.ID, tickets.StateWorking, tickets.Transition{
		State: tickets.StateWaiting,
		Gate:  &tickets.Gate{Step: "approve", Message: "Go?", TimeoutAt: &tFuture},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Store.Apply(t.Context(), noTimeout.ID, tickets.StateWorking, tickets.Transition{
		State: tickets.StateWaiting,
		Gate:  &tickets.Gate{Step: "approve", Message: "Go?"},
	}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Hour)
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}

	gotExpired, err := e.Store.Ticket(t.Context(), expired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotExpired.State != tickets.StateCancelled || gotExpired.Gate == nil || gotExpired.Gate.Decision != tickets.GateDecisionTimedOut {
		t.Fatalf("expired ticket = %+v", gotExpired)
	}
	if msg := lastMessage(t, e, expired.ID); msg != "Gate timed out after 1h." {
		t.Fatalf("expired message = %q, want %q", msg, "Gate timed out after 1h.")
	}
	if state(t, e, future.ID) != tickets.StateWaiting {
		t.Fatalf("future ticket state = %q, want waiting", state(t, e, future.ID))
	}
	if state(t, e, noTimeout.ID) != tickets.StateWaiting {
		t.Fatalf("noTimeout ticket state = %q, want waiting", state(t, e, noTimeout.ID))
	}

	// No run was queued for any of the waiting or timed-out tickets.
	req, err := e.Assign(t.Context(), "runner-1", []string{protocol.StepsHarness}, nil, false)
	if err != nil || req != nil {
		t.Fatalf("Assign = (%+v, %v), want nil", req, err)
	}
}

func TestCompleteRunWaitingAndAssignUsesRemainingTimeoutBudget(t *testing.T) {
	e := newEngine(t)
	e.Factory = &factory.Factory{Presets: map[string]factory.Preset{
		"default": {
			Name:    "default",
			Timeout: 60 * time.Minute,
			Steps: []runner.Step{
				{Name: "checkout", Run: "checkout.sh"},
				{Name: "spec", Run: "spec.sh"},
				{Name: "implement", Run: "implement.sh"},
			},
		},
	}}
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	e.Store.SetClock(func() time.Time { return now })

	ticket := newTicket(t, e, "api")
	req1 := assignRun(t, e)
	if req1.TimeoutMillis != (60 * time.Minute).Milliseconds() {
		t.Fatalf("req1.TimeoutMillis = %d, want %d", req1.TimeoutMillis, (60 * time.Minute).Milliseconds())
	}

	// Run 1 runs for 15 minutes and pauses at a gate (RunWaiting).
	now = now.Add(15 * time.Minute)
	cp := &protocol.Checkpoint{FailedStep: "approve-spec", HeadSHA: "abc123", BaseRef: "origin/main"}
	if err := e.Complete(t.Context(), req1.RunID, protocol.Completion{
		State:      tickets.RunWaiting,
		Checkpoint: cp,
		Stages: []protocol.StageResult{
			{Name: "checkout", Kind: "step", State: tickets.RunSucceeded},
			{Name: "spec", Kind: "step", State: tickets.RunSucceeded},
		},
	}); err != nil {
		t.Fatalf("Complete(RunWaiting): %v", err)
	}

	paused, err := e.Store.Ticket(t.Context(), ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.State != tickets.StateWaiting {
		t.Fatalf("state = %q, want waiting", paused.State)
	}
	wantRemaining := (45 * time.Minute).Milliseconds()
	if paused.RemainingTimeoutMillis == nil || *paused.RemainingTimeoutMillis != wantRemaining {
		t.Fatalf("RemainingTimeoutMillis = %v, want %d", paused.RemainingTimeoutMillis, wantRemaining)
	}

	// Wait 5 hours at the gate — wait time does NOT consume run budget.
	now = now.Add(5 * time.Hour)
	if _, err := e.Store.ApproveGate(t.Context(), ticket.ID, "alice", "looks good", "implement"); err != nil {
		t.Fatalf("ApproveGate: %v", err)
	}

	// Resumed run receives remaining 45m budget instead of full 60m preset timeout.
	req2 := assignRun(t, e)
	if req2.TimeoutMillis != wantRemaining {
		t.Fatalf("req2.TimeoutMillis = %d, want %d", req2.TimeoutMillis, wantRemaining)
	}
	if req2.ResumeFromStep != "implement" || req2.Checkpoint == nil || req2.Checkpoint.HeadSHA != "abc123" {
		t.Fatalf("req2 resume = (%q, %+v)", req2.ResumeFromStep, req2.Checkpoint)
	}

	// Run 2 runs for another 20 minutes and hits a second gate -> 25m remaining.
	now = now.Add(20 * time.Minute)
	if err := e.Complete(t.Context(), req2.RunID, protocol.Completion{
		State:      tickets.RunWaiting,
		Checkpoint: &protocol.Checkpoint{FailedStep: "approve-prod", HeadSHA: "def456", BaseRef: "origin/main"},
	}); err != nil {
		t.Fatalf("Complete(second RunWaiting): %v", err)
	}
	if _, err := e.Store.ApproveGate(t.Context(), ticket.ID, "alice", "ship", "implement"); err != nil {
		t.Fatalf("second ApproveGate: %v", err)
	}
	req3 := assignRun(t, e)
	wantRemaining2 := (25 * time.Minute).Milliseconds()
	if req3.TimeoutMillis != wantRemaining2 {
		t.Fatalf("req3.TimeoutMillis = %d, want %d", req3.TimeoutMillis, wantRemaining2)
	}
}

func TestAssignFailsRunWhenRemainingTimeoutBudgetExhausted(t *testing.T) {
	e := newEngine(t)
	now := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	e.Store.SetClock(func() time.Time { return now })

	ticket := newTicket(t, e, "api")
	req1 := assignRun(t, e)

	// Run consumes entire 1h budget before pausing at gate.
	now = now.Add(65 * time.Minute)
	if err := e.Complete(t.Context(), req1.RunID, protocol.Completion{
		State:      tickets.RunWaiting,
		Checkpoint: &protocol.Checkpoint{FailedStep: "approve", HeadSHA: "abc123"},
	}); err != nil {
		t.Fatalf("Complete(RunWaiting): %v", err)
	}
	paused, err := e.Store.Ticket(t.Context(), ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.RemainingTimeoutMillis == nil || *paused.RemainingTimeoutMillis != 0 {
		t.Fatalf("RemainingTimeoutMillis = %v, want 0", paused.RemainingTimeoutMillis)
	}

	if _, err := e.Store.ApproveGate(t.Context(), ticket.ID, "alice", "", "work"); err != nil {
		t.Fatalf("ApproveGate: %v", err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Assign must fail the run rather than returning a 0ms RunRequest.
	req2, err := e.Assign(t.Context(), "runner-1", []string{protocol.StepsHarness}, nil, false)
	if err != nil || req2 != nil {
		t.Fatalf("Assign with exhausted budget = (%+v, %v), want (nil, nil)", req2, err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateFailed {
		t.Fatalf("state = %q, want %q", got, tickets.StateFailed)
	}
	if msg := lastMessage(t, e, ticket.ID); msg == "" {
		t.Fatal("expected failure message when budget exhausted")
	}
}
