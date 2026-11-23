package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

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
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps"}, 1, false); err != nil {
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
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps", "claude"}, 1, true); err != nil {
		t.Fatal(err)
	}
	_, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil)
	if agents := check(setup, "Agents"); agents.OK || agents.Required || !strings.Contains(agents.Detail, "default needs agy and claude") {
		t.Fatalf("agents = %+v", agents)
	}
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps", "agy", "claude"}, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); !check(setup, "Agents").OK {
		t.Fatalf("agents = %+v", check(setup, "Agents"))
	}
	// A runner that looked and found no agents can run none of them.
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps"}, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); check(setup, "Agents").OK {
		t.Fatalf("a probed runner with no agents passed: %+v", check(setup, "Agents"))
	}
	// One that never looked, from an older Machinist, is taken to run anything.
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps"}, 1, false); err != nil {
		t.Fatal(err)
	}
	if _, setup := f.setup(t, http.MethodGet, "/api/v1/setup", nil); !check(setup, "Agents").OK {
		t.Fatalf("agents = %+v", check(setup, "Agents"))
	}
}

func TestSetupAndPresetsReportOutdatedFactoryFiles(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	if err := f.store.RecordRunner(t.Context(), "vm", "local", []string{"steps"}, 1, false); err != nil {
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
