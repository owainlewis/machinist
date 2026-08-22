package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestOutcomeContractConversionFreezesAdmittedRuns(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Contract conversion", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.OutcomeContract != protocol.OutcomeProcessExit {
		t.Fatalf("legacy Task outcome contract = %q", task.OutcomeContract)
	}
	legacyRun, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "before-conversion",
	})
	if err != nil {
		t.Fatal(err)
	}
	converted, err := store.SetTaskOutcomeContract(context.Background(), task.ID, protocol.SetTaskOutcomeContractRequest{
		OutcomeContract: protocol.OutcomeAgentUpdate, ExpectedGeneration: task.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if converted.Generation != task.Generation+1 || converted.OutcomeContract != protocol.OutcomeAgentUpdate {
		t.Fatalf("converted Task = %#v", converted)
	}
	if _, err := store.SetTaskOutcomeContract(context.Background(), task.ID, protocol.SetTaskOutcomeContractRequest{
		OutcomeContract: protocol.OutcomeProcessExit, ExpectedGeneration: converted.Generation,
	}); !serviceErrorCode(err, "outcome_contract_conversion_invalid") {
		t.Fatalf("reverse conversion error = %v", err)
	}
	legacyRun, err = store.Run(context.Background(), legacyRun.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if legacyRun.Run.OutcomeContract != protocol.OutcomeProcessExit ||
		legacyRun.Run.Task.OutcomeContract != protocol.OutcomeProcessExit {
		t.Fatalf("admitted Run contract changed = %#v", legacyRun.Run)
	}
	agentRun, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "after-conversion",
	})
	if err != nil {
		t.Fatal(err)
	}
	if agentRun.Run.OutcomeContract != protocol.OutcomeAgentUpdate ||
		agentRun.Run.Execution.Backend != protocol.BackendPersistent {
		t.Fatalf("new agent-directed Run = %#v", agentRun.Run)
	}
}

func TestAgentUpdateRejectsFakeCloudWithoutStateChange(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Legacy synthetic", protocol.RuntimeCodex, "succeeded")
	task := createProfileTask(t, store, worker.Repositories[0].ID, profile.ID)

	if _, err := store.SetTaskOutcomeContract(context.Background(), task.ID, protocol.SetTaskOutcomeContractRequest{
		OutcomeContract: protocol.OutcomeAgentUpdate, ExpectedGeneration: task.Generation,
	}); !serviceErrorCode(err, "agent_update_backend_unsupported") {
		t.Fatalf("conversion error = %v", err)
	}
	unchanged, err := store.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Generation != task.Generation || unchanged.OutcomeContract != protocol.OutcomeProcessExit {
		t.Fatalf("rejected conversion changed Task = %#v", unchanged)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "legacy-cloud-next"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, err = store.Run(context.Background(), run.Run.ID)
	if err != nil || run.Run.State != protocol.RunSucceeded || run.Run.OutcomeContract != protocol.OutcomeProcessExit {
		t.Fatalf("legacy synthetic completion = %#v, err %v", run, err)
	}

	persistent, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Agent persistent", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunTask(context.Background(), persistent.ID, protocol.RunTaskRequest{
		RequestKey: "agent-cloud-override", ExecutionProfileID: profile.ID,
	}); !serviceErrorCode(err, "agent_update_backend_unsupported") {
		t.Fatalf("agent-update admission override error = %v", err)
	}
	var runCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE request_key = 'agent-cloud-override'`).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatal("unsupported agent-update admission wrote a Run")
	}
}

func TestWorkLifecycleStatesTargetsAndUpdateBounds(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Durable Work", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "durable-work"})
	if err != nil {
		t.Fatal(err)
	}
	workID := run.Sessions[0].ID
	if len(run.Run.Targets) != 1 || run.Run.Targets[0].ID != workID ||
		run.Run.Targets[0].Position != 0 || run.Run.Targets[0].PublishBranch == "" {
		t.Fatalf("frozen ordered targets = %#v", run.Run.Targets)
	}
	if _, err := store.db.Exec(`
		UPDATE sessions SET execution_owner = 'worker_attempt' WHERE id = ?
	`, workID); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("queued Work accepted an owner: %v", err)
	}
	if _, err := store.db.Exec(`
		UPDATE sessions SET state = 'running', execution_owner = 'none' WHERE id = ?
	`, workID); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("running Work accepted no owner: %v", err)
	}
	if _, err := store.db.Exec(`
		UPDATE sessions SET state = 'preparing', execution_owner = 'operator' WHERE id = ?
	`, workID); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("preparing Work accepted operator ownership: %v", err)
	}
	unchanged, err := store.Work(context.Background(), workID)
	if err != nil || unchanged.State != protocol.WorkQueued || unchanged.ExecutionOwner != protocol.ExecutionOwnerNone {
		t.Fatalf("invalid ownership changed Work = %#v, err %v", unchanged, err)
	}

	states := []struct {
		state protocol.WorkState
		set   string
		args  []any
	}{
		{protocol.WorkQueued, `execution_owner = 'none', terminal_at = NULL`, nil},
		{protocol.WorkRunning, `execution_owner = 'worker_attempt', terminal_at = NULL`, nil},
		{protocol.WorkNeedsInput, `execution_owner = 'none', question = 'Which behavior?', checkpoint_sha = 'abc', pending_resume_sha = 'abc', terminal_at = NULL`, nil},
		{protocol.WorkReady, `execution_owner = 'none', pull_request_url = 'https://github.com/owainlewis/factory/pull/1', terminal_at = 1`, nil},
		{protocol.WorkFailed, `execution_owner = 'none', terminal_at = 1`, nil},
		{protocol.WorkNoChange, `execution_owner = 'none', terminal_at = 1`, nil},
		{protocol.WorkCancelled, `execution_owner = 'none', terminal_at = 1`, nil},
		{protocol.WorkSucceeded, `execution_owner = 'none', terminal_at = 1`, nil},
	}
	for _, test := range states {
		if _, err := store.db.Exec(`UPDATE sessions SET state = ?, `+test.set+` WHERE id = ?`, test.state, workID); err != nil {
			t.Fatalf("store state %q: %v", test.state, err)
		}
		work, err := store.Work(context.Background(), workID)
		if err != nil || work.State != test.state {
			t.Fatalf("read state %q = %#v, err %v", test.state, work, err)
		}
	}

	if _, err := store.db.Exec(`UPDATE sessions SET state = 'queued', execution_owner = 'none', terminal_at = NULL WHERE id = ?`, workID); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "bounded-updates", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	for index := 1; index <= protocol.MaxProgressPerAttempt; index++ {
		_, err := store.db.Exec(`
			INSERT INTO work_updates(id, work_id, attempt_id, request_id, sequence, status, message, actor, accepted_at)
			VALUES (?, ?, ?, ?, ?, 'running', 'progress', 'agent', ?)
		`, fmt.Sprintf("update-%03d", index), workID, claim.Attempt.ID,
			fmt.Sprintf("request-%03d", index), index, index)
		if err != nil {
			t.Fatalf("progress update %d: %v", index, err)
		}
	}
	if _, err := store.db.Exec(`
		INSERT INTO work_updates(id, work_id, attempt_id, request_id, sequence, status, message, actor, accepted_at)
		VALUES ('too-many-progress', ?, ?, 'too-many-progress', 200, 'running', 'progress', 'agent', 200)
	`, workID, claim.Attempt.ID); err == nil || !strings.Contains(err.Error(), "progress update limit") {
		t.Fatalf("200th progress error = %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO work_updates(id, work_id, attempt_id, request_id, sequence, status, message, actor, accepted_at)
		VALUES ('outcome', ?, ?, 'outcome', 200, 'failed', 'blocked', 'agent', 200)
	`, workID, claim.Attempt.ID); err != nil {
		t.Fatalf("reserved outcome slot: %v", err)
	}
	page, err := store.WorkUpdates(context.Background(), workID, 100, 0)
	if err != nil || len(page.Updates) != 100 || !page.HasMore || page.NextAfter != 100 {
		t.Fatalf("bounded update page = %#v, err %v", page, err)
	}
}

func TestRetryRejectsReplacedWorkTransactionally(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Replacement guard", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE sessions SET state = 'failed', terminal_at = 1, terminal_message = 'failed'
		WHERE id = ?
	`, first.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE sessions SET predecessor_work_id = ? WHERE id = ?`,
		first.Sessions[0].ID, second.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetrySession(context.Background(), first.Run.ID, first.Sessions[0].ID); !serviceErrorCode(err, "work_replaced") {
		t.Fatalf("retry replaced Work error = %v", err)
	}
	work, err := store.Work(context.Background(), first.Sessions[0].ID)
	if err != nil || work.State != protocol.WorkFailed || work.RetryMayRepeatEffects {
		t.Fatalf("rejected retry changed Work = %#v, err %v", work, err)
	}
}

func TestRepositoryRetryRejectsMatchingNonterminalLineage(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Lineage guard", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	runs := make([]protocol.RunDetail, 3)
	for index := range runs {
		runs[index], _, err = store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
			RequestKey: fmt.Sprintf("lineage-%d", index),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	rootID := runs[0].Sessions[0].ID
	retriedID := runs[1].Sessions[0].ID
	activeSiblingID := runs[2].Sessions[0].ID
	if _, err := store.db.Exec(`
		UPDATE sessions
		SET state = 'failed', terminal_at = 1, assigned_worker_id = NULL
		WHERE id IN (?, ?)
	`, rootID, retriedID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE sessions SET predecessor_work_id = ? WHERE id IN (?, ?)
	`, rootID, retriedID, activeSiblingID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetrySession(context.Background(), runs[1].Run.ID, retriedID); !serviceErrorCode(err, "matching_work_active") {
		t.Fatalf("same-lineage retry error = %v, want matching_work_active", err)
	}
	work, err := store.Work(context.Background(), retriedID)
	if err != nil || work.State != protocol.WorkFailed || work.RetryMayRepeatEffects {
		t.Fatalf("rejected lineage retry changed Work = %#v, err %v", work, err)
	}
}

func TestRepositoryRetryAllowsIndependentProcedureRun(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Independent retry", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "failed-root"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "independent-active-root",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE sessions SET state = 'failed', terminal_at = 1, assigned_worker_id = NULL WHERE id = ?
	`, failed.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetrySession(context.Background(), failed.Run.ID, failed.Sessions[0].ID); err != nil {
		t.Fatalf("independent Procedure Run blocked retry: %v", err)
	}
}

func TestMaximumLegacyProcessExitPromptRemainsAdmissible(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Maximum legacy prompt", Prompt: strings.Repeat("x", protocol.MaxTaskPromptBytes),
		Runtime: protocol.RuntimeCodex, RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "maximum-legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Run.OutcomeContract != protocol.OutcomeProcessExit || len(run.Sessions[0].ResolvedPrompt) != protocol.MaxTaskPromptBytes {
		t.Fatalf("maximum legacy Run = %#v", run.Run)
	}
}

func TestProcessExitCompletionPreservesMaximumLegacyPayloads(t *testing.T) {
	tests := []struct {
		name, state, result, failure string
	}{
		{name: "result", state: "succeeded", result: strings.Repeat("r", protocol.MaxResultBytes)},
		{name: "error", state: "failed", failure: strings.Repeat("e", protocol.MaxErrorBytes)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
				Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
			})
			task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
				Name: "Maximum " + test.name, Prompt: "Review.", Runtime: protocol.RuntimeCodex,
				RepositoryIDs: []string{worker.Repositories[0].ID},
			})
			if err != nil {
				t.Fatal(err)
			}
			run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
				RequestKey: "maximum-" + test.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
				RequestID: "maximum-" + test.name, LeaseToken: tokenA,
			})
			if err != nil || claim == nil {
				t.Fatalf("claim = %#v, err %v", claim, err)
			}
			if test.state == "succeeded" {
				if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{
					LeaseToken: tokenA,
				}); err != nil {
					t.Fatal(err)
				}
			}
			attempt, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
				LeaseToken: tokenA, State: test.state, Result: test.result, Error: test.failure,
			})
			if err != nil {
				t.Fatal(err)
			}
			work, err := store.Work(context.Background(), run.Sessions[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if attempt.Result != test.result || attempt.Error != test.failure ||
				work.Result != test.result || work.FailureReason != test.failure {
				t.Fatal("maximum process-exit payload was not preserved")
			}
			if work.TerminalMessage != "" {
				t.Fatalf("process-exit payload copied to terminal message: bytes=%d", len(work.TerminalMessage))
			}
		})
	}
}

func TestFakeCloudCompletionPreservesMaximumLegacyResult(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	maximumResult := strings.Repeat("r", protocol.MaxResultBytes)
	profile, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
		Name: "Maximum result cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
		Provider: "openrouter", Model: "test", Enabled: true, Healthy: true,
		FakeOutcome: "succeeded", FakeResult: maximumResult,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := createProfileTask(t, store, worker.Repositories[0].ID, profile.ID)
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "maximum-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	work, err := store.Work(context.Background(), run.Sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if work.Result != maximumResult || work.TerminalMessage != "" {
		t.Fatalf("maximum fake-cloud result compatibility: result bytes=%d terminal bytes=%d",
			len(work.Result), len(work.TerminalMessage))
	}
}
