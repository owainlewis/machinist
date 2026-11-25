package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/sources"
	"github.com/owainlewis/machinist/internal/tickets"
)

type reloadFixture struct {
	*fixture
	dir        string
	configPath string
	factoryDir string
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newReloadFixture(t *testing.T) *reloadFixture {
	t.Helper()
	github := fakegh.Install(t, "machinist-bot")
	dir := t.TempDir()
	factoryDir := filepath.Join(dir, "factory")
	writeFile(t, filepath.Join(factoryDir, "agents", "implement.md"), "---\nharness: claude\nmodel: sonnet\n---\nImplement v1.\n")
	writeFile(t, filepath.Join(factoryDir, "prompts", "rules.md"), "Old rules v1.\n")
	writeFile(t, filepath.Join(factoryDir, "scripts", "check.sh"), "#!/bin/sh\necho check-v1\n")
	writeFile(t, filepath.Join(factoryDir, "presets", "default.yaml"), "description: Default v1.\nsteps:\n  - name: work\n    run: echo old-step\n")
	writeFile(t, filepath.Join(factoryDir, "presets", "legacy.yaml"), "description: Legacy preset.\nsteps:\n  - name: legacy-step\n    run: echo legacy-run\n")

	configPath := filepath.Join(dir, "config.yaml")
	writeFile(t, configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default
  run_timeout: 30m

github:
  handle: machinist-bot
  repositories:
    - owner/alpha
`)

	loadedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	loadedFac, err := factory.Load(loadedCfg.Server.Factory)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tickets.Open(loadedCfg.Server.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server, err := New(loadedCfg, store, loadedFac, "v1.2.3", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateToken(t.Context(), "alice", tickets.RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)

	return &reloadFixture{
		fixture:    &fixture{server: server, store: store, http: web, token: token, github: github},
		dir:        dir,
		configPath: configPath,
		factoryDir: factoryDir,
	}
}

func (rf *reloadFixture) postReload(t *testing.T, token string) (int, ReloadResult, string) {
	t.Helper()
	resp, body := rf.call(t, http.MethodPost, "/api/v1/reload", token, nil)
	if resp.StatusCode == http.StatusOK {
		var result ReloadResult
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode reload result: %v (%s)", err, body)
		}
		return resp.StatusCode, result, ""
	}
	var failure struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &failure)
	return resp.StatusCode, ReloadResult{}, failure.Error
}

func TestReloadAppliesConfigAndFactoryChanges(t *testing.T) {
	t.Setenv(config.LinearTokenEnv, "lin_test_token")
	rf := newReloadFixture(t)

	// No-op reload reports no changes and no restart-needed settings.
	status, res, errMsg := rf.postReload(t, rf.token)
	if status != http.StatusOK || len(res.Changed) != 0 || len(res.RestartNeeded) != 0 {
		t.Fatalf("no-op reload = %d %+v err=%q", status, res, errMsg)
	}

	// Update config.yaml and every part of the factory.
	writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: fast
  run_timeout: 45m

github:
  handle: new-bot
  auto_merge: false
  repositories:
    - owner/beta

linear:
  app_user_id: app-1
  allowed_users: [user-1]
  poll_interval: 30s
  teams:
    ENG: owner/beta
`)
	writeFile(t, filepath.Join(rf.factoryDir, "agents", "implement.md"), "---\nharness: codex\nmodel: gpt-5-codex\n---\nImplement v2.\n")
	writeFile(t, filepath.Join(rf.factoryDir, "agents", "review.md"), "---\nharness: claude\n---\nReview v1.\n")
	writeFile(t, filepath.Join(rf.factoryDir, "prompts", "rules.md"), "New rules v2.\n")
	writeFile(t, filepath.Join(rf.factoryDir, "scripts", "lint.sh"), "#!/bin/sh\necho lint\n")
	writeFile(t, filepath.Join(rf.factoryDir, "presets", "fast.yaml"), "description: Fast preset.\nsteps:\n  - name: work\n    agent: implement\n")
	writeFile(t, filepath.Join(rf.factoryDir, "presets", "default.yaml"), "description: Default v2.\nsteps:\n  - name: work\n    run: echo new-step\n")
	writeFile(t, filepath.Join(rf.factoryDir, "automations", "nightly.md"), "---\nschedule: \"0 9 * * *\"\ntimezone: UTC\nrepo: owner/beta\npreset: fast\n---\nRun nightly checks.\n")

	status, res, errMsg = rf.postReload(t, rf.token)
	if status != http.StatusOK {
		t.Fatalf("reload failed: %d %q", status, errMsg)
	}
	if len(res.RestartNeeded) != 0 {
		t.Fatalf("unexpected restart_needed: %v", res.RestartNeeded)
	}
	for _, want := range []string{
		"server.default_preset: default -> fast",
		"server.run_timeout: 30m0s -> 45m0s",
		"github.handle: machinist-bot -> new-bot",
		"github.auto_merge: true -> false",
		"github.repositories: added owner/beta; removed owner/alpha",
		"linear: enabled",
		"preset added: fast",
		"preset updated: default",
		"agent added: review",
		"agent updated: implement",
		"prompt updated: rules.md",
		"script added: lint.sh",
		"automation added: nightly",
	} {
		if !slices.Contains(res.Changed, want) {
			t.Fatalf("changed = %q, missing %q", res.Changed, want)
		}
	}

	// Verify live endpoints serve the reloaded state.
	resp, body := rf.call(t, http.MethodGet, "/api/v1/repositories", rf.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "owner/beta") || strings.Contains(string(body), "owner/alpha") {
		t.Fatalf("repositories = %d %s", resp.StatusCode, body)
	}
	resp, body = rf.call(t, http.MethodGet, "/api/v1/presets", rf.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"default":"fast"`) || !strings.Contains(string(body), `"name":"fast"`) {
		t.Fatalf("presets = %d %s", resp.StatusCode, body)
	}
	resp, body = rf.call(t, http.MethodGet, "/api/v1/agents", rf.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"review"`) || !strings.Contains(string(body), `"harness":"codex"`) {
		t.Fatalf("agents = %d %s", resp.StatusCode, body)
	}
	resp, body = rf.call(t, http.MethodGet, "/api/v1/automations", rf.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"nightly"`) {
		t.Fatalf("automations = %d %s", resp.StatusCode, body)
	}
	resp, body = rf.call(t, http.MethodGet, "/api/v1/settings/github", rf.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"handle":"new-bot"`) || !strings.Contains(string(body), `"auto_merge":false`) {
		t.Fatalf("github settings = %d %s", resp.StatusCode, body)
	}
	resp, body = rf.call(t, http.MethodGet, "/api/v1/settings/linear", rf.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"enabled":true`) || !strings.Contains(string(body), `"poll_seconds":30`) {
		t.Fatalf("linear settings = %d %s", resp.StatusCode, body)
	}

	// Disable Linear and remove the automation & legacy preset, then reload again.
	writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default
  run_timeout: 45m

github:
  handle: new-bot
  auto_merge: false
  repositories:
    - owner/beta
`)
	if err := os.Remove(filepath.Join(rf.factoryDir, "automations", "nightly.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(rf.factoryDir, "presets", "legacy.yaml")); err != nil {
		t.Fatal(err)
	}
	status, res, errMsg = rf.postReload(t, rf.token)
	if status != http.StatusOK {
		t.Fatalf("second reload failed: %d %q", status, errMsg)
	}
	for _, want := range []string{"linear: disabled", "preset removed: legacy", "automation removed: nightly"} {
		if !slices.Contains(res.Changed, want) {
			t.Fatalf("changed = %q, missing %q", res.Changed, want)
		}
	}
	resp, body = rf.call(t, http.MethodGet, "/api/v1/settings/linear", rf.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"enabled":false`) {
		t.Fatalf("linear settings after disable = %d %s", resp.StatusCode, body)
	}
}

func TestReloadRejectsInvalidConfigOrFactoryAndKeepsPreviousState(t *testing.T) {
	rf := newReloadFixture(t)
	origConfig, err := os.ReadFile(rf.configPath)
	if err != nil {
		t.Fatal(err)
	}
	origPreset, err := os.ReadFile(filepath.Join(rf.factoryDir, "presets", "default.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	assertUnchanged := func(t *testing.T) {
		t.Helper()
		resp, body := rf.call(t, http.MethodGet, "/api/v1/repositories", rf.token, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "owner/alpha") {
			t.Fatalf("repositories changed after failed reload: %d %s", resp.StatusCode, body)
		}
		resp, body = rf.call(t, http.MethodGet, "/api/v1/presets", rf.token, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"default":"default"`) || !strings.Contains(string(body), "echo old-step") {
			t.Fatalf("presets changed after failed reload: %d %s", resp.StatusCode, body)
		}
	}

	t.Run("bad factory preset", func(t *testing.T) {
		writeFile(t, filepath.Join(rf.factoryDir, "presets", "default.yaml"), "description: Broken.\nsteps:\n  - name: bad\n    agent: nonexistent-agent\n")
		t.Cleanup(func() { writeFile(t, filepath.Join(rf.factoryDir, "presets", "default.yaml"), string(origPreset)) })
		status, _, errMsg := rf.postReload(t, rf.token)
		if status != http.StatusBadRequest || !strings.Contains(errMsg, "nonexistent-agent") {
			t.Fatalf("status = %d, err = %q", status, errMsg)
		}
		assertUnchanged(t)
	})

	t.Run("unknown key in config.yaml", func(t *testing.T) {
		writeFile(t, rf.configPath, string(origConfig)+"\nbogus_key: true\n")
		t.Cleanup(func() { writeFile(t, rf.configPath, string(origConfig)) })
		status, _, errMsg := rf.postReload(t, rf.token)
		if status != http.StatusBadRequest || !strings.Contains(errMsg, "unknown key bogus_key") {
			t.Fatalf("status = %d, err = %q", status, errMsg)
		}
		assertUnchanged(t)
	})

	t.Run("automation references removed repository", func(t *testing.T) {
		autoPath := filepath.Join(rf.factoryDir, "automations", "bad.md")
		writeFile(t, autoPath, "---\nschedule: \"0 9 * * *\"\ntimezone: UTC\nrepo: owner/unknown\npreset: default\n---\nCheck things.\n")
		t.Cleanup(func() { _ = os.Remove(autoPath) })
		status, _, errMsg := rf.postReload(t, rf.token)
		if status != http.StatusBadRequest || !strings.Contains(errMsg, "owner/unknown") {
			t.Fatalf("status = %d, err = %q", status, errMsg)
		}
		assertUnchanged(t)
	})

	t.Run("missing github.handle", func(t *testing.T) {
		writeFile(t, rf.configPath, "server:\n  listen: 127.0.0.1:0\n  database: .machinist/tickets.db\n  factory: factory\n")
		t.Cleanup(func() { writeFile(t, rf.configPath, string(origConfig)) })
		status, _, errMsg := rf.postReload(t, rf.token)
		if status != http.StatusBadRequest || !strings.Contains(errMsg, "github.handle") {
			t.Fatalf("status = %d, err = %q", status, errMsg)
		}
		assertUnchanged(t)
	})

	t.Run("github app rejects step longer than token lifetime", func(t *testing.T) {
		keyPath := writeAppKey(t)
		writeFile(t, rf.configPath, "server:\n  listen: 127.0.0.1:0\n  database: .machinist/tickets.db\n  factory: factory\n  run_timeout: 2h\n\ngithub:\n  handle: machinist-bot\n  repositories:\n    - owner/alpha\n  app:\n    id: 7\n    private_key_file: "+keyPath+"\n")
		t.Cleanup(func() { writeFile(t, rf.configPath, string(origConfig)) })
		status, _, errMsg := rf.postReload(t, rf.token)
		if status != http.StatusBadRequest || !strings.Contains(errMsg, "GitHub App tokens last") {
			t.Fatalf("status = %d, err = %q", status, errMsg)
		}
		if rf.server.state().app != nil {
			t.Fatal("failed reload mutated server.app")
		}
		assertUnchanged(t)
	})
}

func TestReloadNamesRestartRequiredSettingsAndDoesNotApplyThem(t *testing.T) {
	rf := newReloadFixture(t)
	origState := rf.server.state().config.Server

	writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:9999
  database: .machinist/other.db
  factory: factory
  default_preset: default
  tls_cert: cert.pem
  tls_key: key.pem

github:
  handle: machinist-bot
  repositories:
    - owner/alpha
    - owner/gamma
`)

	status, res, errMsg := rf.postReload(t, rf.token)
	if status != http.StatusOK {
		t.Fatalf("reload failed: %d %q", status, errMsg)
	}
	wantRestart := []string{"server.listen", "server.database", "server.tls_cert", "server.tls_key"}
	if !slices.Equal(res.RestartNeeded, wantRestart) {
		t.Fatalf("restart_needed = %v, want %v", res.RestartNeeded, wantRestart)
	}
	if !slices.Contains(res.Changed, "github.repositories: added owner/gamma") {
		t.Fatalf("changed = %v, expected reloadable repo addition to still apply", res.Changed)
	}

	// Restart-only fields kept their startup values on the live server.
	cur := rf.server.state().config.Server
	if cur.Listen != origState.Listen || cur.Database != origState.Database || cur.TLSCert != origState.TLSCert || cur.TLSKey != origState.TLSKey {
		t.Fatalf("restart-only settings were mutated: got %+v, want %+v", cur, origState)
	}

	// Reloadable repository change took effect.
	resp, body := rf.call(t, http.MethodGet, "/api/v1/repositories", rf.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "owner/gamma") {
		t.Fatalf("repositories = %d %s", resp.StatusCode, body)
	}

	// A second reload without restarting still reports the same restart-needed settings.
	status, res, errMsg = rf.postReload(t, rf.token)
	if status != http.StatusOK || len(res.Changed) != 0 || !slices.Equal(res.RestartNeeded, wantRestart) {
		t.Fatalf("second reload = %d %+v err=%q", status, res, errMsg)
	}
}

func TestReloadKeepsAlreadyQueuedAndRunningJobsOnTheirOriginalJobDefinition(t *testing.T) {
	rf := newReloadFixture(t)
	runnerToken := rf.newToken(t, "runner-1", tickets.RoleRunner)

	// Job 1: running before reload.
	t1, _, err := rf.server.intake.Run(t.Context(), sources.RunRequest{
		Key: "job-1", Preset: "default", Vars: map[string]string{"repo": "owner/alpha"}, Prompt: "Running job",
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := rf.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	resp, body := rf.call(t, http.MethodPost, "/api/v1/runners/poll", runnerToken, protocol.PollRequest{Harnesses: []string{protocol.StepsHarness}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll job-1 = %d %s", resp.StatusCode, body)
	}
	var runningReq runner.RunRequest
	if err := json.Unmarshal(body, &runningReq); err != nil {
		t.Fatal(err)
	}
	if runningReq.TicketID != t1.ID {
		t.Fatalf("claimed %s, want %s", runningReq.TicketID, t1.ID)
	}

	// Job 2: queued before reload using the "legacy" preset (which will be deleted in reload).
	t2, _, err := rf.server.intake.Run(t.Context(), sources.RunRequest{
		Key: "job-2", Preset: "legacy", Vars: map[string]string{"repo": "owner/alpha"}, Prompt: "Queued legacy job",
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := rf.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Job 3: created before reload (awaiting Tick) using "default" preset (which will be edited in reload).
	t3, _, err := rf.server.intake.Run(t.Context(), sources.RunRequest{
		Key: "job-3", Preset: "default", Vars: map[string]string{"repo": "owner/alpha"}, Prompt: "Awaiting job",
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}

	// Now edit config & factory and reload: edit default preset + rules.md, delete legacy preset, change auto_merge & run_timeout.
	writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default
  run_timeout: 15m

github:
  handle: reloaded-bot
  auto_merge: false
  repositories:
    - owner/alpha
`)
	writeFile(t, filepath.Join(rf.factoryDir, "presets", "default.yaml"), "description: Default v2.\nsteps:\n  - name: work-v2\n    run: echo new-step\n")
	writeFile(t, filepath.Join(rf.factoryDir, "prompts", "rules.md"), "New rules v2.\n")
	if err := os.Remove(filepath.Join(rf.factoryDir, "presets", "legacy.yaml")); err != nil {
		t.Fatal(err)
	}

	status, _, errMsg := rf.postReload(t, rf.token)
	if status != http.StatusOK {
		t.Fatalf("reload failed: %d %q", status, errMsg)
	}

	// Running Job 1 continues heartbeating and completes cleanly.
	if resp, body := rf.call(t, http.MethodPost, "/api/v1/runs/"+runningReq.RunID+"/heartbeat", runnerToken, struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat job-1 = %d %s", resp.StatusCode, body)
	}
	if resp, body := rf.call(t, http.MethodPost, "/api/v1/runs/"+runningReq.RunID+"/complete", runnerToken, protocol.Completion{
		State:  tickets.RunSucceeded,
		Report: &protocol.Report{Outcome: protocol.OutcomeNoChange, Summary: "Done."},
	}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("complete job-1 = %d %s", resp.StatusCode, body)
	}

	// Queued Job 2 (whose preset "legacy" was deleted) still shows its original steps and claims with its original definition.
	resp, body = rf.call(t, http.MethodGet, "/api/v1/tickets/"+t2.ID, rf.token, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"legacy-step"`) {
		t.Fatalf("get ticket job-2 = %d %s", resp.StatusCode, body)
	}
	resp, body = rf.call(t, http.MethodPost, "/api/v1/runners/poll", runnerToken, protocol.PollRequest{Harnesses: []string{protocol.StepsHarness}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll job-2 = %d %s", resp.StatusCode, body)
	}
	var req2 runner.RunRequest
	if err := json.Unmarshal(body, &req2); err != nil {
		t.Fatal(err)
	}
	if req2.TicketID != t2.ID || len(req2.Steps) != 1 || req2.Steps[0].Run != "echo legacy-run" || req2.Files["prompts/rules.md"] != "Old rules v1.\n" || req2.Vars["auto_merge"] != "true" || req2.GitName != "machinist-bot" || req2.TimeoutMillis != (30*time.Minute).Milliseconds() {
		t.Fatalf("job-2 got mutated definition: %+v", req2)
	}

	// Job 3 (created before reload, ticked after reload) also keeps its original "default" v1 definition.
	if err := rf.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	resp, body = rf.call(t, http.MethodPost, "/api/v1/runners/poll", runnerToken, protocol.PollRequest{Harnesses: []string{protocol.StepsHarness}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll job-3 = %d %s", resp.StatusCode, body)
	}
	var req3 runner.RunRequest
	if err := json.Unmarshal(body, &req3); err != nil {
		t.Fatal(err)
	}
	if req3.TicketID != t3.ID || len(req3.Steps) != 1 || req3.Steps[0].Name != "work" || req3.Steps[0].Run != "echo old-step" || req3.Files["prompts/rules.md"] != "Old rules v1.\n" {
		t.Fatalf("job-3 got mutated definition: %+v", req3)
	}

	// A new job created AFTER reload gets the new definition.
	t4, _, err := rf.server.intake.Run(t.Context(), sources.RunRequest{
		Key: "job-4", Preset: "default", Vars: map[string]string{"repo": "owner/alpha"}, Prompt: "New job",
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := rf.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	resp, body = rf.call(t, http.MethodPost, "/api/v1/runners/poll", runnerToken, protocol.PollRequest{Harnesses: []string{protocol.StepsHarness}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll job-4 = %d %s", resp.StatusCode, body)
	}
	var req4 runner.RunRequest
	if err := json.Unmarshal(body, &req4); err != nil {
		t.Fatal(err)
	}
	if req4.TicketID != t4.ID || len(req4.Steps) != 1 || req4.Steps[0].Name != "work-v2" || req4.Steps[0].Run != "echo new-step" || req4.Files["prompts/rules.md"] != "New rules v2.\n" || req4.Vars["auto_merge"] != "false" || req4.GitName != "reloaded-bot" || req4.TimeoutMillis != (15*time.Minute).Milliseconds() {
		t.Fatalf("job-4 did not get reloaded definition: %+v", req4)
	}

	// A new job requesting the removed "legacy" preset is rejected.
	if _, _, err := rf.server.intake.Run(t.Context(), sources.RunRequest{
		Key: "job-5", Preset: "legacy", Vars: map[string]string{"repo": "owner/alpha"}, Prompt: "Should fail",
	}, "alice"); err == nil || !strings.Contains(err.Error(), `preset "legacy" does not exist`) {
		t.Fatalf("expected missing preset error, got %v", err)
	}
}

func TestReloadRequiresFullOperatorToken(t *testing.T) {
	rf := newReloadFixture(t)
	runnerToken := rf.newToken(t, "runner-1", tickets.RoleRunner)
	readToken := rf.newScopedToken(t, "watcher", tickets.ScopeRead)
	runToken := rf.newScopedToken(t, "bot", tickets.ScopeRun)

	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{"unauthenticated", "", http.StatusUnauthorized},
		{"runner token", runnerToken, http.StatusForbidden},
		{"read scope", readToken, http.StatusForbidden},
		{"run scope", runToken, http.StatusForbidden},
		{"full operator", rf.token, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, errMsg := rf.postReload(t, tc.token)
			if status != tc.want {
				t.Fatalf("status = %d, want %d (err=%q)", status, tc.want, errMsg)
			}
		})
	}
}

func TestReloadConcurrentReadsAndServeLinearLifecycle(t *testing.T) {
	t.Setenv(config.LinearTokenEnv, "lin_test_token")
	rf := newReloadFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	listening := make(chan net.Addr, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- rf.server.Serve(ctx, func(addr net.Addr) { listening <- addr })
	}()
	addr := <-listening
	baseURL := "http://" + addr.String()

	var wg sync.WaitGroup
	stopReaders := make(chan struct{})
	for _, path := range []string{"/api/v1/presets", "/api/v1/agents", "/api/v1/automations", "/api/v1/repositories", "/api/v1/settings/linear"} {
		wg.Add(1)
		go func(route string) {
			defer wg.Done()
			client := &http.Client{Timeout: 5 * time.Second}
			for {
				select {
				case <-stopReaders:
					return
				default:
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+route, nil)
				if err != nil {
					return
				}
				req.Header.Set("Authorization", "Bearer "+rf.token)
				resp, err := client.Do(req)
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		}(path)
	}

	// Toggle Linear on and off while Serve and concurrent readers run.
	for i := range 3 {
		if i%2 == 0 {
			writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default

github:
  handle: machinist-bot
  repositories:
    - owner/alpha

linear:
  app_user_id: app-1
  allowed_users: [user-1]
  poll_interval: 10m
  teams:
    ENG: owner/alpha
`)
		} else {
			writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default

github:
  handle: machinist-bot
  repositories:
    - owner/alpha
`)
		}
		if _, err := rf.server.Reload(ctx); err != nil {
			t.Fatalf("reload %d: %v", i, err)
		}
	}

	close(stopReaders)
	wg.Wait()
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serve error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not stop after cancellation")
	}
}
