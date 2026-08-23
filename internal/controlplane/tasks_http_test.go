package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestRunSummaryCountsAttemptsWithoutReturningTheirBodies(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Review Factory", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _, err := store.RunTask(ctx, task.ID, protocol.RunTaskRequest{RequestKey: "summary-run"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := detail.Sessions[0].ID
	var executionID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM executions WHERE session_id = ?`, sessionID).Scan(&executionID); err != nil {
		t.Fatal(err)
	}
	largeResult := strings.Repeat("x", protocol.MaxResultBytes)
	for number := 1; number <= 2; number++ {
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO attempts(id, execution_id, worker_id, attempt_number, state,
			                     lease_digest, lease_expires_at, result, created_at)
			VALUES (?, ?, ?, ?, 'succeeded', X'01', 0, ?, ?)
		`, fmt.Sprintf("summary-attempt-%d", number), executionID, worker.ID, number, largeResult, number); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE sessions SET state = 'succeeded', result = 'done', terminal_at = 2 WHERE id = ?`, sessionID); err != nil {
		t.Fatal(err)
	}

	summary, err := store.RunSummary(ctx, detail.Run.ID)
	if err != nil || summary.ID != detail.Run.ID || summary.TaskName != task.Name || len(summary.Sessions) != 1 {
		t.Fatalf("Run summary = %#v, error %v", summary, err)
	}
	session := summary.Sessions[0]
	if summary.State != protocol.RunSucceeded || session.AttemptCount != 2 || session.Result != "done" || session.ID != sessionID {
		t.Fatalf("Run summary state = %#v", summary)
	}
	page, err := store.RunPage(ctx, 50, "")
	if err != nil || len(page.Runs) != 1 || page.Runs[0].State != protocol.RunSucceeded {
		t.Fatalf("Run page = %#v, error %v", page, err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE sessions SET state = 'blocked', result = NULL, blocked_reason = 'Repository is disabled.'
		WHERE id = ?
	`, sessionID); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.RunSummary(ctx, detail.Run.ID)
	if err != nil || blocked.Sessions[0].BlockedReason != "Repository is disabled." || blocked.State != protocol.RunBlocked {
		t.Fatalf("Blocked Run summary = %#v, error %v", blocked, err)
	}
}

func TestWorkerSummariesDoNotReadOperationalJSON(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	if _, err := store.db.ExecContext(ctx, `
		UPDATE workers
		SET capabilities_json = 'not-json', retained_worktrees_json = 'not-json'
		WHERE id = ?
	`, worker.ID); err != nil {
		t.Fatal(err)
	}

	page, err := store.WorkerSummaries(ctx)
	if err != nil || len(page.Workers) != 1 {
		t.Fatalf("Worker summaries = %#v, error %v", page, err)
	}
	summary := page.Workers[0]
	if summary.ID != worker.ID || summary.Name != worker.Name || summary.Capacity != worker.Capacity {
		t.Fatalf("Worker summary = %#v", summary)
	}
}
