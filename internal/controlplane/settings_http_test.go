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
	status, body = f.get(t, "/api/v1/settings/github?refresh=1")
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
	status, body = f.get(t, "/api/v1/settings/github?refresh=1")
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
	status, body = f.get(t, "/api/v1/settings/github?refresh=1")
	if settings := decodeAs[GitHubSettings](t, status, http.StatusOK, body); !strings.Contains(settings.App.Error, "bad JWT") {
		t.Fatalf("app = %+v", settings.App)
	}
}

func TestGitHubSettingsChecksInstallationsInParallel(t *testing.T) {
	f := newFixture(t)
	repos := []string{"owner/one", "owner/two", "owner/three"}
	f.server.intake.Repositories = repos
	app := newCountingApp(repos...)
	hook, allConcurrent := parallelBarrier(len(repos), 2*time.Second)
	app.onInstalled = hook
	f.server.app = app

	status, body := f.get(t, "/api/v1/settings/github")
	settings := decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if !allConcurrent() {
		t.Fatal("app.Installed was not called concurrently across projects")
	}
	if len(settings.Repositories) != 3 {
		t.Fatalf("repositories = %+v", settings.Repositories)
	}
	for index, want := range repos {
		if got := settings.Repositories[index]; got.Repo != want || got.DefaultBranch != "main" || !got.Protected || got.Problem != "" {
			t.Fatalf("repo %d = %+v, want %s", index, got, want)
		}
	}
}

func TestGitHubSettingsCachesLookupsUntilTTLAndInvalidatesOnProjectChanges(t *testing.T) {
	f := newFixture(t)
	clock := newTestClock()
	f.server.now = clock.Now
	app := newCountingApp("owner/repo")
	f.server.app = app

	if status, _ := f.setup(t, http.MethodPost, "/api/v1/projects", map[string]string{"repo": "owner/repo"}); status != http.StatusOK {
		t.Fatalf("add project status = %d", status)
	}

	// First visit populates identity, installed, and repository access caches.
	status, body := f.get(t, "/api/v1/settings/github")
	settings := decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if len(settings.Repositories) != 1 || !settings.Repositories[0].Protected || settings.Repositories[0].Admin {
		t.Fatalf("settings = %+v", settings)
	}
	ghCallsAfterFirst := len(f.github.Calls())
	if ghCallsAfterFirst == 0 {
		t.Fatal("expected initial RepositoryAccess gh calls")
	}
	idCalls, repoCalls := app.counts()
	if idCalls != 1 || repoCalls["owner/repo"] != 1 {
		t.Fatalf("after first settings call: id=%d repos=%v", idCalls, repoCalls)
	}

	// Within TTL: repeated visits reuse cached identity, installed, and branch protection.
	f.github.SetAccess(false, true)
	clock.Advance(githubCacheTTL / 2)
	status, body = f.get(t, "/api/v1/settings/github")
	settings = decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if !settings.Repositories[0].Protected || settings.Repositories[0].Admin {
		t.Fatalf("expected cached access within TTL, got %+v", settings.Repositories[0])
	}
	if len(f.github.Calls()) != ghCallsAfterFirst {
		t.Fatalf("gh calls grew from %d to %d within TTL", ghCallsAfterFirst, len(f.github.Calls()))
	}
	idCalls, repoCalls = app.counts()
	if idCalls != 1 || repoCalls["owner/repo"] != 1 {
		t.Fatalf("within TTL: id=%d repos=%v", idCalls, repoCalls)
	}

	// ?refresh=1 bypasses the cache and updates cached values.
	status, body = f.get(t, "/api/v1/settings/github?refresh=1")
	settings = decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if settings.Repositories[0].Protected || !settings.Repositories[0].Admin {
		t.Fatalf("expected refreshed access, got %+v", settings.Repositories[0])
	}
	ghCallsAfterRefresh := len(f.github.Calls())
	if ghCallsAfterRefresh <= ghCallsAfterFirst {
		t.Fatalf("expected fresh gh calls on refresh: %d <= %d", ghCallsAfterRefresh, ghCallsAfterFirst)
	}
	idCalls, repoCalls = app.counts()
	if idCalls != 2 || repoCalls["owner/repo"] != 2 {
		t.Fatalf("after refresh: id=%d repos=%v", idCalls, repoCalls)
	}

	// Advancing past TTL expires the cache and fetches fresh branch protection.
	f.github.SetAccess(true, false)
	clock.Advance(githubCacheTTL)
	status, body = f.get(t, "/api/v1/settings/github")
	settings = decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if !settings.Repositories[0].Protected || settings.Repositories[0].Admin {
		t.Fatalf("expected fresh access after TTL expiry, got %+v", settings.Repositories[0])
	}
	idCalls, repoCalls = app.counts()
	if idCalls != 3 || repoCalls["owner/repo"] != 3 {
		t.Fatalf("after TTL expiry: id=%d repos=%v", idCalls, repoCalls)
	}

	// Transient RepositoryAccess errors are not cached and evict stale entries.
	f.github.Fail("gh: temporary 502")
	status, body = f.get(t, "/api/v1/settings/github?refresh=1")
	settings = decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if !strings.Contains(settings.Repositories[0].Problem, "could not read") {
		t.Fatalf("expected failure on refresh: %+v", settings.Repositories[0])
	}
	f.github.Recover()
	f.github.SetAccess(false, false)
	status, body = f.get(t, "/api/v1/settings/github")
	settings = decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if settings.Repositories[0].Problem != "" || settings.Repositories[0].Protected {
		t.Fatalf("expected immediate recovery without waiting for TTL: %+v", settings.Repositories[0])
	}

	// Removing and re-adding the project invalidates its cached entries immediately.
	if status, _ := f.setup(t, http.MethodDelete, "/api/v1/projects/owner/repo", nil); status != http.StatusOK {
		t.Fatalf("remove project status = %d", status)
	}
	if ok, _ := f.server.githubCache.getInstalled("owner/repo", clock.Now()); ok {
		t.Fatal("expected removeProject to invalidate installed cache immediately")
	}
	if _, ok, _ := f.server.githubCache.getAccess("owner/repo", clock.Now()); ok {
		t.Fatal("expected removeProject to invalidate access cache immediately")
	}
	f.github.SetAccess(true, false)
	if status, _ := f.setup(t, http.MethodPost, "/api/v1/projects", map[string]string{"repo": "owner/repo"}); status != http.StatusOK {
		t.Fatalf("re-add project status = %d", status)
	}
	status, body = f.get(t, "/api/v1/settings/github")
	settings = decodeAs[GitHubSettings](t, status, http.StatusOK, body)
	if len(settings.Repositories) != 1 || !settings.Repositories[0].Protected {
		t.Fatalf("expected fresh access after remove+add invalidation, got %+v", settings.Repositories)
	}
}

func TestLinearSettingsSayHowToTurnItOnAndNeverShowTheToken(t *testing.T) {
	f := newFixture(t)
	status, body := f.get(t, "/api/v1/settings/linear")
	off := decodeAs[LinearSettings](t, status, http.StatusOK, body)
	if off.Enabled || !slices.Equal(off.CredentialEnv, []string{"LINEAR_CLIENT_ID", "LINEAR_CLIENT_SECRET"}) || !strings.HasSuffix(off.Docs, "linear.md#set-it-up") || off.Teams == nil || off.AllowedUsers == nil {
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
	if on.Renews || !slices.Equal(on.CredentialEnv, []string{"LINEAR_API_TOKEN"}) {
		t.Fatalf("a fixed token: settings = %+v", on)
	}
	if strings.Contains(string(body), "lin_api_secret_value") {
		t.Fatalf("the Linear token leaked: %s", body)
	}

	f.server.config.Linear.Token = ""
	f.server.config.Linear.ClientID = "client-id-value"
	f.server.config.Linear.ClientSecret = "client-secret-value"
	status, body = f.get(t, "/api/v1/settings/linear")
	renewing := decodeAs[LinearSettings](t, status, http.StatusOK, body)
	if !renewing.Renews || !slices.Equal(renewing.CredentialEnv, []string{"LINEAR_CLIENT_ID", "LINEAR_CLIENT_SECRET"}) {
		t.Fatalf("client credentials: settings = %+v", renewing)
	}
	if strings.Contains(string(body), "client-secret-value") || strings.Contains(string(body), "client-id-value") {
		t.Fatalf("the Linear credentials leaked: %s", body)
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
	if me.Name != "runner-1" || me.Role != "all" || !me.Current || me.LastUsedAt == nil || me.Prefix != f.token[:len(tickets.TokenPrefix)+4] || vm.Role != "runner" || vm.Current || vm.LastUsedAt != nil || vm.Prefix != runnerToken[:len(tickets.TokenPrefix)+4] || alice.Role != "operator" || me.CreatedAt.IsZero() {
		t.Fatalf("tokens = %+v", found.Tokens)
	}
	if strings.Contains(string(body), f.token) || strings.Contains(string(body), runnerToken) || strings.Contains(string(body), "hash") {
		t.Fatalf("a token leaked: %s", body)
	}

	// A runner's token cannot list, create, regenerate or revoke tokens.
	if response, _ := f.call(t, http.MethodGet, "/api/v1/tokens", runnerToken, nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("a runner listed tokens: %d", response.StatusCode)
	}
	if response, _ := f.call(t, http.MethodPost, "/api/v1/tokens", runnerToken, map[string]string{"name": "rogue", "role": "operator"}); response.StatusCode != http.StatusForbidden {
		t.Fatalf("a runner created a token: %d", response.StatusCode)
	}
	if response, _ := f.call(t, http.MethodPost, "/api/v1/tokens/alice/regenerate", runnerToken, nil); response.StatusCode != http.StatusForbidden {
		t.Fatalf("a runner regenerated a token: %d", response.StatusCode)
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

func TestAgentsListTheirFrontMatterPromptAndUses(t *testing.T) {
	f := newFixture(t)
	loaded := &factory.Factory{
		Presets: map[string]factory.Preset{
			"default": {Name: "default", Steps: []runner.Step{{Name: "implement", Run: "machinist agent implement"}, {Name: "review", Run: "machinist agent review"}}},
			"quick":   {Name: "quick", Harness: "agy", Model: "gemini-4-neon-high", Steps: []runner.Step{{Name: "implement", Run: "machinist agent implement", Harness: "agy", Model: "gemini-4-neon-high"}}},
		},
		Agents: map[string]harness.Agent{
			"implement": {Name: "implement", Harness: "claude", Model: "opus", Prompt: "Implement the request."},
			"review":    {Name: "review", Harness: "codex", Program: "/opt/codex", Prompt: "Review the diff."},
			"unused":    {Name: "unused", Harness: "pi", Prompt: "Unused instructions."},
		},
	}
	f.server.factory, f.server.presets = loaded, presetViews(loaded)
	type list struct {
		Agents []AgentView `json:"agents"`
	}
	status, body := f.get(t, "/api/v1/agents")
	found := decodeAs[list](t, status, http.StatusOK, body)
	want := []AgentView{
		{
			Name: "implement", Harness: "claude", Model: "opus", Prompt: "Implement the request.",
			Presets: []string{"default", "quick"},
			Uses: []AgentUse{
				{Preset: "default", Step: "implement", Harness: "claude", Model: "opus"},
				{Preset: "quick", Step: "implement", Harness: "agy", Model: "gemini-4-neon-high"},
			},
		},
		{
			Name: "review", Harness: "codex", Command: "/opt/codex", Prompt: "Review the diff.",
			Presets: []string{"default"},
			Uses:    []AgentUse{{Preset: "default", Step: "review", Harness: "codex"}},
		},
		{
			Name: "unused", Harness: "pi", Prompt: "Unused instructions.",
			Presets: []string{},
			Uses:    []AgentUse{},
		},
	}
	if len(found.Agents) != len(want) {
		t.Fatalf("agents = %+v", found.Agents)
	}
	for index := range want {
		got := found.Agents[index]
		if got.Name != want[index].Name || got.Harness != want[index].Harness || got.Model != want[index].Model || got.Command != want[index].Command || got.Prompt != want[index].Prompt || !slices.Equal(got.Presets, want[index].Presets) || !slices.Equal(got.Uses, want[index].Uses) {
			t.Fatalf("agent %d = %+v, want %+v", index, got, want[index])
		}
	}
}

func TestCreateAndRegenerateTokensFromTheWebApp(t *testing.T) {
	f := newFixture(t)

	// Invalid requests are rejected.
	for _, body := range []map[string]string{
		{"name": "   ", "role": "operator"},
		{"name": "bad/name", "role": "operator"},
		{"name": "bob", "role": ""},
		{"name": "bob", "role": "admin"},
		{"name": "bob", "role": "operator", "scope": "admin"},
		{"name": "bob", "role": "runner", "scope": "read"},
	} {
		if response, data := f.call(t, http.MethodPost, "/api/v1/tokens", f.token, body); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("create %+v: status = %d: %s", body, response.StatusCode, data)
		}
	}

	// Creating a person token returns it once, with a prefix, and it works immediately.
	response, body := f.call(t, http.MethodPost, "/api/v1/tokens", f.token, map[string]string{"name": "bob", "role": "operator"})
	created := decodeAs[CreatedToken](t, response.StatusCode, http.StatusCreated, body)
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
	if !strings.HasPrefix(created.Token, tickets.TokenPrefix) || created.Name != "bob" || created.Role != "operator" || created.Prefix != created.Token[:len(tickets.TokenPrefix)+4] {
		t.Fatalf("created = %+v", created)
	}
	if len(created.Tokens) != 2 {
		t.Fatalf("tokens = %+v", created.Tokens)
	}
	if check, _ := f.call(t, http.MethodGet, "/api/v1/tokens", created.Token, nil); check.StatusCode != http.StatusOK {
		t.Fatalf("created token did not authenticate: %d", check.StatusCode)
	}
	// Duplicate active name is 409 Conflict.
	if dup, _ := f.call(t, http.MethodPost, "/api/v1/tokens", f.token, map[string]string{"name": "bob", "role": "runner"}); dup.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate status = %d", dup.StatusCode)
	}

	// Listing never returns the plaintext token again.
	status, listBody := f.get(t, "/api/v1/tokens")
	if status != http.StatusOK || strings.Contains(string(listBody), created.Token) || !strings.Contains(string(listBody), created.Prefix) {
		t.Fatalf("list = %d: %s", status, listBody)
	}

	// Creating a runner token gives it the runner role.
	response, body = f.call(t, http.MethodPost, "/api/v1/tokens", f.token, map[string]string{"name": "mac-mini", "role": "runner"})
	runnerCreated := decodeAs[CreatedToken](t, response.StatusCode, http.StatusCreated, body)
	if runnerCreated.Role != "runner" || !strings.HasPrefix(runnerCreated.Token, tickets.TokenPrefix) {
		t.Fatalf("runnerCreated = %+v", runnerCreated)
	}

	// Regenerating revokes the old token and returns a new one with the same name and role.
	response, body = f.call(t, http.MethodPost, "/api/v1/tokens/mac-mini/regenerate", f.token, nil)
	regenerated := decodeAs[CreatedToken](t, response.StatusCode, http.StatusOK, body)
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
	if regenerated.Token == runnerCreated.Token || !strings.HasPrefix(regenerated.Token, tickets.TokenPrefix) || regenerated.Name != "mac-mini" || regenerated.Role != "runner" || regenerated.Prefix != regenerated.Token[:len(tickets.TokenPrefix)+4] {
		t.Fatalf("regenerated = %+v", regenerated)
	}
	if oldResp, _ := f.call(t, http.MethodPost, "/api/v1/runners/poll", runnerCreated.Token, struct{}{}); oldResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old runner token still works: %d", oldResp.StatusCode)
	}
	if newResp, _ := f.call(t, http.MethodPost, "/api/v1/runners/poll", regenerated.Token, struct{}{}); newResp.StatusCode != http.StatusNoContent {
		t.Fatalf("new runner token failed: %d", newResp.StatusCode)
	}
	if missing, _ := f.call(t, http.MethodPost, "/api/v1/tokens/missing/regenerate", f.token, nil); missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing regenerate status = %d", missing.StatusCode)
	}

	// Regenerating your own token via session cookie rotates the DB session cookie so the browser stays signed in.
	initialCookie := f.signInCookie(t, created.Token)
	req, err := http.NewRequest(http.MethodPost, f.http.URL+"/api/v1/tokens/bob/regenerate", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(initialCookie)
	cookieResp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer cookieResp.Body.Close()
	var selfRegen CreatedToken
	if err := json.NewDecoder(cookieResp.Body).Decode(&selfRegen); err != nil {
		t.Fatal(err)
	}
	if cookieResp.StatusCode != http.StatusOK || selfRegen.Token == created.Token {
		t.Fatalf("self regen = %d, %+v", cookieResp.StatusCode, selfRegen)
	}
	cookie := findCookie(cookieResp)
	if cookie == nil || !strings.HasPrefix(cookie.Value, tickets.SessionPrefix) || cookie.Value == initialCookie.Value || cookie.Value == selfRegen.Token {
		t.Fatalf("rotated cookie = %+v, want fresh opaque session secret", cookie)
	}
	if got := f.withCookie(t, http.MethodGet, "/api/v1/tokens", cookie.Value); got.StatusCode != http.StatusOK {
		t.Fatalf("rotated cookie did not work: %d", got.StatusCode)
	}
	if got := f.withCookie(t, http.MethodGet, "/api/v1/tokens", initialCookie.Value); got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session cookie still worked: %d", got.StatusCode)
	}
}

func TestSettingsRoutesNeedASignIn(t *testing.T) {
	f := newFixture(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/settings/github"},
		{http.MethodGet, "/api/v1/settings/linear"},
		{http.MethodGet, "/api/v1/tokens"},
		{http.MethodPost, "/api/v1/tokens"},
		{http.MethodPost, "/api/v1/tokens/runner-1/regenerate"},
		{http.MethodDelete, "/api/v1/tokens/runner-1"},
		{http.MethodGet, "/api/v1/agents"},
	} {
		if response, _ := f.call(t, route.method, route.path, "", nil); response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s: status = %d", route.method, route.path, response.StatusCode)
		}
	}
}
