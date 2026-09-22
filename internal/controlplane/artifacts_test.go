package controlplane

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func artifactTask(t *testing.T, s *Store, steps []config.WorkflowStep) (string, *protocol.RunSpec) {
	t.Helper()
	id, err := s.CreateTaskJob(t.Context(), protocol.Task{Title: "Build it", SourceURL: "https://github.com/acme/repo/issues/1", Spec: "literal {{stage.output_dir}}"}, "machinist", "deliver", steps)
	if err != nil {
		t.Fatal(err)
	}
	old := workflowWorker()
	if r, e := s.Poll(t.Context(), old); e != nil || r != nil {
		t.Fatalf("old worker admitted: %v %v", r, e)
	}
	old.Artifacts = true
	old.SharedOutputs = true
	r, e := s.Poll(t.Context(), old)
	if e != nil || r == nil {
		t.Fatalf("poll %v %v", r, e)
	}
	return id, r
}
func TestArtifactFilesPersistUntilTaskDeletion(t *testing.T) {
	s := openTestStore(t, t.TempDir()+"/db")
	steps := []config.WorkflowStep{{ID: "plan", Command: testAgent("plan", "{{task.spec}}"), RequiredOutputs: []string{"spec.md"}}, {ID: "build", Command: testAgent("build", "{{inputs.spec}}"), Approval: true, Inputs: map[string]string{"spec": "plan/spec.md"}}}
	id, r := artifactTask(t, s, steps)
	publish := func(token, content string) (protocol.Artifact, error) {
		return s.PublishArtifact(t.Context(), r.ID, "worker-a", token, "spec.md", strings.NewReader(content))
	}
	if _, err := publish("wrong", "hello"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatal(err)
	}
	a, err := publish(r.LeaseToken, "hello")
	if err != nil {
		t.Fatal(err)
	}
	again, err := publish(r.LeaseToken, "hello")
	if err != nil || again.ID != a.ID {
		t.Fatalf("idempotency %v %v", again, err)
	}
	if _, err = publish(r.LeaseToken, "different"); !errors.Is(err, ErrArtifactConflict) {
		t.Fatal(err)
	}
	complete := protocol.Completion{InstanceID: "worker-a", LeaseToken: r.LeaseToken, State: "succeeded", Result: []byte(`{"step_result":{"outcome":"complete","summary":"ready"}}`), Artifacts: []string{a.ID}}
	if err = s.Complete(t.Context(), r.ID, complete); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := s.Snapshot(t.Context())
	job := snapshot.Jobs[0]
	if job.State != "awaiting_approval" || job.Task.Title != "Build it" {
		t.Fatalf("%+v", job)
	}
	// Time alone never removes files, including while awaiting approval.
	now := s.now()
	s.now = func() time.Time { return now.Add(60 * 24 * time.Hour) }
	if err = s.CleanupArtifacts(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, f, err := s.OpenArtifact(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = s.WorkflowAction(t.Context(), id, job.Runs[1].ID, "approve", false); err != nil {
		t.Fatal(err)
	}
	worker := workflowWorker()
	worker.Artifacts = true
	worker.SharedOutputs = true
	next, err := s.Poll(t.Context(), worker)
	if err != nil || next == nil {
		t.Fatalf("%v %v", next, err)
	}
	if next.Inputs["spec"].ID != a.ID || next.Task.Spec != "literal {{stage.output_dir}}" {
		t.Fatal(next)
	}
	finishStep(t, s, next, "complete")
	terminal := s.now()
	s.now = func() time.Time { return terminal.Add(31 * 24 * time.Hour) }
	if err = s.CleanupArtifacts(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, f, err = s.OpenArtifact(t.Context(), a.ID)
	if err != nil {
		t.Fatal("completed task lost its files:", err)
	}
	f.Close()
	if err = s.DeleteJob(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err = s.CleanupArtifacts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.OpenArtifact(t.Context(), a.ID); !errors.Is(err, ErrArtifactExpired) {
		t.Fatal(err)
	}
	if file, err := s.artifactStore.Open(a.JobID + "/" + a.RunID + "/" + a.ID); !errors.Is(err, os.ErrNotExist) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("deleted task still has stored bytes: %v", err)
	}
	list, err := s.ListArtifacts(t.Context(), id)
	if err != nil || len(list) != 1 || list[0].ExpiredAt == nil {
		t.Fatalf("%v %v", list, err)
	}
}
func TestMissingArtifactBlocksAdvancement(t *testing.T) {
	for _, publicationError := range []string{"", "disk unavailable"} {
		t.Run(publicationError, func(t *testing.T) {
			s := openTestStore(t, t.TempDir()+"/db")
			_, r := artifactTask(t, s, []config.WorkflowStep{{ID: "plan", Command: testAgent("plan", "{{task.spec}}"), RequiredOutputs: []string{"spec.md"}}, {ID: "build", Command: testAgent("build", "{{task.spec}}")}})
			err := s.Complete(t.Context(), r.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: r.LeaseToken, State: "succeeded", Result: []byte(`{"step_result":{"outcome":"complete","summary":"ready"}}`), PublicationError: publicationError})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _ := s.Snapshot(t.Context())
			if snapshot.Jobs[0].State != "blocked" || len(snapshot.Jobs[0].Runs) != 1 {
				t.Fatal(snapshot)
			}
		})
	}
}
func TestArtifactHTTPBinaryRangeAndAuth(t *testing.T) {
	s, web := newTestHTTPServer(t)
	defer web.Close()
	_, r := artifactTask(t, s.store, []config.WorkflowStep{{ID: "plan", Command: testAgent("plan", "{{task.spec}}")}})
	data := []byte{'R', 'I', 'F', 'F', 0, 255, 0, 1}
	a, err := s.store.PublishArtifact(t.Context(), r.ID, "worker-a", r.LeaseToken, "recording.wav", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := web.URL + "/api/v1/artifacts/" + a.ID + "/content"
	for _, headers := range []map[string]string{nil, {"X-Machinist-CSRF": s.csrfToken, "Sec-Fetch-Site": "cross-site"}, {"Authorization": "Bearer bad"}} {
		req, _ := http.NewRequest("GET", endpoint, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 && resp.StatusCode != 403 {
			t.Fatal(resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Range", "bytes=4-7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 206 || !bytes.Equal(b, data[4:]) || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("%d %v", resp.StatusCode, b)
	}
}
