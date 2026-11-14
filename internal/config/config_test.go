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
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestFile(t, path, "")
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := loaded.Server
	if server.Listen != "127.0.0.1:7331" || server.DefaultAgent != "default" || server.MaxTurnsWithoutHuman != 5 || server.RunDeadline() != time.Hour {
		t.Fatalf("server = %+v", server)
	}
	if server.Database != filepath.Join(home, ".machinist", "server", "tickets.db") || server.Factory != filepath.Join(home, ".machinist", "factory") {
		t.Fatalf("paths = %q, %q", server.Database, server.Factory)
	}
	if loaded.Path() != path {
		t.Fatalf("path = %q", loaded.Path())
	}
}

func TestLoadConfigReadsEveryKeyAndResolvesPaths(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	writeTestFile(t, path, `[server]
listen = "10.0.0.5:7331"
database = "data/tickets.db"
factory = "factory"
default_agent = "maintainer"
max_turns_without_human = 2
run_timeout = "15m"
tls_cert = "tls.crt"
tls_key = "tls.key"

[github]
handle = "machinist-bot"
repositories = ["owner/one", "owner/two"]
`)
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := loaded.Server
	if server.Listen != "10.0.0.5:7331" || server.DefaultAgent != "maintainer" || server.MaxTurnsWithoutHuman != 2 || server.RunDeadline() != 15*time.Minute {
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
		{"[server]\nstate = \"factory\"\n", "server.state"},
		{"[server]\nworker_token_file = \"worker.token\"\n", "server.worker_token_file"},
		{"[server]\nmax_concurrent_jobs = 2\n", "server.max_concurrent_jobs"},
		{"[commands.plan]\nexecutor = \"codex\"\n", "commands"},
		{"[workflows.factory]\nmode = \"factory\"\n", "workflows"},
		{"[triggers.github.intake]\nevery = \"5m\"\n", "triggers"},
		{"[storage]\nbackend = \"local\"\n", "storage"},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		writeTestFile(t, path, test.body)
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown key "+test.key) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct{ body, want string }{
		{"[server]\nmax_turns_without_human = -1\n", "max_turns_without_human"},
		{"[server]\nrun_timeout = \"soon\"\n", "run_timeout"},
		{"[server]\nrun_timeout = \"-1m\"\n", "run_timeout"},
		{"[server]\ntls_cert = \"tls.crt\"\n", "tls_cert and tls_key"},
		{"not toml =", "parse config"},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		writeTestFile(t, path, test.body)
		if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestExampleConfigurationLoads(t *testing.T) {
	loaded, err := LoadConfig(filepath.Join("..", "..", "examples", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.DefaultAgent != "default" || loaded.GitHub.Handle == "" {
		t.Fatalf("example = %+v", loaded)
	}
}
