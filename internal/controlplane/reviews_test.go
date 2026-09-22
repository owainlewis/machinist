package controlplane

import (
	"errors"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"strings"
	"testing"
)

func TestReviewRevisionPreservesVersionsAndRebindsApproval(t *testing.T) {
	path := t.TempDir() + "/db"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	steps := []config.WorkflowStep{{ID: "plan", Command: testAgent("plan", "{{task.spec}}"), RequiredOutputs: []string{"plan.md"}}, {ID: "build", Approval: true, Command: testAgent("build", "{{inputs.plan}}"), Inputs: map[string]string{"plan": "plan/plan.md"}}}
	id, r := artifactTask(t, s, steps)
	publish := func(run *protocol.RunSpec, body string) protocol.Artifact {
		t.Helper()
		a, e := s.PublishArtifact(t.Context(), run.ID, "worker-a", run.LeaseToken, "plan.md", strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		e = s.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", Result: []byte(`{"step_result":{"outcome":"complete","summary":"Plan ready"}}`), Artifacts: []string{a.ID}})
		if e != nil {
			t.Fatal(e)
		}
		return a
	}
	first := publish(r, "Original plan")
	snap, _ := s.Snapshot(t.Context())
	gate := snap.Jobs[0].Runs[1]
	if gate.ReviewedRunID != r.ID {
		t.Fatalf("unbound review: %+v", gate)
	}
	if e := s.WorkflowAction(t.Context(), id, gate.ID, "request_changes", false, "  "); !errors.Is(e, ErrWorkflowAction) {
		t.Fatal(e)
	}
	feedback := "Include migration {{task.spec}}"
	if e := s.WorkflowAction(t.Context(), id, gate.ID, "request_changes", false, feedback); e != nil {
		t.Fatal(e)
	}
	for _, action := range []string{"approve", "request_changes"} {
		if e := s.WorkflowAction(t.Context(), id, gate.ID, action, false, feedback); !errors.Is(e, ErrWorkflowAction) {
			t.Fatalf("stale %s: %v", action, e)
		}
	}
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	old := workflowWorker()
	old.Artifacts = true
	if got, e := s.Poll(t.Context(), old); e != nil || got != nil {
		t.Fatalf("old worker got revision: %+v %v", got, e)
	}
	worker := old
	worker.Reviews = true
	revision, e := s.Poll(t.Context(), worker)
	if e != nil || revision == nil {
		t.Fatalf("%+v %v", revision, e)
	}
	if revision.Command != "plan" || revision.Revision.Feedback != feedback || revision.Revision.PreviousRunID != r.ID || revision.Task.Spec != "literal {{stage.output_dir}}" {
		t.Fatalf("bad revision %+v", revision)
	}
	if revision.Inputs["__review_"+first.ID].ID != first.ID {
		t.Fatal("missing original output")
	}
	// A failed revision remains recoverable with its feedback intact.
	finishStep(t, s, revision, "failed")
	if e = s.WorkflowAction(t.Context(), id, revision.ID, "retry", false); e != nil {
		t.Fatal(e)
	}
	revision, e = s.Poll(t.Context(), worker)
	if e != nil || revision.Revision.Feedback != feedback {
		t.Fatalf("feedback lost on retry: %v %v", revision, e)
	}
	second := publish(revision, "Revised plan")
	snap, _ = s.Snapshot(t.Context())
	job := snap.Jobs[0]
	nextGate := job.Runs[len(job.Runs)-1]
	if job.State != "awaiting_approval" || nextGate.ReviewedRunID != revision.ID {
		t.Fatalf("missing new gate: %+v", job)
	}
	if e = s.WorkflowAction(t.Context(), id, nextGate.ID, "approve", false); e != nil {
		t.Fatal(e)
	}
	build, e := s.Poll(t.Context(), worker)
	if e != nil || build.Inputs["plan"].ID != second.ID {
		t.Fatalf("build used wrong version: %+v %v", build, e)
	}
	_, f, e := s.OpenArtifact(t.Context(), first.ID)
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	finishStep(t, s, build, "complete")
	if e = s.DeleteJob(t.Context(), id); e != nil {
		t.Fatalf("delete reviewed task: %v", e)
	}
}

func TestInitialApprovalCannotRequestChanges(t *testing.T) {
	s := openTestStore(t, t.TempDir()+"/db")
	id, e := s.CreateWorkflowJob(t.Context(), "request", "machinist", "single", []config.WorkflowStep{{Command: testAgent("build", "request"), Approval: true}})
	if e != nil {
		t.Fatal(e)
	}
	snap, _ := s.Snapshot(t.Context())
	run := snap.Jobs[0].Runs[0]
	if e = s.WorkflowAction(t.Context(), id, run.ID, "request_changes", false, "revise"); !errors.Is(e, ErrWorkflowAction) {
		t.Fatal(e)
	}
	if e = s.WorkflowAction(t.Context(), id, run.ID, "approve", false); e != nil {
		t.Fatal(e)
	}
}

func TestReviewFeedbackAccumulatesAndGateMigration(t *testing.T) {
	path := t.TempDir() + "/db"
	s, e := OpenStore(path)
	if e != nil {
		t.Fatal(e)
	}
	id := workflowJob(t, s, true)
	worker := workflowWorker()
	worker.Reviews = true
	run, e := s.Poll(t.Context(), worker)
	if e != nil {
		t.Fatal(e)
	}
	finishStep(t, s, run, "complete")
	// Simulate upgrading an existing v4 approval gate without review tables.
	if _, e = s.db.Exec("DROP TABLE execution_reviews; DROP TABLE review_gates; PRAGMA user_version=4;"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = OpenStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, feedback := range []string{"Keep the public API", "Add migration notes"} {
		snap, _ := s.Snapshot(t.Context())
		gate := snap.Jobs[0].Runs[len(snap.Jobs[0].Runs)-1]
		if gate.ReviewedRunID == "" {
			t.Fatal("existing approval was not migrated")
		}
		if e = s.WorkflowAction(t.Context(), id, gate.ID, "request_changes", false, feedback); e != nil {
			t.Fatal(e)
		}
		run, e = s.Poll(t.Context(), worker)
		if e != nil {
			t.Fatal(e)
		}
		if run.Revision.Feedback != feedback {
			t.Fatal(run.Revision)
		}
		if feedback == "Add migration notes" && (len(run.Revision.PriorFeedback) != 1 || run.Revision.PriorFeedback[0] != "Keep the public API") {
			t.Fatal("lost earlier feedback")
		}
		finishStep(t, s, run, "complete")
	}
}
