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

	"github.com/owainlewis/machinist/internal/fakegh"
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
	writeConfigFile(t, directory, "factory/presets/default.toml", "[[steps]]\nname = \"work\"\nrun = \"true\"\n")
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
	fakegh.Install(t, "machinist-bot")
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

const runnerConfigBody = "control_plane = \"http://127.0.0.1:1\"\ntoken_file = \"runner.token\"\nlogin = true\n"

func TestKubernetesRunnerValidatesItsConfiguration(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "runner.toml", "control_plane = \"http://127.0.0.1:1\"\ntoken_file = \"runner.token\"\nenv = [\"ANTHROPIC_API_KEY\"]\n\n[kubernetes]\nsecret = \"s\"\n")
	var stderr bytes.Buffer
	if exitCode := Execute(t.Context(), []string{"runner", "start", "--config=" + path}, strings.NewReader(""), &bytes.Buffer{}, &stderr, "test"); exitCode != 2 || !strings.Contains(stderr.String(), "explicit context") {
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

// cancelOnConnect lets runner start finish without contacting a control plane.
type cancelOnConnect struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnConnect) Write(body []byte) (int, error) {
	n, err := w.Buffer.Write(body)
	if strings.Contains(w.String(), "runner connecting to") {
		w.cancel()
	}
	return n, err
}

func TestRunnerTokenScopeWarning(t *testing.T) {
	for _, command := range []string{"check", "start"} {
		for _, test := range []struct{ name, scopes, want string }{
			{"wide", "repo, delete_repo, admin:public_key", "machinist: warning: the GitHub token has extra scopes: delete_repo, admin:public_key; use a token limited to the repositories the bot works on\n"},
			{"narrow", "repo", ""},
			{"public", "public_repo", ""},
			{"fine-grained", "", ""},
			{"lookup-failure", "", ""},
		} {
			t.Run(command+"/"+test.name, func(t *testing.T) {
				github := fakegh.Install(t, "bot")
				if test.scopes != "" {
					github.SetScopes(test.scopes)
				}
				token := "ghp_secretNeverPrintThis"
				t.Setenv("GH_TOKEN", token)
				if test.name == "lookup-failure" {
					github.Fail("bad credentials: " + token)
				}
				directory := t.TempDir()
				path := writeConfigFile(t, directory, "runner.toml", runnerConfigBody)
				writeConfigFile(t, directory, "runner.token", "runner-token\n")
				// Fail git locally so check cannot reach GitHub.
				git := writeConfigFile(t, directory, "bin/git", "#!/bin/sh\nexit 1\n")
				if err := os.Chmod(git, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", filepath.Dir(git)+string(os.PathListSeparator)+os.Getenv("PATH"))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				stderr := &cancelOnConnect{cancel: cancel}
				var stdout bytes.Buffer
				args := []string{"runner", command, "--config=" + path}
				if command == "check" {
					args = append(args, "--repo=owner/repo")
				}
				exitCode := Execute(ctx, args, strings.NewReader(""), &stdout, stderr, "test")
				if command == "start" && exitCode != 0 {
					t.Fatalf("exit = %d, stderr = %s", exitCode, stderr.String())
				}
				var warnings string
				for _, line := range strings.Split(stderr.String(), "\n") {
					if strings.Contains(line, "warning:") {
						warnings += line + "\n"
					}
				}
				if warnings != test.want {
					t.Fatalf("warnings = %q, want %q", warnings, test.want)
				}
				if strings.Contains(stderr.String()+stdout.String(), token) {
					t.Fatal("token printed")
				}
			})
		}
	}
}
