package agentrunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
)

const localRun = "run_0123456789abcdef01234567"

func gitCommand(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@example.test", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@example.test")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func bareRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	gitCommand(t, root, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed")
	gitCommand(t, root, "init", "--quiet", "--initial-branch=main", seed)
	gitCommand(t, seed, "commit", "--quiet", "--allow-empty", "-m", "init")
	gitCommand(t, seed, "push", "--quiet", remote, "main")
	return remote
}

// fakeClaude installs a claude executable that records what it was given.
func fakeClaude(t *testing.T, report string) string {
	t.Helper()
	bin := t.TempDir()
	output := t.TempDir()
	script := "#!/bin/sh\nenv > " + output + "/env\ncat > " + output + "/prompt\necho $HOME > " + output + "/home\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"apiKeySource\":\"ANTHROPIC_API_KEY\"}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"is_error\":false,\"result\":\"done\",\"total_cost_usd\":0.25,\"usage\":{\"input_tokens\":3,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0,\"output_tokens\":4},\"structured_output\":" + report + "}'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return output
}

func newLocal(t *testing.T, credential config.Credential) (*Local, string) {
	t.Helper()
	data := t.TempDir()
	t.Setenv("GH_TOKEN", "fixture-github")
	local, err := NewLocal(config.Runner{DataDirectory: data, Slots: 1, Credentials: map[string]config.Credential{"claude": credential}})
	if err != nil {
		t.Fatal(err)
	}
	remote := bareRemote(t)
	local.CloneURL = func(string) string { return remote }
	return local, data
}

func localRequest() runner.RunRequest {
	return runner.RunRequest{RunID: localRun, Repository: "owner/repo", Branch: "machinist/t_0123456789abcdef", Prompt: "THE PROMPT", Harness: "claude", TimeoutMillis: time.Minute.Milliseconds(), GitName: "bot", GitEmail: "bot@example.test"}
}

func TestNewLocalRequiresCredentialsToBePresent(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	cfg := config.Runner{Credentials: map[string]config.Credential{"claude": {Env: []string{"MACHINIST_TEST_KEY"}}}}
	if _, err := NewLocal(cfg); err == nil || !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Fatalf("error = %v", err)
	}
	t.Setenv("GH_TOKEN", "x")
	if _, err := NewLocal(cfg); err == nil || !strings.Contains(err.Error(), "MACHINIST_TEST_KEY") {
		t.Fatalf("error = %v", err)
	}
	t.Setenv("MACHINIST_TEST_KEY", "y")
	if _, err := NewLocal(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRunsAgentWithPrivateHomeAndOnlyAllowedEnvironment(t *testing.T) {
	output := fakeClaude(t, `{"outcome":"no_change","summary":"Finished"}`)
	t.Setenv("ANTHROPIC_API_KEY", "fixture-anthropic")
	t.Setenv("MACHINIST_PARENT_SECRET", "must-not-leak")
	local, data := newLocal(t, config.Credential{Env: []string{"ANTHROPIC_API_KEY"}})
	result := local.Execute(t.Context(), localRequest())
	if result.State != runner.StateSucceeded || result.Report == nil || result.Report.Summary != "Finished" {
		t.Fatalf("result = %+v", result)
	}
	if *result.Cost != 0.25 || result.Tokens == nil || result.CredentialSource != "ANTHROPIC_API_KEY" {
		t.Fatalf("result = %+v", result)
	}
	environment, err := os.ReadFile(filepath.Join(output, "env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ANTHROPIC_API_KEY=fixture-anthropic", "GH_TOKEN=fixture-github", "HOME=" + filepath.Join(data, "work", localRun, "home")} {
		if !strings.Contains(string(environment), want) {
			t.Fatalf("missing %s in:\n%s", want, environment)
		}
	}
	if strings.Contains(string(environment), "must-not-leak") {
		t.Fatalf("parent environment leaked:\n%s", environment)
	}
	if prompt, _ := os.ReadFile(filepath.Join(output, "prompt")); string(prompt) != "THE PROMPT" {
		t.Fatalf("prompt = %q", prompt)
	}
	if _, err := os.Stat(filepath.Join(data, "work", localRun)); !os.IsNotExist(err) {
		t.Fatalf("clone and private home were not removed: %v", err)
	}
}

func TestLocalLoginUsesNormalHomeAndNoKey(t *testing.T) {
	output := fakeClaude(t, `{"outcome":"no_change","summary":"Finished"}`)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-pass")
	local, _ := newLocal(t, config.Credential{Login: true})
	if result := local.Execute(t.Context(), localRequest()); result.State != runner.StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(output, "home")); strings.TrimSpace(string(got)) != home {
		t.Fatalf("home = %q", got)
	}
	if environment, _ := os.ReadFile(filepath.Join(output, "env")); strings.Contains(string(environment), "must-not-pass") {
		t.Fatalf("key reached a login run:\n%s", environment)
	}
}

func TestLocalReportsSetupFailures(t *testing.T) {
	local, _ := newLocal(t, config.Credential{Login: true})
	for name, change := range map[string]func(*runner.RunRequest){
		"bad run id":     func(r *runner.RunRequest) { r.RunID = "../escape" },
		"no credential":  func(r *runner.RunRequest) { r.Harness = "codex" },
		"bad repository": func(r *runner.RunRequest) { r.Repository = "not a repo" },
	} {
		t.Run(name, func(t *testing.T) {
			request := localRequest()
			change(&request)
			if result := local.Execute(t.Context(), request); result.State != runner.StateFailed || result.Error == "" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestLocalCloneFailureIsAFailure(t *testing.T) {
	local, _ := newLocal(t, config.Credential{Login: true})
	local.CloneURL = func(string) string { return filepath.Join(t.TempDir(), "missing.git") }
	if result := local.Execute(t.Context(), localRequest()); result.State != runner.StateFailed || !strings.Contains(result.Error, "git clone") {
		t.Fatalf("result = %+v", result)
	}
}

func TestLocalCancelledDuringSetupIsCancelled(t *testing.T) {
	local, _ := newLocal(t, config.Credential{Login: true})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result := local.Execute(ctx, localRequest()); result.State != runner.StateCancelled {
		t.Fatalf("result = %+v", result)
	}
}

func TestLocalMalformedReportFailsWithReason(t *testing.T) {
	fakeClaude(t, `{"outcome":"complete","summary":"old format"}`)
	local, _ := newLocal(t, config.Credential{Login: true})
	result := local.Execute(t.Context(), localRequest())
	if result.State != runner.StateFailed || result.Report != nil || !strings.Contains(result.Error, "report outcome") {
		t.Fatalf("result = %+v", result)
	}
}
