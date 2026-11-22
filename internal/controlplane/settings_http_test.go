package controlplane

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

func decodeAs[T any](t *testing.T, status int, want int, body []byte) T {
	t.Helper()
	if status != want {
		t.Fatalf("status = %d, want %d: %s", status, want, body)
	}
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return value
}

func (f *fixture) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	response, body := f.call(t, http.MethodGet, path, f.token, nil)
	return response.StatusCode, body
}

func TestGitHubSettingsWarnAboutUnprotectedBranchesAndAdminRights(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	status, body := f.get(t, "/api/v1/settings/github")
	settings := decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if settings.Handle != "machinist-bot" || settings.App != nil || !settings.AutoMerge || len(settings.Repositories) != 1 {
		t.Fatalf("settings = %+v", settings)
	}
	if repo := settings.Repositories[0]; repo.Repo != "owner/repo" || repo.DefaultBranch != "main" || !repo.Protected || len(repo.Warnings) != 0 {
		t.Fatalf("repository = %+v", repo)
	}

	f.github.SetAccess(false, true)
	status, body = f.get(t, "/api/v1/settings/github")
	settings = decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	warnings := settings.Repositories[0].Warnings
	if len(warnings) != 2 || !strings.Contains(warnings[0], "auto-merge is on") || !strings.Contains(warnings[1], "admin") {
		t.Fatalf("warnings = %q", warnings)
	}

	off := false
	f.server.config.GitHub.AutoMerge = &off
	status, body = f.get(t, "/api/v1/settings/github")
	settings = decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if settings.AutoMerge || strings.Contains(settings.Repositories[0].Warnings[0], "auto-merge") || !strings.Contains(settings.Repositories[0].Warnings[0], "no branch protection") {
		t.Fatalf("settings = %+v", settings)
	}

	f.github.Fail("gh: Bad credentials (HTTP 401)")
	status, body = f.get(t, "/api/v1/settings/github")
	settings = decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if !strings.Contains(settings.Repositories[0].Problem, "could not read") {
		t.Fatalf("repository = %+v", settings.Repositories[0])
	}
}

func TestGitHubSettingsShowTheAppButNotItsKey(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	f.server.config.GitHub.App = &config.GitHubApp{ID: 42, PrivateKeyFile: "/secret/app-key.pem"}
	f.server.app = fakeApp{}
	status, body := f.get(t, "/api/v1/settings/github")
	settings := decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if settings.App == nil || settings.App.ID != 42 || settings.App.Name != "machinistagent[bot]" || settings.App.InstallURL == "" {
		t.Fatalf("app = %+v", settings.App)
	}
	// The app is not installed on owner/repo, so GitHub is not asked about it.
	if repo := settings.Repositories[0]; !strings.Contains(repo.Problem, "not installed") || repo.DefaultBranch != "" {
		t.Fatalf("repository = %+v", repo)
	}
	if strings.Contains(string(body), "app-key") || strings.Contains(string(body), "secret") {
		t.Fatalf("the key file leaked: %s", body)
	}
	f.server.app = fakeApp{broken: true}
	status, body = f.get(t, "/api/v1/settings/github")
	if settings := decodeAs[GitHubSettings](t, status, http.StatusOK, body); !strings.Contains(settings.App.Error, "bad JWT") {
		t.Fatalf("app = %+v", settings.App)
	}
}

func TestLinearSettingsSayHowToTurnItOnAndNeverShowTheToken(t *testing.T) {
	f := newFixture(t)
	status, body := f.get(t, "/api/v1/settings/linear")
	off := decodeAs[LinearSettings](t, status, http.StatusOK, body)
	if off.Enabled || off.TokenEnv != "LINEAR_API_TOKEN" || !strings.HasSuffix(off.Docs, "linear.md") || off.Teams == nil || off.AllowedUsers == nil {
		t.Fatalf("settings = %+v", off)
	}

	f.server.config.Linear = config.Linear{
		AppUserID:      "app-user",
		AllowedUsers:   []string{"alice-id", "bob-id"},
		Teams:          map[string]string{"WEB": "owner/web", "ENG": "owner/repo"},
		Poll:           90 * time.Second,
		MaxJobsPerHour: 20,
		Token:          "lin_api_secret_value",
	}
	f.server.linear = &sources.Poller{}
	status, body = f.get(t, "/api/v1/settings/linear")
	on := decodeAs[LinearSettings](t, status, http.StatusOK, body)
	want := []LinearTeam{{Team: "ENG", Repo: "owner/repo"}, {Team: "WEB", Repo: "owner/web"}}
	if !on.Enabled || on.AppUserID != "app-user" || !slices.Equal(on.AllowedUsers, []string{"alice-id", "bob-id"}) || !slices.Equal(on.Teams, want) || on.PollSeconds != 90 || on.Preset != "default" || on.MaxJobsPerHour != 20 {
		t.Fatalf("settings = %+v", on)
	}
	if strings.Contains(string(body), "lin_api_secret_value") {
		t.Fatalf("the Linear token leaked: %s", body)
	}
}

func TestTokensListWithoutSecretsAndRevokeFromTheWebApp(t *testing.T) {
	f := newFixture(t)
	runnerToken, err := f.store.CreateToken(t.Context(), "vm-runner", tickets.RoleRunner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateToken(t.Context(), "alice", tickets.RoleOperator); err != nil {
		t.Fatal(err)
	}
	type list struct {
		Tokens []TokenView `json:"tokens"`
	}
	status, body := f.get(t, "/api/v1/tokens")
	found := decodeAs[list](t, status, http.StatusOK, body)
	if len(found.Tokens) != 3 {
		t.Fatalf("tokens = %+v", found.Tokens)
	}
	me, vm, alice := found.Tokens[0], found.Tokens[1], found.Tokens[2]
	if me.Name != "runner-1" || me.Role != "all" || !me.Current || me.LastUsedAt == nil || vm.Role != "runner" || vm.Current || vm.LastUsedAt != nil || alice.Role != "operator" || me.CreatedAt.IsZero() {
		t.Fatalf("tokens = %+v", found.Tokens)
	}
	if strings.Contains(string(body), f.token) || strings.Contains(string(body), runnerToken) || strings.Contains(string(body), "hash") {
		t.Fatalf("a token leaked: %s", body)
	}

	// A runner's token cannot list or revoke tokens.
	if response, _ := f.call(t, http.MethodGet, "/api/v1/tokens", runnerToken, nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("a runner listed tokens: %d", response.StatusCode)
	}
	if response, _ := f.call(t, http.MethodDelete, "/api/v1/tokens/alice", runnerToken, nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("a runner revoked a token: %d", response.StatusCode)
	}

	response, body := f.call(t, http.MethodDelete, "/api/v1/tokens/vm-runner", f.token, nil)
	found = decodeAs[list](t, response.StatusCode, http.StatusOK, body)
	if found.Tokens[1].RevokedAt == nil {
		t.Fatalf("tokens = %+v", found.Tokens)
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/runners", runnerToken, nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoked token still works: %d", response.StatusCode)
	}
	if response, _ := f.call(t, http.MethodDelete, "/api/v1/tokens/vm-runner", f.token, nil); response.StatusCode != http.StatusNotFound {
		t.Fatalf("revoking twice: %d", response.StatusCode)
	}

	// Revoking your own token ends your sign-in.
	response, _ = f.call(t, http.MethodDelete, "/api/v1/tokens/runner-1", f.token, nil)
	if response.StatusCode != http.StatusNoContent || !slices.ContainsFunc(response.Cookies(), func(cookie *http.Cookie) bool { return cookie.Name == sessionCookie && cookie.MaxAge < 0 }) {
		t.Fatalf("status = %d, cookies = %v", response.StatusCode, response.Cookies())
	}
	if response, _ := f.call(t, http.MethodGet, "/api/v1/tokens", f.token, nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoked token still works: %d", response.StatusCode)
	}
}

func TestAgentsListTheirHarnessModelAndPresets(t *testing.T) {
	f := newFixture(t)
	loaded := &factory.Factory{
		Presets: map[string]factory.Preset{
			"default": {Name: "default", Steps: []runner.Step{{Name: "implement", Run: "machinist agent implement"}, {Name: "review", Run: "machinist agent review"}}},
			"quick":   {Name: "quick", Steps: []runner.Step{{Name: "implement", Run: "machinist agent implement"}}},
		},
		Agents: map[string]harness.Agent{
			"implement": {Name: "implement", Harness: "claude", Model: "opus", Prompt: "secret instructions"},
			"review":    {Name: "review", Harness: "codex"},
			"unused":    {Name: "unused", Harness: "pi"},
		},
	}
	f.server.factory, f.server.presets = loaded, presetViews(loaded)
	type list struct {
		Agents []AgentView `json:"agents"`
	}
	status, body := f.get(t, "/api/v1/agents")
	found := decodeAs[list](t, status, http.StatusOK, body)
	want := []AgentView{
		{Name: "implement", Harness: "claude", Model: "opus", Presets: []string{"default", "quick"}},
		{Name: "review", Harness: "codex", Presets: []string{"default"}},
		{Name: "unused", Harness: "pi", Presets: []string{}},
	}
	if len(found.Agents) != len(want) {
		t.Fatalf("agents = %+v", found.Agents)
	}
	for index := range want {
		if got := found.Agents[index]; got.Name != want[index].Name || got.Harness != want[index].Harness || got.Model != want[index].Model || !slices.Equal(got.Presets, want[index].Presets) {
			t.Fatalf("agent %d = %+v, want %+v", index, got, want[index])
		}
	}
	if strings.Contains(string(body), "secret instructions") {
		t.Fatalf("a prompt leaked: %s", body)
	}
}

func TestSettingsRoutesNeedASignIn(t *testing.T) {
	f := newFixture(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/settings/github"},
		{http.MethodGet, "/api/v1/settings/linear"},
		{http.MethodGet, "/api/v1/tokens"},
		{http.MethodDelete, "/api/v1/tokens/runner-1"},
		{http.MethodGet, "/api/v1/agents"},
	} {
		if response, _ := f.call(t, route.method, route.path, "", nil); response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s: status = %d", route.method, route.path, response.StatusCode)
		}
	}
}
