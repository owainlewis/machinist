package tickets

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestCheckpointPersistAndRerunFromStep(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "machinist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ticket, _, err := store.CreateTicket(t.Context(), NewTicket{
		Source:    "api",
		Repo:      "owner/repo",
		Title:     "Fix bug",
		Body:      "Please fix it",
		Agent:     "default",
		CreatedBy: "alice",
		SourceKey: "api:1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// No checkpoint before any run fails.
	if _, _, err := store.LatestCheckpoint(t.Context(), ticket.ID); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("LatestCheckpoint = %v, want ErrNoCheckpoint", err)
	}

	run1, err := store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
	if err != nil {
		t.Fatal(err)
	}
	claim1, err := store.ClaimRun(t.Context(), "runner-1", []string{protocol.StepsHarness}, store.now().Add(60_000_000_000), nil)
	if err != nil || claim1 == nil || claim1.Run.ID != run1.ID {
		t.Fatalf("claim1 = %+v, err = %v", claim1, err)
	}

	wantCP := &protocol.Checkpoint{
		FailedStep:   "review",
		HeadSHA:      "abc123",
		Patch:        []byte("patch-bytes"),
		Worktree:     []byte("worktree-bytes"),
		ControlFiles: map[string]string{"assumptions.md": "note\n", "review.json": `{"verdict":"revision_needed"}`},
	}
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{
		RunID:      run1.ID,
		Status:     RunFailed,
		FailedStep: "review",
		Checkpoint: wantCP,
		Stages: []protocol.StageResult{
			{Name: "checkout", Kind: "step", State: "succeeded"},
			{Name: "implement", Kind: "step", State: "succeeded"},
			{Name: "review", Kind: "step", State: "failed", Error: "exit 1"},
		},
	}, func(Ticket, string) Transition {
		return Transition{State: StateFailed, Message: "step review failed"}
	}); err != nil {
		t.Fatal(err)
	}

	runs, err := store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	if !runs[0].Checkpoint || runs[0].FailedStep != "review" {
		t.Fatalf("run1 = %+v", runs[0])
	}

	cpRunID, gotCP, err := store.LatestCheckpoint(t.Context(), ticket.ID)
	if err != nil || cpRunID != run1.ID {
		t.Fatalf("LatestCheckpoint = (%q, %+v, %v)", cpRunID, gotCP, err)
	}
	if gotCP.FailedStep != wantCP.FailedStep || gotCP.HeadSHA != wantCP.HeadSHA || string(gotCP.Patch) != string(wantCP.Patch) || string(gotCP.Worktree) != string(wantCP.Worktree) || gotCP.ControlFiles["assumptions.md"] != "note\n" {
		t.Fatalf("gotCP = %+v, want %+v", gotCP, wantCP)
	}

	// Resume from review.
	resumedTicket, err := store.RerunFromStep(t.Context(), ticket.ID, "alice", "review")
	if err != nil {
		t.Fatal(err)
	}
	if resumedTicket.State != StateWorking {
		t.Fatalf("resumedTicket.State = %q", resumedTicket.State)
	}
	// Cannot rerun while working.
	if _, err := store.RerunFromStep(t.Context(), ticket.ID, "alice", "review"); !errors.Is(err, ErrStillWorking) {
		t.Fatalf("expected ErrStillWorking, got %v", err)
	}

	run2, err := store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
	if err != nil {
		t.Fatal(err)
	}
	if run2.ResumeFromStep != "review" || run2.CheckpointRunID != run1.ID {
		t.Fatalf("run2 = %+v", run2)
	}
	claim2, err := store.ClaimRun(t.Context(), "runner-1", []string{protocol.StepsHarness}, store.now().Add(60_000_000_000), nil)
	if err != nil || claim2 == nil || claim2.Run.ResumeFromStep != "review" || claim2.Run.CheckpointRunID != run1.ID {
		t.Fatalf("claim2 = %+v, err = %v", claim2, err)
	}

	// Succeed run2; afterwards LatestCheckpoint returns ErrNoCheckpoint because the latest run succeeded.
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{
		RunID:  run2.ID,
		Status: RunSucceeded,
		Report: &protocol.Report{Outcome: protocol.OutcomeNoChange, Summary: "All good"},
	}, func(Ticket, string) Transition {
		return Transition{State: StateDone, Message: "All good"}
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LatestCheckpoint(t.Context(), ticket.ID); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("expected ErrNoCheckpoint after succeeded run, got %v", err)
	}
	if _, err := store.RerunFromStep(t.Context(), ticket.ID, "alice", "review"); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("expected ErrNoCheckpoint on RerunFromStep after succeeded run, got %v", err)
	}

	// Full Rerun clears resume state and queues a fresh run.
	if _, err := store.Rerun(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	run3, err := store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
	if err != nil {
		t.Fatal(err)
	}
	if run3.ResumeFromStep != "" || run3.CheckpointRunID != "" {
		t.Fatalf("expected full rerun to have no resume state, got %+v", run3)
	}
}
