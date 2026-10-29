package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestMultipleProjectsRestoreAndReachWorker(t *testing.T) {
	s, o := setupTestServer(t)
	// This is the on-disk representation required for two configured projects.
	raw := []byte(`{"selection":{"repository":"owner/repo","path":"/tmp/first","agent":"codex","host":"local"},"repositories":{"owner/repo":"/tmp/first","owner/second":"/tmp/second"},"finished":true,"check":{"state":"passed","message":"PONG"}}`)
	if err := json.Unmarshal(raw, &o.settings); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	err := o.saveLocked()
	o.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s.repositories = map[string]string{}
	restored, err := newOnboarding(s)
	if err != nil {
		t.Fatal(err)
	}
	restored.baseURL = "http://127.0.0.1:7331"
	if len(s.repositories) != 2 {
		t.Fatalf("restart restored %d projects, want 2", len(s.repositories))
	}
	cfg, err := restored.workerConfig(restored.snapshot().Selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repositories) != 2 || cfg.Repositories["owner/second"].Path != "/tmp/second" {
		t.Fatalf("worker lost projects: %+v", cfg.Repositories)
	}
}

// Only the network Git probe is stubbed. Checkout validation uses actual Git
// roots, remotes and metadata, so a project cannot silently point at another repo.
func projectCheckout(t *testing.T, repo string) string {
	t.Helper()
	path := t.TempDir()
	for _, args := range [][]string{{"init", path}, {"-C", path, "remote", "add", "origin", "https://github.com/" + repo + ".git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func stubProjectGitProbe(t *testing.T) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = ls-remote ]; then exit 0; fi; done\nexec '" + strings.ReplaceAll(realGit, "'", "'\\''") + "' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type projectTestWorker struct {
	provider func(context.Context, string) (string, error)
}

func (w *projectTestWorker) SetGitHubTokenProvider(p func(context.Context, string) (string, error)) {
	w.provider = p
}
func (w *projectTestWorker) Run(ctx context.Context) error { <-ctx.Done(); return nil }
func readyProjectSetup(t *testing.T) (*GitHubFactoryServer, *onboarding) {
	t.Helper()
	s, o := setupTestServer(t, "owner/repo", "owner/second", "owner/third")
	o.settings.Selection = setupSelection{Repository: "owner/repo", Path: projectCheckout(t, "owner/repo"), Agent: "codex", Host: "local"}
	o.settings.Finished = true
	o.settings.Check = setupCheck{State: "passed", Message: "PONG"}
	s.repositories = map[string]string{"owner/repo": "owner/repo"}
	stubProjectGitProbe(t)
	return s, o
}
func TestAddProjectPreservesSetupAndRoutesBothRepositories(t *testing.T) {
	s, o := readyProjectSetup(t)
	second := projectCheckout(t, "owner/second")
	var cfg config.Worker
	var worker *projectTestWorker
	calls := 0
	s.SetLocalWorkerFactory(func(c config.Worker) (LocalWorker, error) {
		cfg = c
		worker = &projectTestWorker{}
		calls++
		return worker, nil
	})
	selection := o.snapshot().Selection
	factoryHTTP(t, s, "POST", "/api/v1/projects", map[string]string{"repository": "owner/second", "path": second}, 201)
	if o.snapshot().Selection != selection || !o.snapshot().Finished || o.snapshot().Check.State != "passed" {
		t.Fatal("adding a project reset agent setup")
	}
	if len(cfg.Repositories) != 2 || cfg.Repositories["owner/second"].Path != second {
		t.Fatal("runner missing project")
	}
	if _, err := worker.provider(context.Background(), "owner/second"); err != nil {
		t.Fatalf("second project credential rejected: %v", err)
	}
	if _, err := worker.provider(context.Background(), "owner/third"); err == nil {
		t.Fatal("credential exposed to unconfigured project")
	}
	factoryHTTP(t, s, "POST", "/api/v1/projects", map[string]string{"repository": "owner/second"}, 200)
	if calls != 1 {
		t.Fatal("duplicate add restarted runner")
	}
	for _, repo := range []string{"owner/second", "owner/repo"} {
		factoryHTTP(t, s, "POST", "/api/v1/jobs", map[string]string{"repository": repo, "recipe": "single", "spec": "PING"}, 201)
		p := protocol.PollRequest{InstanceID: "multi-worker", Name: "multi-worker", Repositories: []string{repo}, Executors: []string{"codex"}, ReadOnlyExecutors: []string{"codex-review"}, Factory: true, SingleAgent: true, Workflows: true, Reviews: true}
		var assigned protocol.PollResponse
		json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/workers/poll", p, 200), &assigned)
		if assigned.Run == nil || assigned.Run.Repository != repo || assigned.Run.GitHubRepository != repo {
			t.Fatalf("incorrect routing for %s", repo)
		}
		completion := factoryCompletion(assigned.Run, protocol.FactoryResult{Report: "PONG"})
		completion.InstanceID = p.InstanceID
		factoryHTTP(t, s, "POST", "/api/v1/runs/"+assigned.Run.ID+"/complete", completion, 204)
	}
	o.stopWorker()
	restored, err := newOnboarding(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.snapshot().projectPaths()) != 2 {
		t.Fatal("second project did not survive restart")
	}
}
func TestAddProjectRejectsWrongCheckoutAndUnavailableRepository(t *testing.T) {
	s, o := readyProjectSetup(t)
	original := o.snapshot()
	factoryHTTP(t, s, "POST", "/api/v1/projects", map[string]string{"repository": "owner/second", "path": original.Selection.Path}, 400)
	factoryHTTP(t, s, "POST", "/api/v1/projects", map[string]string{"repository": "owner/not-granted", "path": original.Selection.Path}, 400)
	if len(o.snapshot().projectPaths()) != 1 || o.snapshot().Selection != original.Selection {
		t.Fatal("failed add changed configured projects")
	}
}
func TestAddProjectDoesNotInterruptRunningWork(t *testing.T) {
	s, o := readyProjectSetup(t)
	s.repositories["repo"] = "owner/repo"
	id := submitRecipe(t, s, "single", "running-project")
	// The job uses the fixture's alias, but its lease still protects shared runner changes.
	p := protocol.PollRequest{InstanceID: "busy", Name: "busy", Repositories: []string{"repo"}, Executors: []string{"codex"}, ReadOnlyExecutors: []string{"codex-review"}, Factory: true, SingleAgent: true, Workflows: true, Reviews: true}
	var assigned protocol.PollResponse
	json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/workers/poll", p, 200), &assigned)
	if assigned.Run == nil {
		t.Fatalf("no lease for %s", id)
	}
	factoryHTTP(t, s, "PUT", "/api/v1/projects/defaults", map[string]string{"repository": "owner/repo", "recipe": "single", "instructions": "Future work only"}, 200)
	if !s.leaseAlive(assigned.Run.ID) || strings.Contains(s.tasks[id].state.Job.Task.Spec, "Future work only") {
		t.Fatal("defaults edited running job")
	}
	factoryHTTP(t, s, "POST", "/api/v1/projects", map[string]string{"repository": "owner/second", "path": "/unused"}, 409)
	if len(o.snapshot().projectPaths()) != 1 || !s.leaseAlive(assigned.Run.ID) {
		t.Fatal("add interrupted existing run")
	}
}
func TestAddProjectRollsBackWhenRunnerCannotStart(t *testing.T) {
	s, o := readyProjectSetup(t)
	second := projectCheckout(t, "owner/second")
	s.SetLocalWorkerFactory(func(c config.Worker) (LocalWorker, error) {
		if len(c.Repositories) > 1 {
			return nil, errors.New("runner rejected new configuration")
		}
		return &projectTestWorker{}, nil
	})
	factoryHTTP(t, s, "POST", "/api/v1/projects", map[string]string{"repository": "owner/second", "path": second}, 409)
	if !o.snapshot().Finished || len(o.snapshot().projectPaths()) != 1 || s.repositories["owner/second"] != "" {
		t.Fatal("failed runner update lost previous setup")
	}
	restored, err := newOnboarding(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.snapshot().projectPaths()) != 1 {
		t.Fatal("failed addition remained on disk")
	}
}
func TestVMSetupIncludesEveryProject(t *testing.T) {
	s, o := readyProjectSetup(t)
	o.settings.Selection.Host = "remote"
	o.settings.Repositories = map[string]string{"owner/repo": o.settings.Selection.Path, "owner/second": "/tmp/second"}
	raw := string(factoryHTTP(t, s, "GET", "/api/v1/onboarding/worker-config", nil, 200))
	for _, expected := range []string{"./repositories/owner/repo", "./repositories/owner/second", "gh repo clone 'owner/repo'", "gh repo clone 'owner/second'"} {
		if !strings.Contains(raw, expected) {
			t.Fatalf("VM setup missing %s", expected)
		}
	}
	if strings.Contains(raw, "private-test-token") {
		t.Fatal("GitHub credential in downloaded script")
	}
}

func TestProjectDefaultsPersistenceValidationAndSubmission(t *testing.T) {
	s, o := readyProjectSetup(t)
	save := func(recipe, instructions string, status int) {
		t.Helper()
		factoryHTTP(t, s, "PUT", "/api/v1/projects/defaults", map[string]string{"repository": "owner/repo", "recipe": recipe, "instructions": instructions}, status)
	}
	if len(o.snapshot().ProjectDefaults) != 0 {
		t.Fatal("legacy project gained defaults")
	}
	save("single", "Run focused tests.", 200)
	restored, err := newOnboarding(s)
	if err != nil {
		t.Fatal(err)
	}
	if restored.snapshot().ProjectDefaults["owner/repo"].Instructions != "Run focused tests." {
		t.Fatal("defaults not persisted")
	}
	snapshot := o.snapshot()
	snapshot.ProjectDefaults["owner/repo"] = projectDefaults{}
	if o.snapshot().ProjectDefaults["owner/repo"].Recipe != "single" {
		t.Fatal("snapshot aliases settings")
	}
	save("claude", "", 400)
	save("single", strings.Repeat("x", 2001), 400)
	o.settings.Selection.Host = "kubernetes"
	save("native-v1", "", 400)
	o.settings.Selection.Agent = "claude"
	save("single", "Run focused tests.", 200)
	o.settings.Selection.Agent = "codex"
	o.settings.Selection.Host = "local"
	submit := func(key, recipe string, instructions *string, want int) string {
		t.Helper()
		raw := factoryHTTP(t, s, "POST", "/api/v1/jobs", submitRequest{Repository: "owner/repo", Spec: "Fix bug", ClientRequestID: key, Recipe: recipe, ProjectInstructions: instructions}, want)
		var response map[string]string
		json.Unmarshal(raw, &response)
		return response["id"]
	}
	id := submit("defaults", "", nil, 201)
	if s.tasks[id].state.Job.Recipe != "single" || !strings.Contains(s.tasks[id].state.Job.Task.Spec, "Run focused tests.") {
		t.Fatal("defaults not applied")
	}
	original := s.tasks[id].state.Job.Task.Spec
	save("factory", "New instructions", 200)
	if retry := submit("defaults", "", nil, 200); retry != id {
		t.Fatal("retry created another job")
	}
	if s.tasks[id].state.Job.Task.Spec != original {
		t.Fatal("history changed")
	}
	empty := ""
	explicit := submit("explicit", "single", &empty, 201)
	if s.tasks[explicit].state.Job.Recipe != "single" || strings.Contains(s.tasks[explicit].state.Job.Task.Spec, "Project instructions") {
		t.Fatal("explicit choices lost")
	}
	inherited := submit("new", "", nil, 201)
	if s.tasks[inherited].state.Job.Recipe != "factory" || !strings.Contains(s.tasks[inherited].state.Job.Task.Spec, "New instructions") {
		t.Fatal("new defaults not applied")
	}
}

func TestProjectDefaultsAuthorizationAndAtomicFailure(t *testing.T) {
	s, o := readyProjectSetup(t)
	req := httptest.NewRequest("PUT", "/api/v1/projects/defaults", strings.NewReader(`{"repository":"owner/repo","recipe":"single"}`))
	out := httptest.NewRecorder()
	s.Handler().ServeHTTP(out, req)
	if out.Code != 403 {
		t.Fatalf("missing CSRF accepted: %d", out.Code)
	}
	factoryHTTP(t, s, "PUT", "/api/v1/projects/defaults", map[string]string{"repository": "owner/missing", "recipe": "single"}, 404)
	// A file in place of the settings directory forces the atomic write to fail.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	o.dir = blocked
	factoryHTTP(t, s, "PUT", "/api/v1/projects/defaults", map[string]string{"repository": "owner/repo", "recipe": "single"}, 500)
	if len(o.snapshot().ProjectDefaults) != 0 {
		t.Fatal("failed write changed in-memory defaults")
	}
}

func TestLinkedIssueRetainsAdmittedProjectInstructionsAfterRefreshAndRestart(t *testing.T) {
	s, o := readyProjectSetup(t)
	client := newMemoryFactoryGitHub()
	s.client = client
	issue, err := client.CreateTask(t.Context(), "owner/repo", "Linked change", "Original requirement")
	if err != nil {
		t.Fatal(err)
	}
	save := func(instructions string) {
		factoryHTTP(t, s, "PUT", "/api/v1/projects/defaults", map[string]string{"repository": "owner/repo", "recipe": "single", "instructions": instructions}, 200)
	}
	save("Original project constraint")
	var created map[string]string
	json.Unmarshal(factoryHTTP(t, s, "POST", "/api/v1/jobs", submitRequest{Repository: "owner/repo", SourceURL: issue.HTMLURL, ClientRequestID: "linked-defaults"}, 201), &created)
	save("Changed project constraint")
	issue.Body = "Updated GitHub requirement"
	client.issues[issue.Number] = issue
	worker := admissionWorker("linked-worker", "owner/repo")
	run := admissionPoll(t, s, worker)
	if run == nil || run.JobID != created["id"] {
		t.Fatal("linked job was not dispatched")
	}
	for _, text := range []string{run.RenderedPrompt, run.Task.Spec, run.Factory.Context, s.tasks[created["id"]].state.Job.Task.Spec} {
		if !strings.Contains(text, "Original project constraint") || !strings.Contains(text, "Updated GitHub requirement") || strings.Contains(text, "Changed project constraint") {
			t.Fatal("remote refresh lost or changed admitted instructions")
		}
	}
	o.stopWorker()
	s = restartFactoryTestServer(t, s, client)
	if text := factoryJob(t, s, created["id"]).Task.Spec; !strings.Contains(text, "Original project constraint") || strings.Contains(text, "Changed project constraint") {
		t.Fatal("restart lost immutable instruction snapshot")
	}
}
