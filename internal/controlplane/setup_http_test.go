package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/githubapp"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/runner"
)

type fakeApp struct {
	broken    bool
	installed map[string]bool
}

func (a fakeApp) Identity(context.Context) (githubapp.Identity, error) {
	if a.broken {
		return githubapp.Identity{}, errors.New("GitHub replied 401: bad JWT")
	}
	return githubapp.Identity{Slug: "machinistagent", Name: "machinistagent[bot]"}, nil
}

func (a fakeApp) Installed(_ context.Context, repository string) error {
	if a.installed[strings.ToLower(repository)] {
		return nil
	}
	return errors.New("the GitHub App is not installed on " + repository)
}

func (f *fixture) setup(t *testing.T, method, path string, body any) (int, Setup) {
	t.Helper()
	response, raw := f.call(t, method, path, f.token, body)
	var setup Setup
	if response.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &setup); err != nil {
			t.Fatalf("%v: %s", err, raw)
		}
	} else if !strings.Contains(string(raw), "error") {
		t.Fatalf("status = %d with no error: %s", response.StatusCode, raw)
	}
	return response.StatusCode, setup
}

func check(setup Setup, name string) SetupCheck {
	for _, check := range setup.Checks {
		if check.Name == name {
			return check
		}
	}
	return SetupCheck{}
}

func TestSetupListsWhatIsMissingUntilEveryRequiredCheckPasses(t *testing.T) {
	f := newFixture(t)
	f.server.app = fakeApp{installed: map[string]bool{"owner/repo": true}}
	_, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	if setup.Ready || !check(setup, "GitHub").OK || check(setup, "Runner").OK || check(setup, "Projects").OK || check(setup, "Linear").Required {
		t.Fatalf("setup = %+v", setup)
	}
	if setup.InstallURL != "https://github.com/apps/machinistagent/installations/new" || !strings.Contains(check(setup, "GitHub").Detail, "machinistagent[bot]") {
		t.Fatalf("setup = %+v", setup)
	}
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps"}, nil, 1, false); err != nil {
		t.Fatal(err)
	}
	if status, setup := f.setup(t, http.MethodPost, "/api/v1/projects", map[string]string{"repo": "https://github.com/owner/repo/"}); status != http.StatusOK || !setup.Ready || len(setup.Projects) != 1 || setup.Projects[0] != (ProjectView{Repo: "owner/repo", Source: "app", AddedBy: "runner-1"}) {
		t.Fatalf("status = %d, setup = %+v", status, setup)
	}
	if _, body := f.call(t, http.MethodGet, "/api/v1/repositories", f.token, nil); !strings.Contains(string(body), "owner/repo") {
		t.Fatalf("repositories = %s", body)
	}
	if status, setup := f.setup(t, http.MethodDelete, "/api/v1/projects/owner/repo", nil); status != http.StatusOK || setup.Ready || len(setup.Projects) != 0 {
		t.Fatalf("status = %d, setup = %+v", status, setup)
	}
}

func TestAddingAProjectNeedsARealNameAndTheAppInstalled(t *testing.T) {
	f := newFixture(t)
	f.server.app = fakeApp{}
	for _, repo := range []string{"not a repo", "owner/", "owner/missing"} {
		if status, _ := f.setup(t, http.MethodPost, "/api/v1/projects", map[string]string{"repo": repo}); status != http.StatusBadRequest {
			t.Fatalf("%q: status = %d", repo, status)
		}
	}
	if projects, _ := f.store.Projects(t.Context()); len(projects) != 0 {
		t.Fatalf("projects = %+v", projects)
	}
}

func TestSetupFlagsConfigReposTheAppCannotReach(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	f.server.app = fakeApp{}
	_, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	projects := check(setup, "Projects")
	if projects.OK || setup.Projects[0].Source != "config" || !strings.Contains(setup.Projects[0].Problem, "not installed") || projects.Link != setup.InstallURL {
		t.Fatalf("setup = %+v", setup)
	}
	if status, _ := f.setup(t, http.MethodDelete, "/api/v1/projects/owner/repo", nil); status != http.StatusConflict {
		t.Fatalf("removing a config repo: status = %d", status)
	}
	if status, _ := f.setup(t, http.MethodDelete, "/api/v1/projects/owner/other", nil); status != http.StatusNotFound {
		t.Fatalf("removing an unknown repo: status = %d", status)
	}
}

func TestSetupSaysWhenTheAppIsBrokenOrMissing(t *testing.T) {
	f := newFixture(t)
	f.server.app = fakeApp{broken: true}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); check(setup, "GitHub").OK || !strings.Contains(check(setup, "GitHub").Detail, "bad JWT") {
		t.Fatalf("setup = %+v", setup)
	}
	f.server.app = nil
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); !check(setup, "GitHub").OK || !strings.Contains(check(setup, "GitHub").Detail, "GH_TOKEN") {
		t.Fatalf("setup = %+v", setup)
	}
}

func TestSetupFlagsAPresetNoOnlineRunnerCanRun(t *testing.T) {
	f := newFixture(t)
	f.server.factory = &factory.Factory{
		Presets: map[string]factory.Preset{"default": {Name: "default", Steps: []runner.Step{{Name: "implement", Run: "machinist agent implement"}, {Name: "review", Run: "machinist agent review"}}}},
		Agents:  map[string]harness.Agent{"implement": {Name: "implement", Harness: "claude"}, "review": {Name: "review", Harness: "agy"}},
	}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); check(setup, "Agents").Name != "" {
		t.Fatalf("an agents check with no runner online: %+v", check(setup, "Agents"))
	}
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps", "claude"}, nil, 1, true); err != nil {
		t.Fatal(err)
	}
	_, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	if agents := check(setup, "Agents"); agents.OK || agents.Required || !strings.Contains(agents.Detail, "default needs agy and claude") {
		t.Fatalf("agents = %+v", agents)
	}
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps", "agy", "claude"}, nil, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); !check(setup, "Agents").OK {
		t.Fatalf("agents = %+v", check(setup, "Agents"))
	}
	// A runner that looked and found no agents can run none of them.
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps"}, nil, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); check(setup, "Agents").OK {
		t.Fatalf("a probed runner with no agents passed: %+v", check(setup, "Agents"))
	}
	// One that never looked, from an older Machinist, is taken to run anything.
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps"}, nil, 1, false); err != nil {
		t.Fatal(err)
	}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); !check(setup, "Agents").OK {
		t.Fatalf("agents = %+v", check(setup, "Agents"))
	}
	// A preset with runs_on fails the check until an online runner advertises its labels.
	f.server.factory.Presets["ios"] = factory.Preset{Name: "ios", RunsOn: []string{"macos"}, Steps: []runner.Step{{Name: "build", Run: "xcodebuild"}}}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); check(setup, "Agents").OK || !strings.Contains(check(setup, "Agents").Detail, "ios needs label macos") {
		t.Fatalf("agents = %+v", check(setup, "Agents"))
	}
	if err := f.store.RecordRunner(t.Context(), "mac-mini", "local", []string{"steps", "agy", "claude"}, []string{"macos"}, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); !check(setup, "Agents").OK {
		t.Fatalf("agents = %+v", check(setup, "Agents"))
	}
}

func TestSetupAndPresetsReportOutdatedFactoryFiles(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps"}, nil, 1, false); err != nil {
		t.Fatal(err)
	}
	// Up-to-date factory reports no Factory check and no updates.
	_, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	if !setup.Ready || check(setup, "Factory").Name != "" || len(setup.FactoryUpdates) != 0 {
		t.Fatalf("setup = %+v", setup)
	}
	_, presetsRaw := f.call(t, http.MethodGet, "/api/v1/presets", f.token, nil)
	if strings.Contains(string(presetsRaw), "updates") {
		t.Fatalf("unexpected updates in presets: %s", presetsRaw)
	}

	// Outdated, edited and missing files warn without marking setup unready.
	f.server.factory.Updates = []factory.FileUpdate{
		{Path: "agents/publish.md", Status: factory.FileEdited},
		{Path: "scripts/publish.sh", Status: factory.FileOutdated},
		{Path: "scripts/revise.sh", Status: factory.FileMissing},
	}
	_, setup = f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	factoryCheck := check(setup, "Factory")
	if !setup.Ready || factoryCheck.OK || factoryCheck.Required || !factoryCheck.Warning {
		t.Fatalf("setup = %+v, factoryCheck = %+v", setup, factoryCheck)
	}
	for _, want := range []string{"3 factory files are out of date", "agents/publish.md (edited)", "scripts/publish.sh", "scripts/revise.sh (missing)"} {
		if !strings.Contains(factoryCheck.Detail, want) {
			t.Fatalf("detail = %q, want %q", factoryCheck.Detail, want)
		}
	}
	if !strings.Contains(factoryCheck.Fix, "machinist factory upgrade") {
		t.Fatalf("fix = %q", factoryCheck.Fix)
	}
	if len(setup.FactoryUpdates) != 3 {
		t.Fatalf("factory_updates = %+v", setup.FactoryUpdates)
	}

	_, presetsRaw = f.call(t, http.MethodGet, "/api/v1/presets", f.token, nil)
	var presetsBody struct {
		Updates []factory.FileUpdate `json:"updates"`
	}
	if err := json.Unmarshal(presetsRaw, &presetsBody); err != nil {
		t.Fatal(err)
	}
	if len(presetsBody.Updates) != 3 || presetsBody.Updates[0].Path != "agents/publish.md" || presetsBody.Updates[1].Path != "scripts/publish.sh" || presetsBody.Updates[2].Path != "scripts/revise.sh" {
		t.Fatalf("presets updates = %+v", presetsBody.Updates)
	}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type countingApp struct {
	mu             sync.Mutex
	broken         bool
	installed      map[string]bool
	identityCalls  int
	installedCalls map[string]int
	onInstalled    func(ctx context.Context, repository string)
}

func newCountingApp(installed ...string) *countingApp {
	repos := make(map[string]bool, len(installed))
	for _, repo := range installed {
		repos[strings.ToLower(repo)] = true
	}
	return &countingApp{
		installed:      repos,
		installedCalls: map[string]int{},
	}
}

func (a *countingApp) Identity(context.Context) (githubapp.Identity, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.identityCalls++
	if a.broken {
		return githubapp.Identity{}, errors.New("GitHub replied 401: bad JWT")
	}
	return githubapp.Identity{Slug: "machinistagent", Name: "machinistagent[bot]"}, nil
}

func (a *countingApp) Installed(ctx context.Context, repository string) error {
	a.mu.Lock()
	hook := a.onInstalled
	a.mu.Unlock()
	if hook != nil {
		hook(ctx, repository)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := strings.ToLower(repository)
	a.installedCalls[key]++
	if a.installed[key] {
		return nil
	}
	return errors.New("the GitHub App is not installed on " + repository)
}

func (a *countingApp) setBroken(broken bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.broken = broken
}

func (a *countingApp) setInstalled(repository string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.installed[strings.ToLower(repository)] = ok
}

func (a *countingApp) counts() (int, map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	copied := make(map[string]int, len(a.installedCalls))
	for k, v := range a.installedCalls {
		copied[k] = v
	}
	return a.identityCalls, copied
}

func parallelBarrier(n int, timeout time.Duration) (func(ctx context.Context, _ string), func() bool) {
	var mu sync.Mutex
	arrived := 0
	allArrived := make(chan struct{})
	timedOut := false
	hook := func(ctx context.Context, _ string) {
		mu.Lock()
		arrived++
		if arrived == n {
			close(allArrived)
		}
		mu.Unlock()
		select {
		case <-allArrived:
		case <-time.After(timeout):
			mu.Lock()
			timedOut = true
			mu.Unlock()
		case <-ctx.Done():
		}
	}
	ok := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return arrived == n && !timedOut
	}
	return hook, ok
}

func TestSetupChecksInstallationsInParallel(t *testing.T) {
	f := newFixture(t)
	repos := []string{"owner/one", "owner/two", "owner/three"}
	f.server.intake.Repositories = repos
	app := newCountingApp("owner/one", "owner/three")
	hook, allConcurrent := parallelBarrier(len(repos), 2*time.Second)
	app.onInstalled = hook
	f.server.app = app

	status, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !allConcurrent() {
		t.Fatal("app.Installed was not called concurrently across projects")
	}
	if len(setup.Projects) != 3 {
		t.Fatalf("projects = %+v", setup.Projects)
	}
	if setup.Projects[0].Repo != "owner/one" || setup.Projects[0].Problem != "" {
		t.Fatalf("project 0 = %+v", setup.Projects[0])
	}
	if setup.Projects[1].Repo != "owner/two" || !strings.Contains(setup.Projects[1].Problem, "not installed") {
		t.Fatalf("project 1 = %+v", setup.Projects[1])
	}
	if setup.Projects[2].Repo != "owner/three" || setup.Projects[2].Problem != "" {
		t.Fatalf("project 2 = %+v", setup.Projects[2])
	}
}

func TestSetupCachesIdentityAndInstallationsUntilTTLAndRefresh(t *testing.T) {
	f := newFixture(t)
	clock := newTestClock()
	f.server.now = clock.Now
	f.server.intake.Repositories = []string{"owner/ok", "owner/missing"}
	app := newCountingApp("owner/ok")
	f.server.app = app

	// First request populates cache for identity and installed repos, but does not cache errors.
	_, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	if !check(setup, "GitHub").OK || setup.Projects[0].Problem != "" || setup.Projects[1].Problem == "" {
		t.Fatalf("setup = %+v", setup)
	}
	idCalls, repoCalls := app.counts()
	if idCalls != 1 || repoCalls["owner/ok"] != 1 || repoCalls["owner/missing"] != 1 {
		t.Fatalf("after first request: id=%d repos=%v", idCalls, repoCalls)
	}

	// Within TTL: cached identity and positive installation hit cache; failing repo is retried.
	clock.Advance(githubCacheTTL / 2)
	_, setup = f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	if setup.Projects[0].Problem != "" || setup.Projects[1].Problem == "" {
		t.Fatalf("within TTL setup = %+v", setup)
	}
	idCalls, repoCalls = app.counts()
	if idCalls != 1 || repoCalls["owner/ok"] != 1 || repoCalls["owner/missing"] != 2 {
		t.Fatalf("within TTL: id=%d repos=%v", idCalls, repoCalls)
	}

	// Installing the app on the previously missing repo shows up immediately without waiting for TTL.
	app.setInstalled("owner/missing", true)
	_, setup = f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	if setup.Projects[1].Problem != "" {
		t.Fatalf("expected immediate recovery: %+v", setup.Projects[1])
	}
	idCalls, repoCalls = app.counts()
	if idCalls != 1 || repoCalls["owner/ok"] != 1 || repoCalls["owner/missing"] != 3 {
		t.Fatalf("after recovery: id=%d repos=%v", idCalls, repoCalls)
	}

	// Now both repos are cached within TTL.
	_, _ = f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	idCalls, repoCalls = app.counts()
	if idCalls != 1 || repoCalls["owner/ok"] != 1 || repoCalls["owner/missing"] != 3 {
		t.Fatalf("cached second check: id=%d repos=%v", idCalls, repoCalls)
	}

	// ?refresh=1 bypasses the cache immediately.
	_, _ = f.setup(t, http.MethodGet, "/api/v1/setup?refresh=1", nil)
	idCalls, repoCalls = app.counts()
	if idCalls != 2 || repoCalls["owner/ok"] != 2 || repoCalls["owner/missing"] != 4 {
		t.Fatalf("after refresh: id=%d repos=%v", idCalls, repoCalls)
	}

	// Advancing past TTL expires cached entries.
	clock.Advance(githubCacheTTL)
	_, _ = f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	idCalls, repoCalls = app.counts()
	if idCalls != 3 || repoCalls["owner/ok"] != 3 || repoCalls["owner/missing"] != 5 {
		t.Fatalf("after TTL expiry: id=%d repos=%v", idCalls, repoCalls)
	}

	// Transient Identity errors are never cached and evict stale cached identity.
	app.setBroken(true)
	_, setup = f.setup(t, http.MethodGet, "/api/v1/setup?refresh=1", nil)
	if check(setup, "GitHub").OK {
		t.Fatalf("expected broken GitHub check: %+v", setup)
	}
	app.setBroken(false)
	_, setup = f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	if !check(setup, "GitHub").OK {
		t.Fatalf("expected immediate recovery from identity error: %+v", setup)
	}
}

func TestProjectAddAndRemoveInvalidateGitHubCache(t *testing.T) {
	f := newFixture(t)
	clock := newTestClock()
	f.server.now = clock.Now
	f.server.intake.Repositories = []string{"owner/keep"}
	app := newCountingApp("owner/keep", "owner/temp")
	f.server.app = app

	// Populate cache for owner/keep.
	_, _ = f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	_, repoCalls := app.counts()
	if repoCalls["owner/keep"] != 1 {
		t.Fatalf("repos = %v", repoCalls)
	}

	// Adding owner/temp checks it live and keeps owner/keep cached.
	if status, setup := f.setup(t, http.MethodPost, "/api/v1/projects", map[string]string{"repo": "owner/temp"}); status != http.StatusOK || len(setup.Projects) != 2 {
		t.Fatalf("add status = %d, setup = %+v", status, setup)
	}
	_, repoCalls = app.counts()
	if repoCalls["owner/keep"] != 1 || repoCalls["owner/temp"] != 1 {
		t.Fatalf("after add: repos = %v", repoCalls)
	}

	// Removing owner/temp invalidates its cached entry immediately.
	if status, setup := f.setup(t, http.MethodDelete, "/api/v1/projects/owner/temp", nil); status != http.StatusOK || len(setup.Projects) != 1 {
		t.Fatalf("remove status = %d, setup = %+v", status, setup)
	}
	_, repoCalls = app.counts()
	if repoCalls["owner/keep"] != 1 {
		t.Fatalf("after remove: repos = %v", repoCalls)
	}

	// If owner/temp is no longer installed, re-adding it within TTL must fail immediately.
	app.setInstalled("owner/temp", false)
	if status, _ := f.setup(t, http.MethodPost, "/api/v1/projects", map[string]string{"repo": "owner/temp"}); status != http.StatusBadRequest {
		t.Fatalf("re-add uninstalled repo status = %d, want 400", status)
	}
	_, repoCalls = app.counts()
	if repoCalls["owner/temp"] != 2 {
		t.Fatalf("expected live check on re-add: repos = %v", repoCalls)
	}

	// Re-installing and re-adding succeeds and caches the fresh result.
	app.setInstalled("owner/temp", true)
	if status, setup := f.setup(t, http.MethodPost, "/api/v1/projects", map[string]string{"repo": "owner/temp"}); status != http.StatusOK || len(setup.Projects) != 2 {
		t.Fatalf("re-add status = %d, setup = %+v", status, setup)
	}
	_, _ = f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	_, repoCalls = app.counts()
	if repoCalls["owner/temp"] != 3 {
		t.Fatalf("expected cached hit after re-add: repos = %v", repoCalls)
	}
}

func TestSetupAndSettingsCacheRaceFree(t *testing.T) {
	f := newFixture(t)
	clock := newTestClock()
	f.server.now = clock.Now
	f.server.intake.Repositories = []string{"owner/one", "owner/two"}
	f.server.app = newCountingApp("owner/one", "owner/two", "owner/extra")

	var wait sync.WaitGroup
	for i := range 8 {
		wait.Go(func() {
			for j := range 5 {
				switch (i + j) % 4 {
				case 0:
					f.call(t, http.MethodGet, "/api/v1/setup", f.token, nil)
				case 1:
					f.call(t, http.MethodGet, "/api/v1/setup?refresh=1", f.token, nil)
				case 2:
					f.call(t, http.MethodGet, "/api/v1/settings/github", f.token, nil)
				case 3:
					f.call(t, http.MethodPost, "/api/v1/projects", f.token, map[string]string{"repo": "owner/extra"})
					f.call(t, http.MethodDelete, "/api/v1/projects/owner/extra", f.token, nil)
				}
			}
		})
	}
	wait.Wait()
}
