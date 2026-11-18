package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const runnerBase = "control_plane = \"http://10.0.0.5:7331\"\ntoken_file = \"runner.token\"\n"

func loadRunnerBody(t *testing.T, body string) (Runner, string, error) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "runner.toml")
	writeTestFile(t, path, body)
	runner, err := LoadRunner(path)
	return runner, directory, err
}

func TestLoadRunnerResolvesPathsAndListsHarnesses(t *testing.T) {
	runner, directory, err := loadRunnerBody(t, runnerBase+`data_directory = "data"
slots = 2
env = ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"]

[kubernetes]
kubeconfig = "cluster/config"
secret = "agent-credentials"
allowed_hosts = ["proxy.golang.org"]
`)
	if err != nil {
		t.Fatal(err)
	}
	if runner.TokenFile != filepath.Join(directory, "runner.token") || runner.DataDirectory != filepath.Join(directory, "data") || runner.Slots != 2 {
		t.Fatalf("runner = %+v", runner)
	}
	if runner.Kubernetes.Kubeconfig != filepath.Join(directory, "cluster", "config") || runner.Kubernetes.Secret != "agent-credentials" || runner.Kubernetes.AllowedHosts[0] != "proxy.golang.org" {
		t.Fatalf("kubernetes = %+v", runner.Kubernetes)
	}
	if runner.Kind() != "kubernetes" || (Runner{}).Kind() != "local" {
		t.Fatalf("kind = %q", runner.Kind())
	}
	if got := strings.Join(runner.Harnesses(), ",") + " " + strings.Join(runner.Env, ","); got != "steps ANTHROPIC_API_KEY,OPENAI_API_KEY" {
		t.Fatalf("harnesses = %q", got)
	}
}

func TestLoadRunnerDefaultsSlotsTokenAndDataDirectory(t *testing.T) {
	runner, directory, err := loadRunnerBody(t, "control_plane = \"http://10.0.0.5:7331\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if runner.Slots != 1 || runner.DataDirectory != filepath.Join(directory, ".machinist", "runner") || runner.TokenFile != filepath.Join(directory, ".machinist", "runner.token") {
		t.Fatalf("runner = %+v", runner)
	}
}

func TestRunnerTokenReadsTrimmedFile(t *testing.T) {
	runner, directory, err := loadRunnerBody(t, runnerBase+"login = true\n")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(directory, "runner.token"), " secret-token\n")
	token, err := runner.Token()
	if err != nil || token != "secret-token" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}

func TestLoadRunnerNamesOldKeys(t *testing.T) {
	for _, test := range []struct{ body, key string }{
		{runnerBase + "[executors.codex]\ncommand = [\"codex\"]\n", "executors"},
		{runnerBase + "[repositories.app]\npath = \"/srv/app\"\n", "repositories"},
		{runnerBase + "name = \"worker\"\n", "name"},
		{runnerBase + "[kubernetes]\nauth_secret = \"old\"\n", "kubernetes.auth_secret"},
		{runnerBase + "[credentials.claude]\nlogin = true\n", "credentials"},
	} {
		if _, _, err := loadRunnerBody(t, test.body); err == nil || !strings.Contains(err.Error(), "unknown key "+test.key) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestLoadRunnerRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct{ body, want string }{
		{"token_file = \"t\"\n", "control_plane"},
		{"control_plane = \"10.0.0.5:7331\"\ntoken_file = \"t\"\n", "control_plane"},
		{runnerBase + "slots = -1\n", "slots"},
		{runnerBase + "env = [\"BAD=NAME\"]\n", "invalid environment variable"},
		{runnerBase + "login = true\n[kubernetes]\nsecret = \"s\"\n", "do not support login"},
	} {
		if _, _, err := loadRunnerBody(t, test.body); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%q: error = %v, want %q", test.body, err, test.want)
		}
	}
}
