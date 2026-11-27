package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const runnerBase = "control_plane: http://10.0.0.5:7331\ntoken_file: runner.token\n"

func loadRunnerBody(t *testing.T, body string) (Runner, string, error) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "runner.yaml")
	writeTestFile(t, path, body)
	runner, err := LoadRunner(path)
	return runner, directory, err
}

func TestLoadRunnerResolvesPathsAndListsHarnesses(t *testing.T) {
	runner, directory, err := loadRunnerBody(t, runnerBase+`data_directory: data
slots: 2
env: [ANTHROPIC_API_KEY, OPENAI_API_KEY]

kubernetes:
  kubeconfig: cluster/config
  secret: agent-credentials
  allowed_hosts: [proxy.golang.org]
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
	runner, directory, err := loadRunnerBody(t, "control_plane: http://10.0.0.5:7331\n")
	if err != nil {
		t.Fatal(err)
	}
	if runner.Slots != 1 || runner.DataDirectory != filepath.Join(directory, ".machinist", "runner") || runner.TokenFile != filepath.Join(directory, ".machinist", "runner.token") {
		t.Fatalf("runner = %+v", runner)
	}
}

func TestRunnerTokenReadsTrimmedFile(t *testing.T) {
	runner, directory, err := loadRunnerBody(t, runnerBase+"login: true\n")
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
		{runnerBase + "executors:\n  codex:\n    command: [codex]\n", "executors"},
		{runnerBase + "repositories:\n  app:\n    path: /srv/app\n", "repositories"},
		{runnerBase + "name: worker\n", "name on line 3"},
		{runnerBase + "kubernetes:\n  auth_secret: old\n", "auth_secret on line 4"},
		{runnerBase + "credentials:\n  claude:\n    login: true\n", "credentials"},
	} {
		if _, _, err := loadRunnerBody(t, test.body); err == nil || !strings.Contains(err.Error(), "unknown key "+test.key) {
			t.Fatalf("%q: error = %v", test.body, err)
		}
	}
}

func TestLoadRunnerRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct{ body, want string }{
		{"token_file: t\n", "control_plane"},
		{"control_plane: 10.0.0.5:7331\ntoken_file: t\n", "control_plane"},
		{runnerBase + "slots: -1\n", "slots"},
		{runnerBase + "env: [BAD=NAME]\n", "invalid environment variable"},
		{runnerBase + "login: true\nkubernetes:\n  secret: s\n", "do not support login"},
		{runnerBase + "login: true\ndocker:\n  image: ghcr.io/owainlewis/machinist-runner:latest\n", "do not support login"},
		{runnerBase + "kubernetes:\n  secret: s\ndocker:\n  image: ghcr.io/owainlewis/machinist-runner:latest\n", "cannot configure both docker and kubernetes"},
		{runnerBase + "docker: {}\n", "image is required"},
		{runnerBase + "docker:\n  image: --evil\n", "invalid image"},
		{runnerBase + "docker:\n  image: img:latest\n  runtime: --bad\n", "invalid runtime"},
		{runnerBase + "docker:\n  image: img:latest\n  network: \"bad net\"\n", "invalid network"},
		{runnerBase + "docker:\n  image: img:latest\n  proxy_url: ftp://proxy:3128\n", "proxy_url"},
		{runnerBase + "docker:\n  image: img:latest\n  memory: 4tb\n", "invalid memory limit"},
		{runnerBase + "docker:\n  image: img:latest\n  cpus: -1\n", "cpus must be positive"},
		{runnerBase + "docker:\n  image: img:latest\n  pids_limit: -5\n", "pids_limit must be positive"},
		{runnerBase + "docker:\n  image: img:latest\n  env: [BAD=VAR]\n", "invalid environment variable"},
		{runnerBase + "docker:\n  image: img:latest\n  harnesses: [unknown]\n", "unknown harness"},
	} {
		if _, _, err := loadRunnerBody(t, test.body); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%q: error = %v, want %q", test.body, err, test.want)
		}
	}
}

func TestLoadRunnerDockerDefaultsAndOptions(t *testing.T) {
	runner, _, err := loadRunnerBody(t, runnerBase+`docker:
  image: ghcr.io/owainlewis/machinist-runner:latest
`)
	if err != nil {
		t.Fatal(err)
	}
	if runner.Kind() != "docker" {
		t.Fatalf("kind = %q, want docker", runner.Kind())
	}
	if runner.Docker.Memory != "4g" || runner.Docker.CPUs != 2 || runner.Docker.PIDsLimit != 512 || runner.Docker.Runtime != "" {
		t.Fatalf("docker defaults = %+v", runner.Docker)
	}

	gvisor, _, err := loadRunnerBody(t, runnerBase+`env: [ANTHROPIC_API_KEY]
docker:
  image: ghcr.io/owainlewis/machinist-runner:latest
  runtime: runsc
  network: machinist-egress
  proxy_url: http://egress:3128
  memory: 8g
  cpus: 4.5
  pids_limit: 1024
  env: [ANTHROPIC_API_KEY, OPENAI_API_KEY]
  harnesses: [codex, claude, codex]
`)
	if err != nil {
		t.Fatal(err)
	}
	if gvisor.Kind() != "docker (runsc)" {
		t.Fatalf("kind = %q, want docker (runsc)", gvisor.Kind())
	}
	if gvisor.Docker.Memory != "8g" || gvisor.Docker.CPUs != 4.5 || gvisor.Docker.PIDsLimit != 1024 || gvisor.Docker.Network != "machinist-egress" || gvisor.Docker.ProxyURL != "http://egress:3128" {
		t.Fatalf("docker = %+v", gvisor.Docker)
	}
	if got := strings.Join(gvisor.EffectiveEnv(), ","); got != "ANTHROPIC_API_KEY,OPENAI_API_KEY" {
		t.Fatalf("effective env = %q", got)
	}
	if got := strings.Join(gvisor.Docker.Harnesses, ","); got != "claude,codex" {
		t.Fatalf("harnesses = %q", got)
	}
}

func TestLoadRunnerNormalizesAndValidatesLabels(t *testing.T) {
	runner, _, err := loadRunnerBody(t, runnerBase+"labels: [\"  MacOS-15 \", gpu, macos-15, x86_64.v2]\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(runner.Labels, ","); got != "gpu,macos-15,x86_64.v2" {
		t.Fatalf("labels = %q, want gpu,macos-15,x86_64.v2", got)
	}

	empty, _, err := loadRunnerBody(t, runnerBase+"labels: []\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Labels) != 0 {
		t.Fatalf("empty labels = %v", empty.Labels)
	}

	for _, bad := range []string{
		"labels: [\"\"]\n",
		"labels: [\"   \"]\n",
		"labels: [-leading-dash]\n",
		"labels: [.leading-dot]\n",
		"labels: [_leading_under]\n",
		"labels: [\"has space\"]\n",
		"labels: [\"bad/slash\"]\n",
		"labels: [\"bad,comma\"]\n",
	} {
		if _, _, err := loadRunnerBody(t, runnerBase+bad); err == nil || !strings.Contains(err.Error(), "invalid label") {
			t.Fatalf("%q: err = %v, want invalid label", bad, err)
		}
	}
}
