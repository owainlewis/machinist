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

[credentials.codex]
env = ["OPENAI_API_KEY"]

[credentials.claude]
env = ["ANTHROPIC_API_KEY"]

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
	if got := strings.Join(runner.Harnesses(), ","); got != "claude,codex" {
		t.Fatalf("harnesses = %q", got)
	}
}

func TestLoadRunnerDefaultsSlotsAndDataDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runner, _, err := loadRunnerBody(t, runnerBase+"[credentials.claude]\nlogin = true\n")
	if err != nil {
		t.Fatal(err)
	}
	if runner.Slots != 1 || runner.DataDirectory != filepath.Join(home, ".machinist", "runner") {
		t.Fatalf("runner = %+v", runner)
	}
}

func TestRunnerTokenReadsTrimmedFile(t *testing.T) {
	runner, directory, err := loadRunnerBody(t, runnerBase+"[credentials.claude]\nlogin = true\n")
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
		{runnerBase + "[credentials.claude]\nlogin = true\n[kubernetes]\nauth_secret = \"old\"\n", "kubernetes.auth_secret"},
	} {
		if _, _, err := loadRunnerBody(t, test.body); err == nil || !strings.Contains(err.Error(), "unknown key "+test.key) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestLoadRunnerRejectsInvalidConfiguration(t *testing.T) {
	login := "[credentials.claude]\nlogin = true\n"
	for _, test := range []struct{ body, want string }{
		{"token_file = \"t\"\n" + login, "control_plane"},
		{"control_plane = \"10.0.0.5:7331\"\ntoken_file = \"t\"\n" + login, "control_plane"},
		{"control_plane = \"http://x\"\n" + login, "token_file"},
		{runnerBase, "at least one"},
		{runnerBase + "slots = -1\n" + login, "slots"},
		{runnerBase + "[credentials.gemini]\nlogin = true\n", "harness must be"},
		{runnerBase + "[credentials.claude]\n", "exactly one"},
		{runnerBase + "[credentials.claude]\nlogin = true\nenv = [\"ANTHROPIC_API_KEY\"]\n", "exactly one"},
		{runnerBase + "[credentials.claude]\nenv = [\"BAD=NAME\"]\n", "invalid environment variable"},
		{runnerBase + login + "[kubernetes]\nsecret = \"s\"\n", "do not support login"},
		{runnerBase + "slots = 2\n[credentials.codex]\nlogin = true\n", "slots = 1"},
	} {
		if _, _, err := loadRunnerBody(t, test.body); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%q: error = %v, want %q", test.body, err, test.want)
		}
	}
}
