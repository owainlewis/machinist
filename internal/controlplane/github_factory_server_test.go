package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

type memoryFactoryGitHub struct {
	issues        map[int]GitHubIssue
	comments      map[int][]GitHubComment
	next          int64
	creates       int
	adopts        int
	offline       bool
	uncertainPost bool
}

func newMemoryFactoryGitHub() *memoryFactoryGitHub {
	return &memoryFactoryGitHub{issues: map[int]GitHubIssue{}, comments: map[int][]GitHubComment{}, next: 1}
}
func (f *memoryFactoryGitHub) ListTasks(context.Context, string) ([]GitHubIssue, error) {
	if f.offline {
		return nil, errors.New("offline")
	}
	var out []GitHubIssue
	for _, i := range f.issues {
		out = append(out, i)
	}
	return out, nil
}
func (f *memoryFactoryGitHub) CreateTask(_ context.Context, repo, title, body string) (GitHubIssue, error) {
	f.creates++
	n := len(f.issues) + 1
	i := GitHubIssue{Number: n, Title: title, Body: body, HTMLURL: fmt.Sprintf("https://github.com/%s/issues/%d", repo, n), State: "open", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.issues[n] = i
	return i, nil
}
func (f *memoryFactoryGitHub) GetTask(_ context.Context, _ string, n int) (GitHubIssue, error) {
	return f.issues[n], nil
}
func (f *memoryFactoryGitHub) AdoptTask(context.Context, string, int) error { f.adopts++; return nil }
func (f *memoryFactoryGitHub) Comments(_ context.Context, _ string, n int) ([]GitHubComment, error) {
	if f.offline {
		return nil, errors.New("offline")
	}
	return append([]GitHubComment{}, f.comments[n]...), nil
}
func (f *memoryFactoryGitHub) PostComment(_ context.Context, _ string, n int, body string) (GitHubComment, error) {
	c := GitHubComment{ID: f.next, Body: body, User: GitHubActor{Login: "owner"}, CreatedAt: time.Now()}
	f.next++
	f.comments[n] = append(f.comments[n], c)
	if f.uncertainPost {
		f.uncertainPost = false
		return GitHubComment{}, errors.New("publication response lost")
	}
	return c, nil
}
func (f *memoryFactoryGitHub) AgentPrompt(context.Context, string, string) (string, string, error) {
	return "Task {{machinist.prompt}}", "prompt-sha", nil
}
func (f *memoryFactoryGitHub) SaveAgentPrompt(context.Context, string, string, string, string) (string, error) {
	return "new-sha", nil
}
func (f *memoryFactoryGitHub) CurrentUser(context.Context) (string, error) {
	if f.offline {
		return "", errors.New("offline")
	}
	return "owner", nil
}

func githubFactoryTestServer(t *testing.T, client GitHubFactoryClient) *GitHubFactoryServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), "factory.toml")
	var config strings.Builder
	for _, role := range []string{"triage", "spec", "implement", "review"} {
		fmt.Fprintf(&config, "[commands.%s]\nexecutor = 'codex'\ntimeout = '1m'\n", role)
	}
	if err := os.WriteFile(path, []byte(config.String()), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := newGitHubFactoryServer(path, "secret", 1, map[string]string{"repo": "owner/repo"}, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}
func restartFactoryTestServer(t *testing.T, old *GitHubFactoryServer, client GitHubFactoryClient) *GitHubFactoryServer {
	t.Helper()
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	server, err := newGitHubFactoryServer(old.definitionPath, "secret", 1, old.repositories, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}
func factoryHTTP(t *testing.T, s *GitHubFactoryServer, method, path string, input any, want int) []byte {
	t.Helper()
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	if out.Code != want {
		t.Fatalf("%s %s: status %d want %d body %s", method, path, out.Code, want, out.Body.String())
	}
	return out.Body.Bytes()
}
func factorySubmit(t *testing.T, s *GitHubFactoryServer) string {
	t.Helper()
	raw := factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": "repo", "workflow": "factory", "title": "Task", "spec": "Implement useful behavior"}, 201)
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result["id"]
}
func factoryPoll(t *testing.T, s *GitHubFactoryServer) *protocol.RunSpec {
	t.Helper()
	p := protocol.PollRequest{SingleAgent: true, InstanceID: "worker-instance", Name: "worker", Executors: []string{"codex"}, ReadOnlyExecutors: []string{"codex"}, Repositories: []string{"repo"}, Factory: true, Workflows: true, Reviews: true}
	raw := factoryHTTP(t, s, "POST", "/api/v1/workers/poll", p, 200)
	var result protocol.PollResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result.Run
}
func factoryCompletion(run *protocol.RunSpec, f protocol.FactoryResult) protocol.Completion {
	f.Version = 1
	f.Role = run.Factory.Role
	if f.Report == "" {
		f.Report = "Evidence and handoff"
	}
	step := protocol.StepResult{Outcome: "complete", Summary: "Done", Factory: &f}
	result, _ := json.Marshal(map[string]any{"step_result": step})
	return protocol.Completion{InstanceID: "worker-instance", LeaseToken: run.LeaseToken, State: "succeeded", Result: result}
}
func factoryFinish(t *testing.T, s *GitHubFactoryServer, run *protocol.RunSpec, f protocol.FactoryResult) {
	t.Helper()
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", factoryCompletion(run, f), 204)
}
func factoryJob(t *testing.T, s *GitHubFactoryServer, id string) Job {
	t.Helper()
	raw := factoryHTTP(t, s, "GET", "/api/v1/status", nil, 200)
	var result struct{ Jobs []Job }
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	for _, job := range result.Jobs {
		if job.ID == id {
			return job
		}
	}
	t.Fatal("missing task")
	return Job{}
}

func TestGitHubFactoryHTTPFourRolesAndReconstruction(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := factorySubmit(t, s)
	triage := factoryPoll(t, s)
	if triage == nil || triage.Factory.Role != "triage" {
		t.Fatal("triage not assigned")
	}
	factoryFinish(t, s, triage, protocol.FactoryResult{NeedsSpec: true})
	spec := factoryPoll(t, s)
	if spec == nil || spec.Factory.Role != "spec" {
		t.Fatal("spec not assigned")
	}
	metadata := protocol.FactoryResult{SpecURL: "https://github.com/owner/repo/blob/" + strings.Repeat("a", 40) + "/.machinist/specs/task-1.md", PRURL: "https://github.com/owner/repo/pull/2", HeadSHA: strings.Repeat("a", 40), Branch: "codex/issue-1"}
	factoryFinish(t, s, spec, metadata)
	job := factoryJob(t, s, id)
	if job.State != "awaiting_approval" {
		t.Fatalf("state %s", job.State)
	}
	if run := factoryPoll(t, s); run != nil {
		t.Fatal("implementation started before approval")
	}
	gate := job.Runs[len(job.Runs)-1]
	factoryHTTP(t, s, "POST", "/api/v1/jobs/"+id+"/approve", map[string]string{"run_id": gate.ID}, 200)
	implementation := factoryPoll(t, s)
	if implementation == nil || implementation.Factory.Role != "implement" || implementation.Factory.PRURL != metadata.PRURL {
		t.Fatal("implementation lost approved PR")
	}
	metadata.HeadSHA = strings.Repeat("b", 40)
	factoryFinish(t, s, implementation, metadata)
	review := factoryPoll(t, s)
	if review == nil || review.Factory.Role != "review" || review.Factory.HeadSHA != metadata.HeadSHA {
		t.Fatal("review not bound to implementation")
	}
	metadata.Verdict = "accepted"
	factoryFinish(t, s, review, metadata)
	if job = factoryJob(t, s, id); job.State != "awaiting_merge" {
		t.Fatalf("state %s", job.State)
	}
	fresh := restartFactoryTestServer(t, s, client)
	if job = factoryJob(t, fresh, id); job.State != "awaiting_merge" || job.Workflow.Factory.HeadSHA != metadata.HeadSHA {
		t.Fatalf("reconstructed %#v", job)
	}
	if run := factoryPoll(t, fresh); run != nil {
		t.Fatal("completed pipeline replayed")
	}
}

func TestGitHubFactoryRestartInterruptsWithoutReplay(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := factorySubmit(t, s)
	if factoryPoll(t, s) == nil {
		t.Fatal("missing assignment")
	}
	fresh := restartFactoryTestServer(t, s, client)
	if job := factoryJob(t, fresh, id); job.State != "interrupted" {
		t.Fatalf("state %s", job.State)
	}
	if run := factoryPoll(t, fresh); run != nil {
		t.Fatal("interrupted task replayed")
	}
}
func TestLocalFactoryCompletionReceiptRetry(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := factorySubmit(t, s)
	run := factoryPoll(t, s)
	completion := factoryCompletion(run, protocol.FactoryResult{Resolved: true, Report: "PONG"})
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
	fresh := restartFactoryTestServer(t, s, client)
	factoryHTTP(t, fresh, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
	if job := factoryJob(t, fresh, id); len(job.Runs) != 1 || job.State != "succeeded" {
		t.Fatalf("job %#v", job)
	}
	completion.LeaseToken = "wrong"
	factoryHTTP(t, fresh, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 409)
	if client.creates != 0 || client.adopts != 0 || len(client.comments) != 0 {
		t.Fatal("local task mutated GitHub")
	}
}

func TestGitHubFactoryAdoptsTicketAndRejectsSourceMismatch(t *testing.T) {
	client := newMemoryFactoryGitHub()
	client.issues[42] = GitHubIssue{Number: 42, Title: "Existing ticket", Body: "Ticket requirements", HTMLURL: "https://github.com/owner/repo/issues/42", State: "open", CreatedAt: time.Now()}
	s := githubFactoryTestServer(t, client)
	input := map[string]string{"repository": "repo", "workflow": "factory", "source_url": "https://github.com/other/repo/issues/42"}
	factoryHTTP(t, s, "POST", "/api/v1/jobs", input, 400)
	if client.adopts != 0 || client.creates != 0 {
		t.Fatal("mismatched source mutated GitHub")
	}
	input["source_url"] = "https://github.com/owner/repo/issues/42"
	factoryHTTP(t, s, "POST", "/api/v1/jobs", input, 201)
	factoryHTTP(t, s, "POST", "/api/v1/jobs", input, 201)
	if client.creates != 0 || client.adopts != 0 || len(client.comments[42]) != 0 {
		t.Fatal("adoption duplicated task")
	}
}
func TestLocalFactoryWorksWithoutGitHub(t *testing.T) {
	client := newMemoryFactoryGitHub()
	client.offline = true
	s := githubFactoryTestServer(t, client)
	id := factorySubmit(t, s)
	run := factoryPoll(t, s)
	if run == nil || run.Factory.IssueURL != "" || run.Task.SourceURL != "" {
		t.Fatalf("run %#v", run)
	}
	factoryFinish(t, s, run, protocol.FactoryResult{Resolved: true, Report: "PONG"})
	if factoryJob(t, s, id).State != "succeeded" {
		t.Fatal("offline task failed")
	}
	if client.creates != 0 || client.adopts != 0 || len(client.comments) != 0 {
		t.Fatal("local task wrote to GitHub")
	}
}

func TestGitHubFactoryAnswersWithoutStartingImplementation(t *testing.T) {
	client := newMemoryFactoryGitHub()
	s := githubFactoryTestServer(t, client)
	id := factorySubmit(t, s)
	run := factoryPoll(t, s)
	completion := factoryCompletion(run, protocol.FactoryResult{Resolved: true, Report: "PONG"})
	var result map[string]protocol.StepResult
	if err := json.Unmarshal(completion.Result, &result); err != nil {
		t.Fatal(err)
	}
	step := result["step_result"]
	step.Summary = "PONG"
	completion.Result, _ = json.Marshal(map[string]protocol.StepResult{"step_result": step})
	factoryHTTP(t, s, "POST", "/api/v1/runs/"+run.ID+"/complete", completion, 204)
	job := factoryJob(t, s, id)
	if job.State != "succeeded" || len(job.Runs) != 1 || job.Runs[0].Summary != "PONG" {
		t.Fatalf("job = %#v", job)
	}
	if next := factoryPoll(t, s); next != nil {
		t.Fatal("informational request started implementation")
	}
	fresh := restartFactoryTestServer(t, s, client)
	if factoryJob(t, fresh, id).State != "succeeded" {
		t.Fatal("answer lost on restart")
	}
}

func TestLinkedIssueRefreshesRequirementsAndClosedStateBeforeDispatch(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprint(closed), func(t *testing.T) {
			client := newMemoryFactoryGitHub()
			client.issues[9] = GitHubIssue{Number: 9, HTMLURL: "https://github.com/owner/repo/issues/9", Title: "Original", Body: "Old requirements", State: "open"}
			s := githubFactoryTestServer(t, client)
			factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"workflow": "factory", "repository": "repo", "source_url": "https://github.com/owner/repo/issues/9"}, 201)
			issue := client.issues[9]
			issue.Body = "Updated requirements"
			issue.Title = "Updated task"
			if closed {
				issue.State = "closed"
			}
			client.issues[9] = issue
			run := factoryPoll(t, s)
			if closed {
				if run != nil {
					t.Fatal("closed issue dispatched")
				}
				for _, task := range s.tasks {
					if task.state.Job.State != "blocked" {
						t.Fatal("closed issue stayed queued")
					}
				}
				return
			}
			if run == nil || run.Task.Spec != "Updated requirements" || run.Task.Title != "Updated task" {
				t.Fatalf("stale task: %#v", run)
			}
		})
	}
}
func TestLocalTasksHaveDistinctBranches(t *testing.T) {
	s := githubFactoryTestServer(t, newMemoryFactoryGitHub())
	factorySubmit(t, s)
	first := factoryPoll(t, s)
	factoryFinish(t, s, first, protocol.FactoryResult{Resolved: true})
	factorySubmit(t, s)
	second := factoryPoll(t, s)
	if first.Factory.Branch == second.Factory.Branch || strings.Contains(first.Factory.Branch, "issue-0") {
		t.Fatal("local requests share a task branch")
	}
}
