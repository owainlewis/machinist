package engine

import (
	"errors"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

func TestEngineCheckpointAssignAndFullRerun(t *testing.T) {
	e := newEngine(t)
	ticket, _, err := e.Store.CreateTicket(t.Context(), tickets.NewTicket{
		Source:    "api",
		Repo:      "owner/repo",
		Title:     "Fix it",
		Body:      "Do the work",
		Agent:     "default",
		CreatedBy: "alice",
		SourceKey: "api:cp-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	req1, err := e.Assign(t.Context(), "runner-1", []string{protocol.StepsHarness}, nil, false)
	if err != nil || req1 == nil {
		t.Fatalf("req1 = %+v, err = %v", req1, err)
	}
	if req1.ResumeFromStep != "" || req1.Checkpoint != nil {
		t.Fatalf("first run should not resume: %+v", req1)
	}

	// Oversized checkpoint is rejected with ErrInvalidCompletion.
	oversized := &protocol.Checkpoint{
		FailedStep: "review",
		Patch:      make([]byte, protocol.MaxPatchBytes+1),
	}
	if err := e.Complete(t.Context(), req1.RunID, protocol.Completion{
		State:      tickets.RunFailed,
		Error:      "step review: exit 1",
		FailedStep: "review",
		Checkpoint: oversized,
	}); !errors.Is(err, ErrInvalidCompletion) {
		t.Fatalf("expected ErrInvalidCompletion for oversized checkpoint, got %v", err)
	}

	// Valid checkpoint is persisted on failure.
	cp := &protocol.Checkpoint{
		FailedStep:   "review",
		HeadSHA:      "deadbeef",
		Patch:        []byte("From abc\nSubject: [PATCH] Fix\n"),
		ControlFiles: map[string]string{"assumptions.md": "Small change.\n"},
	}
	if err := e.Complete(t.Context(), req1.RunID, protocol.Completion{
		State:      tickets.RunFailed,
		Error:      "step review: exit 1",
		FailedStep: "review",
		Checkpoint: cp,
		Stages: []protocol.StageResult{
			{Name: "checkout", Kind: "step", State: "succeeded"},
			{Name: "implement", Kind: "step", State: "succeeded"},
			{Name: "review", Kind: "step", State: "failed", Error: "exit 1"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Resume from review -> Assign returns ResumeFromStep and saved Checkpoint.
	if _, err := e.Store.RerunFromStep(t.Context(), ticket.ID, "alice", "review"); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	req2, err := e.Assign(t.Context(), "runner-1", []string{protocol.StepsHarness}, nil, false)
	if err != nil || req2 == nil {
		t.Fatalf("req2 = %+v, err = %v", req2, err)
	}
	if req2.ResumeFromStep != "review" || req2.Checkpoint == nil {
		t.Fatalf("expected resumed request, got %+v", req2)
	}
	if req2.Checkpoint.HeadSHA != "deadbeef" || string(req2.Checkpoint.Patch) != string(cp.Patch) || req2.Checkpoint.ControlFiles["assumptions.md"] != "Small change.\n" {
		t.Fatalf("checkpoint mismatch: %+v", req2.Checkpoint)
	}

	// If runner is lost (RunInterrupted), the automatic retry still carries ResumeFromStep and Checkpoint.
	if err := e.Complete(t.Context(), req2.RunID, protocol.Completion{State: tickets.RunCancelled}); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	req3, err := e.Assign(t.Context(), "runner-2", []string{protocol.StepsHarness}, nil, false)
	if err != nil || req3 == nil {
		t.Fatalf("req3 = %+v, err = %v", req3, err)
	}
	if req3.ResumeFromStep != "review" || req3.Checkpoint == nil || req3.Checkpoint.HeadSHA != "deadbeef" {
		t.Fatalf("expected interrupted retry to keep checkpoint, got %+v", req3)
	}

	// Fail req3 without checkpoint -> full Rerun starts from scratch without checkpoint.
	if err := e.Complete(t.Context(), req3.RunID, protocol.Completion{State: tickets.RunFailed, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	req4, err := e.Assign(t.Context(), "runner-2", []string{protocol.StepsHarness}, nil, false)
	if err != nil || req4 == nil {
		t.Fatalf("req4 = %+v, err = %v", req4, err)
	}
	if req4.ResumeFromStep != "" || req4.Checkpoint != nil {
		t.Fatalf("expected full rerun to clear resume state, got %+v", req4)
	}
}
