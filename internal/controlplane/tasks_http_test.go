package controlplane

import (
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestSummarizeRunOmitsUnboundedAttemptHistory(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	detail := protocol.RunDetail{
		Run: protocol.Run{
			ID: "run-1", Task: protocol.TaskSnapshot{Name: "Review Factory", Prompt: "private"},
			State: protocol.RunRunning, Source: "manual", AdmittedAt: now, UpdatedAt: now,
		},
		Sessions: []protocol.Session{{
			ID: "session-1", RepositoryIdentity: "github.com/owainlewis/factory",
			State: protocol.SessionSucceeded, AssignedWorkerID: "worker-1", Result: "done",
			Attempts: []protocol.Attempt{{ID: "attempt-1", Result: "large"}, {ID: "attempt-2", Result: "large"}},
		}},
	}

	summary := summarizeRun(detail)
	if summary.ID != detail.Run.ID || summary.TaskName != detail.Run.Task.Name || len(summary.Sessions) != 1 {
		t.Fatalf("Run summary = %#v", summary)
	}
	session := summary.Sessions[0]
	if session.AttemptCount != 2 || session.Result != "done" || session.ID != "session-1" {
		t.Fatalf("Session summary = %#v", session)
	}
}

func TestSummarizeWorkersOmitsLargeOperationalState(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	workers := []protocol.Worker{{
		ID: "worker-1", Name: "local", Runtime: "codex", Capacity: 10, ActiveCount: 2,
		Health: "healthy", Online: true, LastHeartbeat: now,
		Repositories:      []protocol.Repository{{ID: "repository-1", RemoteIdentity: "github.com/owainlewis/factory"}},
		RetainedWorktrees: []protocol.RetainedWorktree{{AttemptID: "attempt-1"}},
	}}

	page := summarizeWorkers(workers)
	if len(page.Workers) != 1 {
		t.Fatalf("Worker summary page = %#v", page)
	}
	summary := page.Workers[0]
	if summary.ID != workers[0].ID || summary.ActiveCount != 2 || summary.LastHeartbeat != now {
		t.Fatalf("Worker summary = %#v", summary)
	}
}
