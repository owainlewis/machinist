package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestManifestStoreUpgradesVersionOneTaskIdentity(t *testing.T) {
	root := t.TempDir()
	workerID := "11111111-1111-4111-8111-111111111111"
	legacyTaskID := "22222222-2222-4222-8222-222222222222"
	executionID := "33333333-3333-4333-8333-333333333333"
	attemptID := "44444444-4444-4444-8444-444444444444"
	repositoryID := "55555555-5555-4555-8555-555555555555"
	attempts := filepath.Join(root, "attempts")
	if err := os.Mkdir(attempts, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	body := `{
  "schema_version": 1,
  "worker_id": "` + workerID + `",
  "task_id": "` + legacyTaskID + `",
  "execution_id": "` + executionID + `",
  "attempt_id": "` + attemptID + `",
  "repository_id": "` + repositoryID + `",
  "repository_key": "factory",
  "repository_path": "/tmp/factory-repository",
  "remote_identity": "github.com/owainlewis/factory",
  "base_commit": "1111111111111111111111111111111111111111",
  "worktree_path": "` + filepath.Join(root, "worktrees", attemptID) + `",
  "branch": "factory/` + legacyTaskID[:12] + `-` + attemptID[:12] + `",
  "process_active": false,
  "lifecycle": "retained",
  "created_at": "` + now + `",
  "updated_at": "` + now + `"
}`
	path := filepath.Join(attempts, attemptID+".json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newManifestStore(root, workerID)
	manifest, err := store.load(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != manifestSchemaVersion || manifest.SessionID != legacyTaskID {
		t.Fatalf("upgraded manifest = %#v", manifest)
	}
	if _, err := store.update(attemptID, func(*attemptManifest) error { return nil }); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updated), `"task_id"`) || !strings.Contains(string(updated), `"session_id"`) ||
		!strings.Contains(string(updated), `"schema_version": 3`) {
		t.Fatalf("persisted manifest was not upgraded: %s", updated)
	}
}

func TestVerifyServerAttemptAllowsStoppedBetweenStageHandoff(t *testing.T) {
	oldSupervisor, oldGroup := int64(101), int64(102)
	manifest := attemptManifest{
		WorkerID: "worker-a", ExecutionID: "execution-a", AttemptID: "attempt-a",
		SupervisorPID: 201, SupervisorIdentity: "new-supervisor", ProcessGroupID: 202,
		Lifecycle: manifestSupervisorReady, ProcessActive: false,
	}
	attempt := protocol.Attempt{
		ID: "attempt-a", ExecutionID: "execution-a", WorkerID: "worker-a", State: "running",
		SupervisorPID: &oldSupervisor, ProcessIdentity: "old-supervisor", ProcessGroupID: &oldGroup,
	}
	if err := verifyServerAttempt(manifest, attempt); err != nil {
		t.Fatalf("between-stage handoff was not recoverable: %v", err)
	}
	manifest.Lifecycle = manifestRunning
	if err := verifyServerAttempt(manifest, attempt); err == nil {
		t.Fatal("running manifest accepted a process identity mismatch")
	}
	manifest.Lifecycle = manifestSupervisorReady
	attempt.ProcessGroupID = nil
	if err := verifyServerAttempt(manifest, attempt); err == nil {
		t.Fatal("between-stage handoff accepted a partial server identity")
	}
}

func TestManifestStoreUpgradesVersionTwoSessionIdentity(t *testing.T) {
	root := t.TempDir()
	workerID := "11111111-1111-4111-8111-111111111111"
	legacySessionID := "22222222-2222-4222-8222-222222222222"
	executionID := "33333333-3333-4333-8333-333333333333"
	attemptID := "44444444-4444-4444-8444-444444444444"
	repositoryID := "55555555-5555-4555-8555-555555555555"
	attempts := filepath.Join(root, "attempts")
	if err := os.Mkdir(attempts, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	body := `{
  "schema_version": 2,
  "worker_id": "` + workerID + `",
  "work_target_id": "` + legacySessionID + `",
  "execution_id": "` + executionID + `",
  "attempt_id": "` + attemptID + `",
  "repository_id": "` + repositoryID + `",
  "repository_key": "factory",
  "repository_path": "/tmp/factory-repository",
  "remote_identity": "github.com/owainlewis/factory",
  "base_commit": "1111111111111111111111111111111111111111",
  "worktree_path": "` + filepath.Join(root, "worktrees", attemptID) + `",
  "branch": "factory/` + legacySessionID[:12] + `-` + attemptID[:12] + `",
  "process_active": false,
  "lifecycle": "retained",
  "created_at": "` + now + `",
  "updated_at": "` + now + `"
}`
	path := filepath.Join(attempts, attemptID+".json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newManifestStore(root, workerID)
	manifest, err := store.load(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != manifestSchemaVersion || manifest.SessionID != legacySessionID {
		t.Fatalf("upgraded manifest = %#v", manifest)
	}
	if _, err := store.update(attemptID, func(*attemptManifest) error { return nil }); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updated), `"work_target_id"`) || !strings.Contains(string(updated), `"session_id"`) ||
		!strings.Contains(string(updated), `"schema_version": 3`) {
		t.Fatalf("persisted manifest was not upgraded: %s", updated)
	}
}
