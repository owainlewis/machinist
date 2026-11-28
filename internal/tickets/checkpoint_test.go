package tickets

import (
	"errors"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestCheckpointPersistAndRerunFromStep(t *testing.T) {
	store := openStore(t)

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
		BaseRef:      "origin/HEAD",
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
	if gotCP.FailedStep != wantCP.FailedStep || gotCP.HeadSHA != wantCP.HeadSHA || gotCP.BaseRef != wantCP.BaseRef || string(gotCP.Patch) != string(wantCP.Patch) || string(gotCP.Worktree) != string(wantCP.Worktree) || gotCP.ControlFiles["assumptions.md"] != "note\n" {
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

func setupResumedRun(t *testing.T, store *Store, sourceKey string) (Ticket, Run, Run) {
	t.Helper()
	ticket, _, err := store.CreateTicket(t.Context(), NewTicket{
		Source:    "api",
		Repo:      "owner/repo",
		Title:     "Fix bug",
		Body:      "Please fix it",
		Agent:     "default",
		CreatedBy: "alice",
		SourceKey: sourceKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	run1, err := store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
	if err != nil {
		t.Fatal(err)
	}
	claim1, err := store.ClaimRun(t.Context(), "runner-1", []string{protocol.StepsHarness}, store.now().Add(60_000_000_000), nil)
	if err != nil || claim1 == nil || claim1.Run.ID != run1.ID {
		t.Fatalf("claim1 = %+v, err = %v", claim1, err)
	}
	if _, _, err := store.CompleteRun(t.Context(), RunCompletion{
		RunID:      run1.ID,
		Status:     RunFailed,
		FailedStep: "review",
		Checkpoint: &protocol.Checkpoint{
			FailedStep: "review",
			HeadSHA:    "abc123",
			BaseRef:    "origin/HEAD",
			Patch:      []byte("patch-bytes"),
		},
	}, func(Ticket, string) Transition {
		return Transition{State: StateFailed, Message: "step review failed"}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RerunFromStep(t.Context(), ticket.ID, "alice", "review"); err != nil {
		t.Fatal(err)
	}
	run2, err := store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
	if err != nil {
		t.Fatal(err)
	}
	if run2.ResumeFromStep != "review" || run2.CheckpointRunID != run1.ID {
		t.Fatalf("run2 = %+v, want resume from review/%s", run2, run1.ID)
	}
	claim2, err := store.ClaimRun(t.Context(), "runner-1", []string{protocol.StepsHarness}, store.now().Add(60_000_000_000), nil)
	if err != nil || claim2 == nil || claim2.Run.ID != run2.ID {
		t.Fatalf("claim2 = %+v, err = %v", claim2, err)
	}
	return ticket, run1, claim2.Run
}

func TestResumedRunWithMidRunInputClearsCheckpointFields(t *testing.T) {
	store := openStore(t)
	ticket, _, run2 := setupResumedRun(t, store, "api:resume-mid-input")

	// New input arrives while the resumed run is in progress.
	if _, added, err := store.AddInput(t.Context(), ticket.ID, Input{
		Kind:      EntryComment,
		Author:    "bob",
		Body:      "Please also update docs.",
		SourceKey: "api:comment:1",
	}); err != nil || !added {
		t.Fatalf("AddInput = added:%v err:%v", added, err)
	}

	// The resumed run succeeds with a PRReady transition, which CompleteRun converts
	// to StateWorking because of the unconsumed mid-run input.
	updated, transitioned, err := store.CompleteRun(t.Context(), RunCompletion{
		RunID:  run2.ID,
		Status: RunSucceeded,
		Report: &protocol.Report{Outcome: protocol.OutcomePRReady, Summary: "Opened PR"},
	}, func(Ticket, string) Transition {
		return Transition{State: StatePRReady, PRNumber: 42, Message: "Opened PR"}
	})
	if err != nil || !transitioned {
		t.Fatalf("CompleteRun = transitioned:%v err:%v", transitioned, err)
	}
	if updated.State != StateWorking {
		t.Fatalf("updated.State = %q, want %q", updated.State, StateWorking)
	}

	// The follow-up run must not inherit the stale checkpoint fields from run1.
	run3, err := store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
	if err != nil {
		t.Fatal(err)
	}
	if run3.ResumeFromStep != "" || run3.CheckpointRunID != "" {
		t.Fatalf("follow-up run inherited stale checkpoint fields: ResumeFromStep=%q CheckpointRunID=%q", run3.ResumeFromStep, run3.CheckpointRunID)
	}
	claim3, err := store.ClaimRun(t.Context(), "runner-1", []string{protocol.StepsHarness}, store.now().Add(60_000_000_000), nil)
	if err != nil || claim3 == nil || claim3.Run.ID != run3.ID {
		t.Fatalf("claim3 = %+v, err = %v", claim3, err)
	}
	if claim3.Run.ResumeFromStep != "" || claim3.Run.CheckpointRunID != "" {
		t.Fatalf("claimed follow-up run inherited stale checkpoint fields: %+v", claim3.Run)
	}
}

func TestResumedRunCheckpointRetentionByCompletionStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		wantRetain bool
	}{
		{name: "succeeded clears", status: RunSucceeded, wantRetain: false},
		{name: "failed clears", status: RunFailed, wantRetain: false},
		{name: "timed out clears", status: RunTimedOut, wantRetain: false},
		{name: "interrupted retains", status: RunInterrupted, wantRetain: true},
		{name: "runner stopped retains", status: RunCancelled, wantRetain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t)
			ticket, run1, run2 := setupResumedRun(t, store, "api:status:"+tc.status)

			updated, transitioned, err := store.CompleteRun(t.Context(), RunCompletion{
				RunID:  run2.ID,
				Status: tc.status,
			}, func(Ticket, string) Transition {
				return Transition{State: StateWorking}
			})
			if err != nil || !transitioned || updated.State != StateWorking {
				t.Fatalf("CompleteRun = (%+v, %v, %v)", updated, transitioned, err)
			}

			run3, err := store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantRetain {
				if run3.ResumeFromStep != "review" || run3.CheckpointRunID != run1.ID {
					t.Fatalf("run3 = %+v, want retained resume from review/%s", run3, run1.ID)
				}
			} else {
				if run3.ResumeFromStep != "" || run3.CheckpointRunID != "" {
					t.Fatalf("run3 = %+v, want cleared resume fields", run3)
				}
			}
		})
	}

	t.Run("expired lease retains", func(t *testing.T) {
		store := openStore(t)
		now := store.now()
		store.SetClock(func() time.Time { return now })
		ticket, run1, _ := setupResumedRun(t, store, "api:status:expired")

		now = now.Add(120 * time.Second)
		expired, err := store.ExpireLeases(t.Context())
		if err != nil || expired != 1 {
			t.Fatalf("ExpireLeases = (%d, %v), want (1, nil)", expired, err)
		}

		run3, err := store.QueueRun(t.Context(), ticket.ID, protocol.StepsHarness)
		if err != nil {
			t.Fatal(err)
		}
		if run3.ResumeFromStep != "review" || run3.CheckpointRunID != run1.ID {
			t.Fatalf("run3 = %+v, want retained resume from review/%s", run3, run1.ID)
		}
	})
}
