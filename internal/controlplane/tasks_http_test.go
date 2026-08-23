package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
		UPDATE runs SET
			task_snapshot = json_set(task_snapshot, '$.prompt', ?),
			targets_snapshot = json_array(json_object('context_snapshot', ?))
		WHERE id = ?
	`, strings.Repeat("p", protocol.MaxTaskPromptBytes), strings.Repeat("c", protocol.MaxResolvedPromptBytes), detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	list, err := store.RunSummaryPage(ctx, 50, "")
	if err != nil || len(list.Runs) != 1 || list.Runs[0].ID != detail.Run.ID || list.Runs[0].TaskName != task.Name {
		t.Fatalf("Run summary page = %#v, error %v", list, err)
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 4096 || strings.Contains(string(encoded), strings.Repeat("c", 100)) {
		t.Fatalf("Run summary page includes frozen payloads: %d bytes", len(encoded))
	}
	handler := NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/v1/runs?limit=50&view=summary", nil)
	request.Host = "localhost"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var apiList protocol.RunListPage
	if response.Code != http.StatusOK {
		t.Fatalf("Run summary API status = %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &apiList); err != nil || len(apiList.Runs) != 1 || apiList.Runs[0].ID != detail.Run.ID {
		t.Fatalf("Run summary API = %#v, error %v", apiList, err)
	}
	terminalAt := store.now().UnixMilli()
	if _, err := store.db.ExecContext(ctx, `UPDATE sessions SET state = 'failed', terminal_at = ? WHERE id = ?`, terminalAt, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE runs SET terminal_at = ?, updated_at = ? WHERE id = ?`, terminalAt, terminalAt, detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	fullFailed, err := store.RunPage(ctx, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	compactFailed, err := store.RunSummaryPage(ctx, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if !fullFailed.Runs[0].NeedsAttention || compactFailed.Runs[0].NeedsAttention != fullFailed.Runs[0].NeedsAttention {
		t.Fatalf("Run summary attention parity: full=%t compact=%t", fullFailed.Runs[0].NeedsAttention, compactFailed.Runs[0].NeedsAttention)
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
