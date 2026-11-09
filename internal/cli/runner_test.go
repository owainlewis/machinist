package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeConfigFile(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeStartConfig(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	writeConfigFile(t, directory, "factory/agents/default/agent.toml", "harness = \"claude\"\n")
	writeConfigFile(t, directory, "factory/agents/default/playbook.md", "Do the work.\n")
	return writeConfigFile(t, directory, "config.toml", "[server]\nlisten = \"127.0.0.1:0\"\ndatabase = \"tickets.db\"\nfactory = \"factory\"\n\n[github]\nhandle = \"machinist-bot\"\n")
}

type cancelOnListen struct {
	mu     sync.Mutex
	body   bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnListen) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.body.Write(body)
	if strings.Contains(w.body.String(), "listening on") {
		w.cancel()
	}
	return len(body), nil
}

func TestStartReportsListeningAfterBindAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	stderr := &cancelOnListen{cancel: cancel}
	exitCode := Execute(ctx, []string{"start", "--config=" + writeStartConfig(t)}, strings.NewReader(""), &bytes.Buffer{}, stderr, "test")
	if exitCode != 0 || !strings.Contains(stderr.body.String(), "control plane listening on 127.0.0.1:") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.body.String())
	}
}

func TestStartRejectsInvalidFactory(t *testing.T) {
	directory := t.TempDir()
	path := writeConfigFile(t, directory, "config.toml", "[server]\nfactory = \"missing\"\ndatabase = \"tickets.db\"\n\n[github]\nhandle = \"bot\"\n")
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "load factory") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

const runnerConfigBody = "control_plane = \"http://127.0.0.1:1\"\ntoken_file = \"runner.token\"\n\n[credentials.claude]\nlogin = true\n"

func TestRunnerRefusesKubernetesForNow(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "runner.toml", "control_plane = \"http://127.0.0.1:1\"\ntoken_file = \"runner.token\"\n\n[credentials.claude]\nenv = [\"ANTHROPIC_API_KEY\"]\n\n[kubernetes]\nsecret = \"s\"\n")
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"runner", "start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "Kubernetes runners") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunnerStartRequiresGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	path := writeConfigFile(t, t.TempDir(), "runner.toml", runnerConfigBody)
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"runner", "start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "GH_TOKEN") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunnerStartReadsTokenFileAndStopsOnCancellation(t *testing.T) {
	t.Setenv("GH_TOKEN", "fixture")
	directory := t.TempDir()
	path := writeConfigFile(t, directory, "runner.toml", runnerConfigBody+"\n")
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"runner", "start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "runner.token") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	writeConfigFile(t, directory, "runner.token", "token\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stderr.Reset()
	if exitCode := Execute(ctx, []string{"runner", "start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}

func TestRunnerCheckRequiresRepository(t *testing.T) {
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"runner", "check"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "repo") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
}
