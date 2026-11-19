package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

const testRunID = "run_0123456789abcdef01234567"

func stepsRequest(steps ...Step) RunRequest {
	return RunRequest{
		RunID:         testRunID,
		TicketID:      "t_1",
		Preset:        "coding",
		Repository:    "owner/repo",
		Branch:        "machinist/t_1",
		Vars:          map[string]string{"repo": "owner/repo"},
		Prompt:        "THE PROMPT",
		Steps:         steps,
		TimeoutMillis: time.Minute.Milliseconds(),
		GitName:       "machinist-bot",
		GitEmail:      "machinist-bot@users.noreply.github.com",
	}
}

func prepare(t *testing.T, request RunRequest) Workspace {
	t.Helper()
	root := t.TempDir()
	workspace, err := PrepareWorkspace(WorkspaceOptions{Request: request, Root: root, GitHubToken: "token", Environment: []string{"PATH=" + os.Getenv("PATH")}, Home: filepath.Join(root, "home")})
	if err != nil {
		t.Fatal(err)
	}
	return workspace
}

func run(t *testing.T, request RunRequest) (RunResult, Workspace) {
	t.Helper()
	workspace := prepare(t, request)
	return RunSteps(t.Context(), request, workspace, t.TempDir()), workspace
}

func TestPrepareWorkspaceIsEmptyAndThePromptIsBesideIt(t *testing.T) {
	workspace := prepare(t, stepsRequest())
	entries, err := os.ReadDir(workspace.Directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %v, err = %v", entries, err)
	}
	prompt, err := os.ReadFile(filepath.Join(workspace.Control, "prompt.md"))
	if err != nil || string(prompt) != "THE PROMPT" {
		t.Fatalf("prompt = %q, err = %v", prompt, err)
	}
}

func TestStepsShareTheWorkspaceAndSeeTheJob(t *testing.T) {
	result, workspace := run(t, stepsRequest(
		Step{Name: "plan", Run: `printf '%s|%s|%s|%s|%s' "$MACHINIST_JOB_ID" "$MACHINIST_PRESET" "$MACHINIST_REPO" "$MACHINIST_BRANCH" "$GIT_AUTHOR_NAME" > PLAN.md`},
		Step{Name: "build", Run: `cat PLAN.md > out; printf '|' >> out; cat >> out`},
	))
	if result.State != StateSucceeded || result.Report == nil || result.Report.Outcome != protocol.OutcomeNoChange || result.Report.Summary != defaultSummary {
		t.Fatalf("result = %+v", result)
	}
	out, err := os.ReadFile(filepath.Join(workspace.Directory, "out"))
	if err != nil || string(out) != "t_1|coding|owner/repo|machinist/t_1|machinist-bot|THE PROMPT" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if len(result.Stages) != 2 || result.Stages[0].State != string(StateSucceeded) || result.Stages[1].Kind != "step" {
		t.Fatalf("stages = %+v", result.Stages)
	}
}

func TestStepsRunFactoryScriptsByNameAndReadItsPrompts(t *testing.T) {
	request := stepsRequest(Step{Name: "greet", Run: `greet.sh world > out`})
	request.Files = map[string]string{
		"scripts/greet.sh":    "#!/bin/sh\nprintf '%s, %s' \"$(cat \"$MACHINIST_DIR/prompts/greeting.md\")\" \"$1\"\n",
		"prompts/greeting.md": "Hello",
	}
	result, workspace := run(t, request)
	if result.State != StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
	out, err := os.ReadFile(filepath.Join(workspace.Directory, "out"))
	if err != nil || string(out) != "Hello, world" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	info, err := os.Stat(filepath.Join(workspace.Control, "prompts", "greeting.md"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("prompt = %v, err = %v", info, err)
	}
}

func TestPrepareWorkspaceRejectsFactoryFilesOutsideItsDirectories(t *testing.T) {
	for _, path := range []string{"../escape.sh", "scripts/../../escape.sh", "other/file", "scripts/nested/file", "prompts/"} {
		request := stepsRequest()
		request.Files = map[string]string{path: "x"}
		root := t.TempDir()
		if _, err := PrepareWorkspace(WorkspaceOptions{Request: request, Root: root, Home: filepath.Join(root, "home")}); err == nil || !strings.Contains(err.Error(), "invalid factory file") {
			t.Fatalf("%q: error = %v", path, err)
		}
	}
}

func TestResultFileIsTheSummary(t *testing.T) {
	result, _ := run(t, stepsRequest(Step{Name: "ship", Run: `printf 'Opened https://example.test/pull/1\n' > "$MACHINIST_DIR/result.md"`}))
	if result.Report == nil || result.Report.Summary != "Opened https://example.test/pull/1" {
		t.Fatalf("result = %+v", result)
	}
}

func TestFailingStepStopsTheRun(t *testing.T) {
	result, workspace := run(t, stepsRequest(
		Step{Name: "test", Run: "exit 3"},
		Step{Name: "ship", Run: "touch shipped"},
	))
	if result.State != StateFailed || result.Report != nil || !strings.Contains(result.Error, "step test: command exited with status 3") {
		t.Fatalf("result = %+v", result)
	}
	if result.Stages[1].State != protocol.StageSkipped {
		t.Fatalf("stages = %+v", result.Stages)
	}
	if _, err := os.Stat(filepath.Join(workspace.Directory, "shipped")); !os.IsNotExist(err) {
		t.Fatal("a step after the failure ran")
	}
}

func TestAFailingCommandFailsItsStep(t *testing.T) {
	result, workspace := run(t, stepsRequest(Step{Name: "test", Run: "false\ntouch after"}))
	if result.State != StateFailed || !strings.Contains(result.Error, "step test: command exited with status 1") {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(workspace.Directory, "after")); !os.IsNotExist(err) {
		t.Fatal("the step went on after a failing command")
	}
}

func TestAStepMayIgnoreThePrompt(t *testing.T) {
	request := stepsRequest(Step{Name: "skip", Run: "true"}, Step{Name: "again", Run: "true"})
	request.Prompt = strings.Repeat("a long prompt ", 1<<16)
	if result, _ := run(t, request); result.State != StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
}

func TestAQuestionFileDoesNotStopTheRun(t *testing.T) {
	result, _ := run(t, stepsRequest(
		Step{Name: "plan", Run: `printf 'Which database?\n' > "$MACHINIST_DIR/result.md"`},
		Step{Name: "build", Run: `printf 'Built.' > "$MACHINIST_DIR/result.md"`},
	))
	if result.State != StateSucceeded || result.Report == nil || result.Report.Summary != "Built." || result.Stages[1].State != string(StateSucceeded) {
		t.Fatalf("result = %+v", result)
	}
}

func TestStepTimeoutAndRunTimeout(t *testing.T) {
	step := Step{Name: "slow", Run: "sleep 30", TimeoutMillis: 200}
	result, _ := run(t, stepsRequest(step))
	if result.State != StateTimedOut || result.Stages[0].State != string(StateTimedOut) {
		t.Fatalf("result = %+v", result)
	}
	request := stepsRequest(Step{Name: "slow", Run: "sleep 30"})
	request.TimeoutMillis = 200
	if result, _ := run(t, request); result.State != StateTimedOut {
		t.Fatalf("result = %+v", result)
	}
}

func TestControlFilesMustBeSmallRegularFiles(t *testing.T) {
	result, _ := run(t, stepsRequest(Step{Name: "link", Run: `ln -s /etc/passwd "$MACHINIST_DIR/result.md"`}))
	if result.State != StateFailed || !strings.Contains(result.Error, "not a regular file") {
		t.Fatalf("result = %+v", result)
	}
	result, _ = run(t, stepsRequest(Step{Name: "big", Run: `head -c 70000 /dev/zero | tr '\0' x > "$MACHINIST_DIR/result.md"`}))
	if result.State != StateFailed || !strings.Contains(result.Error, "larger than") {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunStepsRejectsNoStepsAndBadVars(t *testing.T) {
	if result, _ := run(t, stepsRequest()); result.State != StateFailed || result.Error != "the preset has no steps" {
		t.Fatalf("result = %+v", result)
	}
	request := stepsRequest(Step{Name: "x", Run: "true"})
	request.Vars = map[string]string{"PATH": "/evil"}
	root := t.TempDir()
	if _, err := PrepareWorkspace(WorkspaceOptions{Request: request, Root: root, GitHubToken: "token", Home: filepath.Join(root, "home")}); err == nil {
		t.Fatal("bad var name accepted")
	}
}

func TestEachStepRecordsTheUsageItsAgentReported(t *testing.T) {
	result, _ := run(t, stepsRequest(
		Step{Name: "plan", Run: `printf '{"tokens":100,"cost":0.25}' > "$MACHINIST_DIR/usage.json"`},
		Step{Name: "build", Run: `true`},
		Step{Name: "review", Run: `printf '{"tokens":40,"cost":0.05}' > "$MACHINIST_DIR/usage.json"`},
	))
	if result.State != StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
	plan, build, review := result.Stages[0], result.Stages[1], result.Stages[2]
	if plan.Tokens == nil || *plan.Tokens != 100 || *plan.Cost != 0.25 || build.Tokens != nil || build.Cost != nil || *review.Tokens != 40 {
		t.Fatalf("stages = %+v", result.Stages)
	}
	tokens, cost := protocol.Totals(result.Stages)
	if *tokens != 140 || *cost != 0.3 {
		t.Fatalf("totals = %d, %v", *tokens, *cost)
	}
}

func TestStepsCanCallTheRunningMachinist(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, _ := run(t, stepsRequest(Step{Name: "path", Run: `case ":$PATH:" in *":` + filepath.Dir(executable) + `:"*) exit 0;; esac; exit 1`}))
	if result.State != StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
}
