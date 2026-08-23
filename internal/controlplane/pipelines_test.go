package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestPipelineTemplateSnapshotsAndSequencesAgentStages(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	pipeline, err := store.CreatePipeline(ctx, protocol.SavePipelineRequest{
		Name: "Build and review",
		Stages: []protocol.PipelineStage{
			{Name: "Build", Prompt: "Build {{ task.name }}: {{ task.prompt }} on {{ branch }}"},
			{Name: "Test", Prompt: "Test the work in {{ repository }} for Run {{ run.id }}"},
			{Name: "Review", Prompt: "Review {{ task.id }} on {{ branch }}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Ship Pipelines", Prompt: "Implement the feature.", Runtime: protocol.RuntimeCodex,
		PipelineID: pipeline.ID, RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "pipeline-sequence"})
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Run.Task.Pipeline.ID != pipeline.ID || len(admitted.Run.Task.Pipeline.Stages) != 3 {
		t.Fatalf("frozen Pipeline = %#v", admitted.Run.Task.Pipeline)
	}
	if len(admitted.Sessions[0].Stages) != 3 || admitted.Sessions[0].Stages[0].State != protocol.StagePending {
		t.Fatalf("admitted stages = %#v", admitted.Sessions[0].Stages)
	}
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{RequestID: "pipeline-claim", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if len(claim.Session.Stages) != 3 || !strings.Contains(claim.Session.Stages[0].Prompt, task.Name) ||
		!strings.Contains(claim.Session.Stages[0].Prompt, admitted.Sessions[0].Target.PublishBranch) ||
		!strings.Contains(claim.Session.Stages[1].Prompt, admitted.Run.ID) {
		t.Fatalf("rendered stages = %#v", claim.Session.Stages)
	}
	if claim.Session.Prompt != "" {
		t.Fatalf("claim duplicated its first stage prompt in Session.prompt: %q", claim.Session.Prompt)
	}
	if _, err := store.StartAttempt(ctx, claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{LeaseToken: tokenA, State: "succeeded"}); !serviceErrorCode(err, "pipeline_incomplete") {
		t.Fatalf("early completion error = %v", err)
	}
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 1, protocol.StartStageRequest{LeaseToken: tokenA}); !serviceErrorCode(err, "stage_predecessor_incomplete") {
		t.Fatalf("out-of-order start error = %v", err)
	}
	for position := 0; position < 3; position++ {
		if _, err := store.StartStage(ctx, claim.Attempt.ID, position, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
			t.Fatalf("start stage %d: %v", position, err)
		}
		if _, err := store.CompleteStage(ctx, claim.Attempt.ID, position, protocol.CompleteStageRequest{
			LeaseToken: tokenA, State: protocol.StageSucceeded, Result: "done",
		}); err != nil {
			t.Fatalf("complete stage %d: %v", position, err)
		}
	}
	if _, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "reviewed",
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := store.Run(ctx, admitted.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Run.State != protocol.RunSucceeded || len(completed.Sessions[0].Stages) != 3 ||
		completed.Sessions[0].Stages[2].State != protocol.StageSucceeded {
		t.Fatalf("completed Pipeline = %#v", completed)
	}
	for _, stage := range completed.Sessions[0].Stages {
		if stage.Prompt != "" || stage.Result != "" || stage.Error != "" {
			t.Fatalf("Run detail exposed stage payloads: %#v", stage)
		}
	}
	work, err := store.Work(ctx, completed.Sessions[0].ID)
	if err != nil || work.Stages[0].Prompt == "" || work.Stages[0].Result != "done" {
		t.Fatalf("Work stage detail = %#v, error %v", work.Stages, err)
	}
	largeResult := strings.Repeat("\x00", protocol.MaxResultBytes)
	largeError := strings.Repeat("\x00", protocol.MaxErrorBytes)
	largePrompt := strings.Repeat("p", protocol.MaxTaskPromptBytes)
	if _, err := store.db.ExecContext(ctx, `
		UPDATE session_stages SET prompt = ?, result = ?, error = ? WHERE session_id = ?
	`, largePrompt, largeResult, largeError, completed.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	polled, err := store.Run(ctx, admitted.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(polled)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) >= protocol.MaxBodyBytes {
		t.Fatalf("Run polling response retained stage payloads: %d bytes", len(body))
	}

	if _, err := store.UpdatePipeline(ctx, pipeline.ID, protocol.SavePipelineRequest{
		Name: "Changed later", ExpectedGeneration: pipeline.Generation,
		Stages: []protocol.PipelineStage{{Name: "Different", Prompt: "Ignore old Runs"}},
	}); err != nil {
		t.Fatal(err)
	}
	historical, err := store.Run(ctx, admitted.Run.ID)
	if err != nil || historical.Run.Task.Pipeline.Name != "Build and review" || len(historical.Run.Task.Pipeline.Stages) != 3 {
		t.Fatalf("historical snapshot changed = %#v, error %v", historical.Run.Task.Pipeline, err)
	}
}

func TestRunAdmissionRejectsRenderedPipelineThatCannotFitAClaim(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	stages := make([]protocol.PipelineStage, 9)
	for index := range stages {
		stages[index] = protocol.PipelineStage{Name: "Stage", Prompt: "{{ task.prompt }}"}
	}
	pipeline, err := store.CreatePipeline(ctx, protocol.SavePipelineRequest{Name: "Oversized claim", Stages: stages})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Too large", Prompt: strings.Repeat("\x00", 10<<10), Runtime: protocol.RuntimeCodex,
		PipelineID: pipeline.ID, RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "oversized-claim"}); !serviceErrorCode(err, "pipeline_claim_too_large") {
		t.Fatalf("oversized claim admission error = %v", err)
	}
	var runCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE request_key = 'oversized-claim'`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("oversized claim created %d Runs", runCount)
	}
}

func TestPipelineTemplateRejectsUnknownVariables(t *testing.T) {
	store := newTestStore(t)
	for _, prompt := range []string{"Use {{ previous.result }}", "Use {{ unsupported_key }}"} {
		_, err := store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
			Name: "Invalid", Stages: []protocol.PipelineStage{{Name: "Build", Prompt: prompt}},
		})
		if !serviceErrorCode(err, "unknown_pipeline_variable") {
			t.Fatalf("prompt %q error = %v", prompt, err)
		}
	}
}

func TestPipelineTemplateRejectsMultibyteStageNamesOverStorageLimit(t *testing.T) {
	store := newTestStore(t)
	_, err := store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
		Name: "Invalid stage name",
		Stages: []protocol.PipelineStage{{
			Name: strings.Repeat("🧪", 51), Prompt: "{{ task.prompt }}",
		}},
	})
	if !serviceErrorCode(err, "invalid_pipeline_stage_name") {
		t.Fatalf("multibyte stage name error = %v", err)
	}
	if _, err := store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
		Name: "Valid stage name",
		Stages: []protocol.PipelineStage{{
			Name: strings.Repeat("🧪", 50), Prompt: "{{ task.prompt }}",
		}},
	}); err != nil {
		t.Fatalf("200-byte stage name: %v", err)
	}
}

func TestPipelineStagesRequireRunningAttemptAndStopAfterCancellation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	pipeline, err := store.CreatePipeline(ctx, protocol.SavePipelineRequest{
		Name: "Two stages",
		Stages: []protocol.PipelineStage{
			{Name: "Build", Prompt: "{{ task.prompt }}"},
			{Name: "Review", Prompt: "Review the work."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Cancellation boundary", Prompt: "Build it.", Runtime: protocol.RuntimeCodex,
		PipelineID: pipeline.ID, RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "stage-guards"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{RequestID: "stage-guards", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); !serviceErrorCode(err, "attempt_not_running") {
		t.Fatalf("pre-start stage start error = %v", err)
	}
	if _, err := store.CompleteStage(ctx, claim.Attempt.ID, 0, protocol.CompleteStageRequest{
		LeaseToken: tokenA, State: protocol.StageSucceeded,
	}); !serviceErrorCode(err, "attempt_not_running") {
		t.Fatalf("pre-start stage completion error = %v", err)
	}
	detail, err := store.Run(ctx, run.Run.ID)
	if err != nil || detail.Sessions[0].Stages[0].State != protocol.StagePending {
		t.Fatalf("pre-start stage mutated = %#v, error %v", detail.Sessions[0].Stages, err)
	}
	if _, err := store.StartAttempt(ctx, claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteStage(ctx, claim.Attempt.ID, 0, protocol.CompleteStageRequest{
		LeaseToken: tokenA, State: protocol.StageSucceeded,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelSession(ctx, run.Run.ID, run.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 1, protocol.StartStageRequest{LeaseToken: tokenA}); !serviceErrorCode(err, "cancellation_requested") {
		t.Fatalf("post-cancellation stage start error = %v", err)
	}
	detail, err = store.Run(ctx, run.Run.ID)
	if err != nil || detail.Sessions[0].Stages[1].State != protocol.StagePending || detail.Sessions[0].Stages[1].StartedAt != nil {
		t.Fatalf("cancelled next stage = %#v, error %v", detail.Sessions[0].Stages[1], err)
	}
}

func TestPipelineDeleteRejectsTemplatesUsedByTasks(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	pipeline, err := store.CreatePipeline(context.Background(), protocol.SavePipelineRequest{
		Name: "Used", Stages: []protocol.PipelineStage{{Name: "Build", Prompt: "{{ task.prompt }}"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Uses Pipeline", Prompt: "Build it.", Runtime: protocol.RuntimeCodex,
		PipelineID: pipeline.ID, RepositoryIDs: []string{worker.Repositories[0].ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePipeline(context.Background(), pipeline.ID); !serviceErrorCode(err, "pipeline_in_use") {
		t.Fatalf("delete error = %v", err)
	}
}

func TestSingleStageCompatibilityCannotOverwriteAStageFailure(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Fail safely", Prompt: "Fail this stage.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "failed-stage"}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{RequestID: "failed-stage", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartAttempt(ctx, claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteStage(ctx, claim.Attempt.ID, 0, protocol.CompleteStageRequest{
		LeaseToken: tokenA, State: protocol.StageFailed, Error: "test failed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded",
	}); !serviceErrorCode(err, "pipeline_incomplete") {
		t.Fatalf("attempt success error = %v", err)
	}
}
