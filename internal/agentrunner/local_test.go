package agentrunner

import (
	"context"
	"encoding/json"
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

func newLocal(t *testing.T, runnerConfig config.Runner) (*Local, string) {
	t.Helper()
	runnerConfig.DataDirectory = t.TempDir()
	runnerConfig.Slots = 1
	t.Setenv("GH_TOKEN", "fixture-github")
	local, err := NewLocal(runnerConfig)
	if err != nil {
		t.Fatal(err)
	}
	return local, runnerConfig.DataDirectory
}

func localRequest(steps ...runner.Step) runner.RunRequest {
	return runner.RunRequest{
		RunID:         localRun,
		TicketID:      "t_0123456789abcdef",
		Preset:        "default",
		Repository:    "owner/repo",
		Branch:        "machinist/t_0123456789abcdef",
		Vars:          map[string]string{"repo": "owner/repo"},
		Prompt:        "THE PROMPT",
		Steps:         steps,
		TimeoutMillis: time.Minute.Milliseconds(),
		GitName:       "bot",
		GitEmail:      "bot@example.test",
	}
}

func TestNewLocalRequiresEveryVariableItPasses(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	cfg := config.Runner{Env: []string{"MACHINIST_TEST_KEY"}}
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

func TestLocalStepsGetAPrivateHomeAndOnlyAllowedEnvironment(t *testing.T) {
	output := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "fixture-anthropic")
	t.Setenv("MACHINIST_PARENT_SECRET", "must-not-leak")
	local, data := newLocal(t, config.Runner{Env: []string{"ANTHROPIC_API_KEY"}})
	result := local.Execute(t.Context(), localRequest(runner.Step{Name: "look", Run: "env > " + output + "/env; cat > " + output + "/prompt; pwd > " + output + "/pwd"}))
	if result.State != runner.StateSucceeded || result.Report == nil || result.Report.Summary != "Every step passed." {
		t.Fatalf("result = %+v", result)
	}
	environment, err := os.ReadFile(filepath.Join(output, "env"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(data, "work", localRun)
	for _, want := range []string{"ANTHROPIC_API_KEY=fixture-anthropic", "GH_TOKEN=fixture-github", "HOME=" + filepath.Join(root, "home"), "MACHINIST_REPO=owner/repo", "MACHINIST_BRANCH=machinist/t_0123456789abcdef", "MACHINIST_JOB_ID=t_0123456789abcdef", "GIT_AUTHOR_NAME=bot"} {
		if !strings.Contains(string(environment), want) {
			t.Fatalf("missing %s in:\n%s", want, environment)
		}
	}
	for _, unwanted := range []string{"must-not-leak"} {
		if strings.Contains(string(environment), unwanted) {
			t.Fatalf("%s reached the step:\n%s", unwanted, environment)
		}
	}
	if prompt, _ := os.ReadFile(filepath.Join(output, "prompt")); string(prompt) != "THE PROMPT" {
		t.Fatalf("prompt = %q", prompt)
	}
	if pwd, _ := os.ReadFile(filepath.Join(output, "pwd")); !strings.HasSuffix(strings.TrimSpace(string(pwd)), filepath.Join(localRun, "workspace")) {
		t.Fatalf("pwd = %q", pwd)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("workspace and private home were not removed: %v", err)
	}
}

func TestLocalLoginUsesTheUsersHome(t *testing.T) {
	output := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-pass")
	local, _ := newLocal(t, config.Runner{Login: true})
	if result := local.Execute(t.Context(), localRequest(runner.Step{Name: "look", Run: "env > " + output + "/env"})); result.State != runner.StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
	environment, _ := os.ReadFile(filepath.Join(output, "env"))
	if !strings.Contains(string(environment), "HOME="+home+"\n") || strings.Contains(string(environment), "must-not-pass") {
		t.Fatalf("environment:\n%s", environment)
	}
}

func TestLocalReportsSetupFailures(t *testing.T) {
	local, _ := newLocal(t, config.Runner{})
	for name, change := range map[string]func(*runner.RunRequest){
		"bad run id": func(r *runner.RunRequest) { r.RunID = "../escape" },
		"no steps":   func(r *runner.RunRequest) { r.Steps = nil },
		"bad var":    func(r *runner.RunRequest) { r.Vars = map[string]string{"Bad-Name": "x"} },
	} {
		t.Run(name, func(t *testing.T) {
			request := localRequest(runner.Step{Name: "ok", Run: "true"})
			change(&request)
			if result := local.Execute(t.Context(), request); result.State != runner.StateFailed || result.Error == "" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestLocalCancelledRunIsCancelled(t *testing.T) {
	local, _ := newLocal(t, config.Runner{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result := local.Execute(ctx, localRequest(runner.Step{Name: "wait", Run: "sleep 30"})); result.State != runner.StateCancelled {
		t.Fatalf("result = %+v", result)
	}
}

func TestLocalWritesOneEventLogForAllSteps(t *testing.T) {
	local, data := newLocal(t, config.Runner{})
	result := local.Execute(t.Context(), localRequest(
		runner.Step{Name: "first", Run: "echo one"},
		runner.Step{Name: "second", Run: "echo two"},
	))
	if result.State != runner.StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
	body, err := os.ReadFile(filepath.Join(data, "runs", localRun, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	started := []string{}
	for index, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var event runner.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Sequence != int64(index+1) {
			t.Fatalf("event %d has sequence %d", index+1, event.Sequence)
		}
		if event.Type == "step.started" {
			started = append(started, event.Message)
		}
	}
	if got := strings.Join(started, ", "); got != "name=first, name=second" {
		t.Fatalf("step events = %s", got)
	}
}

func gitCommand(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=F", "GIT_AUTHOR_EMAIL=f@example.test", "GIT_COMMITTER_NAME=F", "GIT_COMMITTER_EMAIL=f@example.test")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

// bareRemote is a repository a step can clone and push to.
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
