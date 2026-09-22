package controlplane

import (
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestSharedSnapshotsPreserveDeletionAndRejectLegacyWorkers(t *testing.T) {
	s := openTestStore(t, t.TempDir()+"/db")
	steps := []config.WorkflowStep{}
	for _, name := range []string{"plan", "build", "verify"} {
		steps = append(steps, config.WorkflowStep{ID: name, SharedOutputs: true, Command: testAgent(name, "{{task.spec}}")})
	}
	_, err := s.CreateTaskJob(t.Context(), protocol.Task{Spec: "test"}, "machinist", "shared", steps)
	if err != nil {
		t.Fatal(err)
	}
	worker := workflowWorker()
	worker.Artifacts = true
	if r, e := s.Poll(t.Context(), worker); e != nil || r != nil {
		t.Fatalf("legacy worker dispatched: %v %v", r, e)
	}
	worker.SharedOutputs = true
	first, err := s.Poll(t.Context(), worker)
	if err != nil || first == nil {
		t.Fatalf("%v %v", first, err)
	}
	worker.SharedOutputs = false
	if _, err := s.Poll(t.Context(), worker); err == nil {
		t.Fatal("legacy worker resumed shared execution")
	}
	worker.SharedOutputs = true
	a, err := s.PublishArtifact(t.Context(), first.ID, worker.InstanceID, first.LeaseToken, "nested/plan.md", strings.NewReader("original"))
	if err != nil {
		t.Fatal(err)
	}
	complete := protocol.Completion{InstanceID: worker.InstanceID, LeaseToken: first.LeaseToken, State: "succeeded", Result: []byte(`{"step_result":{"outcome":"complete","summary":"ready"}}`), Artifacts: []string{a.ID}}
	if err = s.Complete(t.Context(), first.ID, complete); err != nil {
		t.Fatal(err)
	}
	second, err := s.Poll(t.Context(), worker)
	if err != nil || second == nil {
		t.Fatal(err)
	}
	if second.Inputs["__workspace__/nested/plan.md"].ID != a.ID {
		t.Fatalf("wrong snapshot: %+v", second.Inputs)
	}
	// An empty complete snapshot deletes the previous file from the shared folder.
	complete.LeaseToken = second.LeaseToken
	complete.Artifacts = nil
	if err = s.Complete(t.Context(), second.ID, complete); err != nil {
		t.Fatal(err)
	}
	third, err := s.Poll(t.Context(), worker)
	if err != nil || third == nil {
		t.Fatal(err)
	}
	if len(third.Inputs) != 0 {
		t.Fatalf("deleted file resurrected: %+v", third.Inputs)
	}
	_, f, err := s.OpenArtifact(t.Context(), a.ID)
	if err != nil {
		t.Fatal("historical file lost:", err)
	}
	f.Close()
}

func TestIncompleteArtifactManifestBlocksProgression(t *testing.T) {
	s := openTestStore(t, t.TempDir()+"/db")
	_, r := artifactTask(t, s, []config.WorkflowStep{{ID: "plan", Command: testAgent("plan", "{{task.spec}}")}, {ID: "build", Command: testAgent("build", "{{task.spec}}")}})
	if _, err := s.PublishArtifact(t.Context(), r.ID, "worker-a", r.LeaseToken, "hidden.txt", strings.NewReader("not in manifest")); err != nil {
		t.Fatal(err)
	}
	finishStep(t, s, r, "complete")
	snap, err := s.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Jobs[0].State != "blocked" || len(snap.Jobs[0].Runs) != 1 {
		t.Fatalf("incomplete snapshot advanced: %+v", snap.Jobs[0])
	}
}
