package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigAppliesDefaults(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	writeTestFile(t, path, "")
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := loaded.Server
	if server.Listen != "127.0.0.1:7331" || server.DefaultPreset != "default" || server.RunDeadline() != time.Hour {
		t.Fatalf("server = %+v", server)
	}
	if server.Database != filepath.Join(directory, ".machinist", "tickets.db") || server.Factory != filepath.Join(directory, "factory") {
		t.Fatalf("paths = %q, %q", server.Database, server.Factory)
	}
	if loaded.Path() != path {
		t.Fatalf("path = %q", loaded.Path())
	}
}

func TestLoadConfigReadsTheCurrentDirectoryByDefault(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	writeTestFile(t, filepath.Join(directory, "config.yaml"), "github:\n  handle: bot\n")
	loaded, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Path() != filepath.Join(directory, "config.yaml") || loaded.GitHub.Handle != "bot" {
		t.Fatalf("loaded %q with handle %q", loaded.Path(), loaded.GitHub.Handle)
	}
}

func TestLoadConfigReadsEveryKeyAndResolvesPaths(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	writeTestFile(t, path, `server:
  listen: 10.0.0.5:7331
  database: data/tickets.db
  factory: factory
  default_preset: maintainer
  run_timeout: 15m
  tls_cert: tls.crt
  tls_key: tls.key

github:
  handle: machinist-bot
  repositories: [owner/one, owner/two]
`)
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := loaded.Server
	if server.Listen != "10.0.0.5:7331" || server.DefaultPreset != "maintainer" || server.RunDeadline() != 15*time.Minute {
		t.Fatalf("server = %+v", server)
	}
	for got, want := range map[string]string{server.Database: "data/tickets.db", server.Factory: "factory", server.TLSCert: "tls.crt", server.TLSKey: "tls.key"} {
		if got != filepath.Join(directory, filepath.FromSlash(want)) {
			t.Fatalf("path = %q, want it under %q", got, directory)
		}
	}
	if loaded.GitHub.Handle != "machinist-bot" || strings.Join(loaded.GitHub.Repositories, ",") != "owner/one,owner/two" {
		t.Fatalf("github = %+v", loaded.GitHub)
	}
}

func TestLoadConfigNamesOldKeys(t *testing.T) {
	for _, test := range []struct{ body, key string }{
		{"server:\n  state: factory\n", "state on line 2"},
		{"server:\n  worker_token_file: worker.token\n", "worker_token_file"},
		{"server:\n  max_concurrent_jobs: 2\n", "max_concurrent_jobs"},
		{"commands:\n  plan:\n    executor: codex\n", "commands on line 1"},
		{"workflows:\n  factory: {}\n", "workflows"},
		{"storage:\n  backend: local\n", "storage"},
		{"server:\n  max_turns_without_human: 5\n", "max_turns_without_human"},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		writeTestFile(t, path, test.body)
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown key "+test.key) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct{ body, want string }{
		{"server:\n  run_timeout: soon\n", "run_timeout"},
		{"server:\n  run_timeout: -1m\n", "run_timeout"},
		{"server:\n  tls_cert: tls.crt\n", "tls_cert and tls_key"},
		{"server: [", "parse config"},
		{"server:\n  run_timeout: [1]\n", "parse config"},
		{"github:\n  handle: one\n---\ngithub:\n  handle: two\n", "only one YAML document"},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		writeTestFile(t, path, test.body)
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestExampleConfigurationLoads(t *testing.T) {
	loaded, err := LoadConfig(filepath.Join("..", "..", "examples", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.DefaultPreset != "default" || loaded.GitHub.Handle == "" {
		t.Fatalf("example = %+v", loaded)
	}
}

func TestLoadConfigExplainsAnOldTOMLFile(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	writeTestFile(t, filepath.Join(directory, "config.toml"), "[github]\nhandle = \"bot\"\n")
	for _, path := range []string{"", filepath.Join(directory, "config.toml")} {
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "is TOML, and Machinist now reads YAML. Rewrite it as "+filepath.Join(directory, "config.yaml")) {
			t.Fatalf("%q: error = %v", path, err)
		}
	}
}

func TestLinearIsOffUntilTeamsAreSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "github:\n  handle: machinist-bot\n")
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Linear.Enabled() {
		t.Fatalf("linear = %+v, err = %v", loaded.Linear, err)
	}
	writeTestFile(t, path, "linear:\n  app_user_id: app-user\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "set teams") {
		t.Fatalf("error = %v", err)
	}
}

func TestLinearNeedsASecretAndATrigger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "linear:\n  app_user_id: app-user\n  teams:\n    ENG: owner/repo\n")
	t.Setenv(LinearSecretEnv, "")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), LinearSecretEnv) {
		t.Fatalf("no secret: error = %v", err)
	}
	t.Setenv(LinearSecretEnv, "short")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("short secret: error = %v", err)
	}
	t.Setenv(LinearSecretEnv, "lin_wh_0123456789abcdef")
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Linear.Secret != "lin_wh_0123456789abcdef" || loaded.Linear.MaxJobsPerHour != 20 {
		t.Fatalf("linear = %+v, err = %v", loaded.Linear, err)
	}
	writeTestFile(t, path, "linear:\n  teams:\n    ENG: owner/repo\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "app_user_id") {
		t.Fatalf("no trigger: error = %v", err)
	}
	writeTestFile(t, path, "linear:\n  secret: in-the-file\n  teams:\n    ENG: owner/repo\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown key secret") {
		t.Fatalf("secret in the file: error = %v", err)
	}
}
