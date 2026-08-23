package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func claimRunningAgentWork(t *testing.T, store *Store, requestKey string) (protocol.RunDetail, protocol.Claim) {
	t.Helper()
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Agent updates", Prompt: "Report an outcome.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: requestKey})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: requestKey, LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	return run, *claim
}

func TestAgentUpdateProgressOutcomeAndSemanticCompletion(t *testing.T) {
	store := newTestStore(t)
	run, claim := claimRunningAgentWork(t, store, "agent-update-completion")
	progressID := "10000000-0000-4000-8000-000000000001"
	progress, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: progressID, Status: protocol.WorkUpdateRunning, Message: "Running focused tests.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Sequence != 1 || progress.WorkID != run.Sessions[0].ID || progress.Actor != protocol.WorkUpdateActorAgent {
		t.Fatalf("progress = %#v", progress)
	}
	outcome, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "10000000-0000-4000-8000-000000000002",
		Status: protocol.WorkUpdateFailed, Message: "The dependency is unavailable.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Sequence != 2 {
		t.Fatalf("outcome = %#v", outcome)
	}
	attempt, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "arbitrary prose must be ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.Work(context.Background(), run.Sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != "succeeded" || attempt.Result != "" || attempt.Error != "" ||
		work.State != protocol.WorkFailed || work.FailureReason != outcome.Message ||
		work.TerminalMessage != outcome.Message || work.LatestProgress != progress.Message {
		t.Fatalf("semantic completion = Attempt %#v, Work %#v", attempt, work)
	}
}

func TestAgentUpdateExactReplayPrecedesLeaseExpiry(t *testing.T) {
	store := newTestStore(t)
	_, claim := claimRunningAgentWork(t, store, "agent-update-replay")
	request := protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "20000000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateRunning, Message: "Waiting for CI.",
	}
	stored, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `UPDATE attempts SET lease_expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).UnixMilli(), claim.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, request)
	if err != nil || replayed.ID != stored.ID {
		t.Fatalf("expired exact replay = %#v, err %v", replayed, err)
	}
	changed := request
	changed.Message = "Changed after expiry."
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, changed); !serviceErrorCode(err, "update_request_conflict") {
		t.Fatalf("changed expired replay error = %v", err)
	}
	request.RequestID = "20000000-0000-4000-8000-000000000002"
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, request); !serviceErrorCode(err, "lease_not_owner") {
		t.Fatalf("new expired request error = %v", err)
	}
	request.LeaseToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	request.RequestID = "20000000-0000-4000-8000-000000000001"
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, request); !serviceErrorCode(err, "lease_not_owner") {
		t.Fatalf("wrong-token replay error = %v", err)
	}
}

func TestReadyAgentUpdateReplayNeedsNoFreshDeliveryEvidence(t *testing.T) {
	store := newTestStore(t)
	_, claim := claimRunningAgentWork(t, store, "agent-update-ready-replay")
	request := protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "21000000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateReady, Message: "Pull request is ready.",
		PullRequestURL:        "https://github.com/owainlewis/factory/pull/342",
		PullRequestHeadBranch: "factory/work-ready",
		PullRequestHeadSHA:    strings.Repeat("a", 40),
	}
	stored, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `UPDATE attempts SET lease_expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Minute).UnixMilli(), claim.Attempt.ID); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, ReplayOnly: true, RequestID: request.RequestID,
		Status: request.Status, Message: request.Message, PullRequestURL: request.PullRequestURL,
	})
	if err != nil || replayed.ID != stored.ID || replayed.PullRequestHeadSHA != request.PullRequestHeadSHA {
		t.Fatalf("ready replay = %#v, err %v", replayed, err)
	}
}

func TestAgentUpdateReservesOutcomeAfter199ProgressReports(t *testing.T) {
	store := newTestStore(t)
	_, claim := claimRunningAgentWork(t, store, "agent-update-limit")
	for index := 0; index < protocol.MaxProgressPerAttempt; index++ {
		requestID := fmt.Sprintf("30000000-0000-4000-8000-%012d", index)
		if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
			LeaseToken: tokenA, RequestID: requestID, Status: protocol.WorkUpdateRunning, Message: "Progress.",
		}); err != nil {
			t.Fatalf("progress %d: %v", index, err)
		}
	}
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "30000000-0000-4000-8000-999999999999",
		Status: protocol.WorkUpdateRunning, Message: "Too much progress.",
	}); !serviceErrorCode(err, "progress_update_limit") {
		t.Fatalf("progress limit error = %v", err)
	}
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "30000000-0000-4000-8000-999999999998",
		Status: protocol.WorkUpdateNoChange, Message: "No defensible change exists.",
	}); err != nil {
		t.Fatalf("reserved outcome: %v", err)
	}
	page, err := store.WorkUpdates(context.Background(), claim.Session.ID, 200, 0)
	if err != nil || len(page.Updates) != protocol.MaxUpdatesPerAttempt {
		t.Fatalf("updates = %d, err %v", len(page.Updates), err)
	}
}

func TestAgentUpdateRejectsProcessExitAndOutcomeConflicts(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Legacy", Prompt: "Exit normally.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "legacy-update-reject"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "legacy-update-reject", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendAgentUpdate(context.Background(), claim.Attempt.ID, protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "40000000-0000-4000-8000-000000000001",
		Status: protocol.WorkUpdateFailed, Message: "Should be rejected.",
	}); !serviceErrorCode(err, "agent_update_not_allowed") {
		t.Fatalf("legacy update error = %v", err)
	}

	_, agentClaim := claimRunningAgentWork(t, store, "outcome-conflict")
	first := protocol.AttemptUpdateRequest{
		LeaseToken: tokenA, RequestID: "40000000-0000-4000-8000-000000000002",
		Status: protocol.WorkUpdateNoChange, Message: "Nothing to change.",
	}
	stored, err := store.AppendAgentUpdate(context.Background(), agentClaim.Attempt.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	first.RequestID = "40000000-0000-4000-8000-000000000003"
	repeated, err := store.AppendAgentUpdate(context.Background(), agentClaim.Attempt.ID, first)
	if err != nil || repeated.ID != stored.ID {
		t.Fatalf("identical second outcome = %#v, err %v", repeated, err)
	}
	first.Message = strings.Repeat("x", 8)
	first.RequestID = "40000000-0000-4000-8000-000000000004"
	if _, err := store.AppendAgentUpdate(context.Background(), agentClaim.Attempt.ID, first); !serviceErrorCode(err, "outcome_already_reported") {
		t.Fatalf("different outcome error = %v", err)
	}
}
