package controlplane

import (
	"encoding/json"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestWorkflowHTTPSubmissionAndApproval(t *testing.T) {
	s, web := newTestHTTPServer(t)
	defer web.Close()
	f, err := os.OpenFile(s.definitionPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n[workflows.deliver]\nsteps=[{command='plan',approval='before'}]\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.store.Poll(t.Context(), protocol.PollRequest{InstanceID: "worker-a", Name: "host", Executors: []string{"test"}, Repositories: []string{"machinist"}, Workflows: true, Artifacts: true, SharedOutputs: true})
	if err != nil {
		t.Fatal(err)
	}
	status := getStatus(t, web.URL)
	if len(status.Workflows) != 1 || status.Workflows[0] != "deliver" {
		t.Fatalf("catalog %#v", status)
	}
	responseDefinitions, err := http.Get(web.URL + "/api/v1/definitions")
	if err != nil {
		t.Fatal(err)
	}
	var definitions definitionsResponse
	err = json.NewDecoder(responseDefinitions.Body).Decode(&definitions)
	responseDefinitions.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	stages := definitions.Workflows["deliver"]
	if len(stages) != 1 || stages[0].Name != "plan" || !stages[0].Approval {
		t.Fatalf("approval metadata missing: %+v", stages)
	}
	headers := map[string]string{"Origin": web.URL, "X-Machinist-CSRF": status.CSRFToken}
	response := postJSON(t, web.URL+"/api/v1/jobs", map[string]string{"workflow": "deliver", "repository": "machinist", "prompt": "issue"}, headers)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("submission %d", response.StatusCode)
	}
	response.Body.Close()
	status = getStatus(t, web.URL)
	job := status.Jobs[0]
	if job.Task == nil || job.Task.Spec != "issue" {
		t.Fatalf("legacy prompt was not normalized: %+v", job.Task)
	}
	if job.State != "awaiting_approval" || job.Workflow.Name != "deliver" {
		t.Fatal(job)
	}
	body := map[string]string{"run_id": job.Runs[0].ID}
	response = postJSON(t, web.URL+"/api/v1/jobs/"+job.ID+"/approve", body, nil)
	if response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unprotected approval %d", response.StatusCode)
	}
	response.Body.Close()
	response = postJSON(t, web.URL+"/api/v1/jobs/"+job.ID+"/approve", body, headers)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("approval %d", response.StatusCode)
	}
	response.Body.Close()
	response = postJSON(t, web.URL+"/api/v1/workers/poll", protocol.PollRequest{InstanceID: "worker-a", Name: "host", Executors: []string{"test"}, Repositories: []string{"machinist"}, Workflows: true, Artifacts: true, SharedOutputs: true}, map[string]string{"Authorization": "Bearer secret"})
	defer response.Body.Close()
	var polled protocol.PollResponse
	if err = json.NewDecoder(response.Body).Decode(&polled); err != nil {
		t.Fatal(err)
	}
	if polled.Run == nil || !polled.Run.Workflow {
		t.Fatalf("workflow flag lost: %#v", polled)
	}
}

func TestWorkflowEndToEndWithScriptExecutors(t *testing.T) {
	s := openTestStore(t, t.TempDir()+"/db")
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v", out, err)
	}
	script := `input=$(cat); test "$input" = 'issue URL' || exit 3; printf '%s' '{"outcome":"complete","summary":"Verified the issue"}' > "$MACHINIST_STEP_RESULT_PATH"`
	command := config.ResolvedCommand{Name: "triage", Executor: "script", Prompt: "issue URL", Timeout: time.Second, Command: []string{"sh", "-c", script}}
	build := command
	build.Name = "build"
	id, err := s.createLegacyWorkflowJob(t.Context(), "issue URL", "machinist", "deliver", []config.WorkflowStep{{Command: command}, {Command: build}})
	if err != nil {
		t.Fatal(err)
	}
	worker := workflowWorker()
	worker.Executors = []string{"script"}
	for i := 0; i < 2; i++ {
		spec, err := s.Poll(t.Context(), worker)
		if err != nil || spec == nil {
			t.Fatalf("poll %v %v", spec, err)
		}
		actual := command
		actual.Name = spec.Command
		actual.Prompt = spec.RenderedPrompt
		result, err := runner.Execute(t.Context(), runner.Options{Workflow: spec.Workflow, JobID: id, RunID: spec.ID, ArtifactKey: spec.LeaseToken, Command: actual, Repository: repo, DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Complete(t.Context(), spec.ID, protocol.Completion{InstanceID: worker.InstanceID, LeaseToken: spec.LeaseToken, State: string(result.State), ExitCode: result.ExitCode, Result: body}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.Snapshot(t.Context())
	if err != nil || snapshot.Jobs[0].State != "succeeded" || len(snapshot.Jobs[0].Runs) != 2 {
		t.Fatalf("result %#v %v", snapshot, err)
	}
}
