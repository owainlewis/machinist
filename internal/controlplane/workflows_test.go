package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func workflowWorker() protocol.PollRequest {
	p := pollRequest("worker-a", []string{"codex"}, []string{"machinist"})
	p.Workflows = true
	return p
}
func finishStep(t *testing.T, s *Store, r *protocol.RunSpec, outcome string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"step_result": map[string]string{"outcome": outcome, "summary": "Stage " + outcome}})
	if err := s.Complete(t.Context(), r.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: r.LeaseToken, State: "succeeded", Result: body}); err != nil {
		t.Fatal(err)
	}
}
func workflowJob(t *testing.T, s *Store, approval bool) string {
	t.Helper()
	id, err := s.createLegacyWorkflowJob(t.Context(), "issue URL", "machinist", "deliver", []config.WorkflowStep{{Command: testAgent("triage", "issue URL")}, {Command: testAgent("build", "issue URL"), Approval: approval}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestWorkflowSequenceApprovalRetryAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id := workflowJob(t, s, true)
	old := workflowWorker()
	old.Workflows = false
	if run, err := s.Poll(t.Context(), old); err != nil || run != nil {
		t.Fatalf("legacy worker dispatched workflow: %v %v", run, err)
	}
	r, err := s.Poll(t.Context(), workflowWorker())
	if err != nil || r == nil || !r.Workflow {
		t.Fatalf("poll %v %v", r, err)
	}
	finishStep(t, s, r, "complete")
	// A duplicate completion must not create another build attempt.
	finishStep(t, s, r, "complete")
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snapshot, err := s.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	job := snapshot.Jobs[0]
	if len(snapshot.Jobs) != 1 || len(job.Runs) != 2 || job.State != "awaiting_approval" || job.Workflow.CurrentStep != 1 {
		t.Fatalf("job %#v", job)
	}
	if run, err := s.Poll(t.Context(), workflowWorker()); err != nil || run != nil {
		t.Fatalf("approval bypass %v %v", run, err)
	}
	build := job.Runs[1].ID
	if err = s.WorkflowAction(t.Context(), id, r.ID, "approve", false); !errors.Is(err, ErrWorkflowAction) {
		t.Fatalf("stale approval: %v", err)
	}
	if err = s.WorkflowAction(t.Context(), id, build, "approve", false); err != nil {
		t.Fatal(err)
	}
	if err = s.WorkflowAction(t.Context(), id, build, "approve", false); !errors.Is(err, ErrWorkflowAction) {
		t.Fatalf("duplicate approval: %v", err)
	}
	r, err = s.Poll(t.Context(), workflowWorker())
	if err != nil || r == nil {
		t.Fatalf("build %v %v", r, err)
	}
	if r.RenderedPrompt != "issue URL" {
		t.Fatalf("input changed: %q", r.RenderedPrompt)
	}
	finishStep(t, s, r, "blocked")
	if err = s.WorkflowAction(t.Context(), id, r.ID, "retry", false); err != nil {
		t.Fatal(err)
	}
	if err = s.WorkflowAction(t.Context(), id, r.ID, "retry", false); !errors.Is(err, ErrWorkflowAction) {
		t.Fatalf("duplicate retry: %v", err)
	}
	snapshot, _ = s.Snapshot(t.Context())
	job = snapshot.Jobs[0]
	if len(job.Runs) != 3 || job.Runs[1].Outcome != "blocked" || job.State != "awaiting_approval" {
		t.Fatalf("retry erased history %#v", job)
	}
	if err = s.WorkflowAction(t.Context(), id, job.Runs[2].ID, "approve", false); err != nil {
		t.Fatal(err)
	}
	r, err = s.Poll(t.Context(), workflowWorker())
	if err != nil || r == nil {
		t.Fatalf("retry %v %v", r, err)
	}
	finishStep(t, s, r, "complete")
	snapshot, _ = s.Snapshot(t.Context())
	if snapshot.Jobs[0].State != "succeeded" {
		t.Fatal(snapshot.Jobs[0])
	}
}
func TestWorkflowLeaseLossRequiresExplicitRecovery(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	now := time.Now()
	s.now = func() time.Time { return now }
	id := workflowJob(t, s, false)
	r, err := s.Poll(t.Context(), workflowWorker())
	if err != nil || r == nil {
		t.Fatalf("poll %v %v", r, err)
	}
	now = now.Add(leaseDuration + time.Second)
	if next, err := s.Poll(t.Context(), workflowWorker()); err != nil || next != nil {
		t.Fatalf("replayed lost lease %v %v", next, err)
	}
	snapshot, _ := s.Snapshot(t.Context())
	if snapshot.Jobs[0].State != "interrupted" {
		t.Fatal(snapshot.Jobs[0])
	}
	if err = s.WorkflowAction(t.Context(), id, r.ID, "retry", false); !errors.Is(err, ErrWorkflowAction) {
		t.Fatalf("unsafe retry %v", err)
	}
	if err = s.WorkflowAction(t.Context(), id, r.ID, "retry", true); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(t.Context(), r.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: r.LeaseToken, State: "succeeded"}); err == nil {
		t.Fatal("accepted interrupted completion")
	}
	next, err := s.Poll(t.Context(), workflowWorker())
	if err != nil || next == nil || next.ID == r.ID {
		t.Fatalf("retry %v %v", next, err)
	}
}
func TestWorkflowMissingResultStopsSequence(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	workflowJob(t, s, false)
	r, err := s.Poll(t.Context(), workflowWorker())
	if err != nil || r == nil {
		t.Fatalf("poll %v %v", r, err)
	}
	if err = s.Complete(t.Context(), r.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: r.LeaseToken, State: "succeeded", Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := s.Snapshot(t.Context())
	if snapshot.Jobs[0].State != "failed" || len(snapshot.Jobs[0].Runs) != 1 {
		t.Fatal(snapshot.Jobs[0])
	}
}

func TestWorkflowWorkerAffinityAndWholePlanCapabilities(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	build := testAgent("build", "issue")
	build.Executor = "claude"
	_, err := s.createLegacyWorkflowJob(t.Context(), "issue", "machinist", "deliver", []config.WorkflowStep{{Command: testAgent("triage", "issue")}, {Command: build}})
	if err != nil {
		t.Fatal(err)
	}
	p := workflowWorker()
	if r, err := s.Poll(t.Context(), p); err != nil || r != nil {
		t.Fatalf("incapable worker admitted %v %v", r, err)
	}
	p.Executors = append(p.Executors, "claude")
	r, err := s.Poll(t.Context(), p)
	if err != nil || r == nil {
		t.Fatalf("capable poll %v %v", r, err)
	}
	finishStep(t, s, r, "complete")
	other := p
	other.InstanceID = "worker-b"
	other.Name = "another-host"
	if r, err := s.Poll(t.Context(), other); err != nil || r != nil {
		t.Fatalf("moved workspace to another worker %v %v", r, err)
	}
	if r, err := s.Poll(t.Context(), p); err != nil || r == nil || r.Command != "build" {
		t.Fatalf("build poll %v %v", r, err)
	}
}

func TestWorkflowSchemaMigrationPreservesLegacyRun(t *testing.T) {
	path := openVersionOneDatabase(t, `
 DROP INDEX IF EXISTS jobs_active_shepherd_repository;
 ALTER TABLE jobs DROP COLUMN schedule_name;
 ALTER TABLE jobs DROP COLUMN has_shepherd;
 INSERT INTO jobs(id,prompt,repository,command,state,created_at,updated_at) VALUES('old_job','issue','machinist','build','succeeded','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
 INSERT INTO runs(id,job_id,command,command_hash,executor,repository,rendered_prompt,timeout_ms,state,result,events,token_usage) VALUES('old_run','old_job','build','hash','codex','machinist','issue',1000,'succeeded','{"retained":true}','old events',42);
 PRAGMA user_version=2;`)
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	output, err := s.RunOutput(t.Context(), "old_run")
	if err != nil || output.Events != "old events" || output.Result != `{"retained":true}` {
		t.Fatalf("lost run: %#v %v", output, err)
	}
	snapshot, err := s.Snapshot(t.Context())
	if err != nil || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].Workflow != nil || *snapshot.Jobs[0].Runs[0].TokenUsage != 42 {
		t.Fatalf("lost legacy job: %#v %v", snapshot, err)
	}
	workflowJob(t, s, false)
}

func TestWorkflowCancelPreventsProgression(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	id := workflowJob(t, s, false)
	r, err := s.Poll(t.Context(), workflowWorker())
	if err != nil || r == nil {
		t.Fatalf("poll %v %v", r, err)
	}
	if err = s.WorkflowAction(t.Context(), id, r.ID, "cancel", false); err != nil {
		t.Fatal(err)
	}
	finishStep(t, s, r, "complete")
	snapshot, _ := s.Snapshot(t.Context())
	if snapshot.Jobs[0].State != "cancelled" || len(snapshot.Jobs[0].Runs) != 1 {
		t.Fatal(snapshot.Jobs[0])
	}
	if err = s.WorkflowAction(t.Context(), id, r.ID, "retry", false); !errors.Is(err, ErrWorkflowAction) {
		t.Fatalf("cancel retry without confirmation %v", err)
	}
}

func TestWorkflowCompletionAndProgressionAreAtomic(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "db"))
	workflowJob(t, s, false)
	r, err := s.Poll(t.Context(), workflowWorker())
	if err != nil || r == nil {
		t.Fatalf("poll %v %v", r, err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_next_step BEFORE INSERT ON runs BEGIN SELECT RAISE(FAIL,'temporary storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(`{"step_result":{"outcome":"complete","summary":"ready"}}`)
	if err = s.Complete(t.Context(), r.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: r.LeaseToken, State: "succeeded", Result: body}); err == nil {
		t.Fatal("expected storage failure")
	}
	snapshot, _ := s.Snapshot(t.Context())
	job := snapshot.Jobs[0]
	if job.State != "running" || job.Runs[0].State != "running" || job.Runs[0].Outcome != "" || len(job.Runs) != 1 {
		t.Fatalf("partial progression: %#v", job)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_next_step`); err != nil {
		t.Fatal(err)
	}
	finishStep(t, s, r, "complete")
}

func TestDynamicApprovalSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id := workflowJob(t, s, false)
	r, err := s.Poll(t.Context(), workflowWorker())
	if err != nil || r == nil {
		t.Fatal(err)
	}
	body := json.RawMessage(`{"step_result":{"outcome":"complete","summary":"Medium risk","approval_required":true}}`)
	if err := s.Complete(t.Context(), r.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: r.LeaseToken, State: "succeeded", Result: body}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err := s.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	j := snap.Jobs[0]
	if j.State != "awaiting_approval" {
		t.Fatalf("approval bypass: %s", j.State)
	}
	if r, err := s.Poll(t.Context(), workflowWorker()); err != nil || r != nil {
		t.Fatalf("dispatched: %v %v", r, err)
	}
	if err := s.WorkflowAction(t.Context(), id, j.Runs[1].ID, "approve", false); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Poll(t.Context(), workflowWorker()); err != nil || r == nil {
		t.Fatalf("approved dispatch: %v %v", r, err)
	}
}

// Fixtures for workflows submitted before task specs and shared files.
func (s *Store) createLegacyWorkflowJob(ctx context.Context, prompt, repository, name string, steps []config.WorkflowStep) (string, error) {
	return s.createWorkflowJob(ctx, prompt, repository, name, steps, nil)
}
