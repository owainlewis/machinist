package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

func TestAgentBuildsItsTurnFromTheControlDirectory(t *testing.T) {
	control := t.TempDir()
	writeControl := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(control, path)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(control, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeControl("agents/review.md", "---\nharness: codex\n---\nReview the branch.\n")
	writeControl("prompts/rules.md", "Run headless.\n")
	writeControl("prompt.md", "Fix the toast.")
	t.Setenv("MACHINIST_REPO", "owner/repo")
	t.Setenv("MACHINIST_BRANCH", "machinist/7")
	t.Setenv("MACHINIST_HARNESS", "")
	t.Setenv("MACHINIST_MODEL", "")

	system, task, agent, err := agentTurn(control, "review")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Harness != "codex" || task != "Fix the toast." {
		t.Fatalf("agent = %+v, task = %q", agent, task)
	}
	want := "You are working in a clone of owner/repo on branch machinist/7.\nThe control directory is " + control + ". Files named below without a path are there.\n\nRun headless.\n\nReview the branch."
	if system != want {
		t.Fatalf("system =\n%s\nwant\n%s", system, want)
	}
	if _, _, _, err := agentTurn(control, "missing"); err == nil || !strings.Contains(err.Error(), "agent missing is not in the factory's agents/") {
		t.Fatalf("missing agent error = %v", err)
	}
}

func TestAgentNeedsAStep(t *testing.T) {
	t.Setenv("MACHINIST_DIR", "")
	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"agent", "implement"}, strings.NewReader(""), &stdout, &stderr, "test"); code == 0 || !strings.Contains(stderr.String(), "runs inside a step") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
}

func TestAgentRespectsHarnessAndModelEnvOverrides(t *testing.T) {
	control := t.TempDir()
	for path, body := range map[string]string{
		"agents/review.md": "---\nharness: claude\nmodel: sonnet\ncommand: /opt/claude\n---\nReview the branch.\n",
		"prompt.md":        "Fix the toast.",
	} {
		full := filepath.Join(control, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("MACHINIST_HARNESS", "agy")
	t.Setenv("MACHINIST_MODEL", "gemini-4-neon-high")
	_, _, agent, err := agentTurn(control, "review")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Harness != "agy" || agent.Model != "gemini-4-neon-high" || agent.Program != "" {
		t.Fatalf("agent = %+v", agent)
	}

	// Harness override without model clears the agent file's model and custom program.
	t.Setenv("MACHINIST_HARNESS", "codex")
	t.Setenv("MACHINIST_MODEL", "")
	_, _, agent, err = agentTurn(control, "review")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Harness != "codex" || agent.Model != "" || agent.Program != "" {
		t.Fatalf("agent = %+v", agent)
	}

	// Model-only override keeps the agent file's harness and command.
	t.Setenv("MACHINIST_HARNESS", "")
	t.Setenv("MACHINIST_MODEL", "opus")
	_, _, agent, err = agentTurn(control, "review")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Harness != "claude" || agent.Model != "opus" || agent.Program != "/opt/claude" {
		t.Fatalf("agent = %+v", agent)
	}

	// Invalid harness override fails clearly.
	t.Setenv("MACHINIST_HARNESS", "bogus")
	t.Setenv("MACHINIST_MODEL", "")
	if _, _, _, err := agentTurn(control, "review"); err == nil || !strings.Contains(err.Error(), "MACHINIST_HARNESS") {
		t.Fatalf("error = %v", err)
	}
}

// TestMachinistAgentHelper lets TestRunnerShellScriptsRespectHarnessAndModelOverrides
// invoke the real machinist CLI from inside shell scripts without building a
// separate binary.
func TestMachinistAgentHelper(t *testing.T) {
	if os.Getenv("MACHINIST_TEST_CLI") != "1" {
		return
	}
	sep := slices.Index(os.Args, "--")
	if sep < 0 {
		os.Exit(2)
	}
	os.Exit(Execute(t.Context(), os.Args[sep+1:], os.Stdin, os.Stdout, os.Stderr, "test"))
}

func TestRunnerShellScriptsRespectHarnessAndModelOverrides(t *testing.T) {
	binDir := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "invocations.log")
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	writeBin := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// Real machinist agent + verdict via the test binary, plus a stub push for publish.sh.
	writeBin("machinist", fmt.Sprintf(`#!/bin/sh
if [ "$1" = "push" ]; then
  exit 0
fi
exec env MACHINIST_TEST_CLI=1 %q -test.run=^TestMachinistAgentHelper$ -- "$@"
`, testBinary))

	// Fake agy records its harness and --model argument, writes review.json when reviewing, and emits stream-json.
	writeBin("agy", fmt.Sprintf(`#!/bin/sh
model=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--model" ]; then
    model="$arg"
  fi
  prev="$arg"
done
printf 'agy %%s\n' "$model" >> %q
if [ ! -f "$MACHINIST_DIR/review.json" ]; then
  if [ -f "$MACHINIST_DIR/first-review.json" ]; then
    printf '{"verdict":"accepted","summary":"All fixed."}\n' > "$MACHINIST_DIR/review.json"
  else
    printf '{"verdict":"revision_needed","summary":"Fix one issue."}\n' > "$MACHINIST_DIR/review.json"
  fi
fi
printf '{"event":"init","init":{"model":"gemini-4-neon-high"}}\n'
printf '{"event":"result","result":{"status":"SUCCESS","num_turns":1,"usage":{"total_tokens":120}}}\n'
`, logFile))

	// Fake claude records its harness and --model argument and emits stream-json with usage.
	writeBin("claude", fmt.Sprintf(`#!/bin/sh
model=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--model" ]; then
    model="$arg"
  fi
  prev="$arg"
done
printf 'claude %%s\n' "$model" >> %q
printf '{"type":"system","subtype":"init","model":"claude-opus-5-5"}\n'
printf '{"type":"result","num_turns":1,"total_cost_usd":0.42,"usage":{"input_tokens":50,"output_tokens":10}}\n'
`, logFile))

	request := runner.RunRequest{
		RunID:      "run_0123456789abcdef01234567",
		TicketID:   "t_1",
		Preset:     "neon",
		Repository: "owner/repo",
		Branch:     "machinist/t_1",
		Vars:       map[string]string{"repo": "owner/repo"},
		Prompt:     "Add feature X.",
		Files: map[string]string{
			// All three agent files default to claude with no model.
			"agents/review.md":  "---\nharness: claude\n---\nReview the branch.\n",
			"agents/revise.md":  "---\nharness: claude\n---\nAddress the review.\n",
			"agents/publish.md": "---\nharness: claude\n---\nOpen a PR.\n",
			// Shell scripts matching examples/factory/scripts/{review,revise,publish}.sh.
			"scripts/review.sh":  "#!/bin/sh\nset -e\nrm -f \"$MACHINIST_DIR/review.json\"\nmachinist agent review\nmachinist verdict\n",
			"scripts/revise.sh":  "#!/bin/sh\nset -e\n[ \"$(machinist verdict)\" = revision_needed ] || exit 0\nmachinist agent revise\nmv \"$MACHINIST_DIR/review.json\" \"$MACHINIST_DIR/first-review.json\"\nmachinist agent review\nmachinist verdict\n",
			"scripts/publish.sh": "#!/bin/sh\nset -e\nmachinist push\nmachinist agent publish\n",
		},
		Steps: []runner.Step{
			{Name: "review", Run: "review.sh", Harness: "agy", Model: "gemini-4-neon-high"},
			{Name: "revise", Run: "revise.sh", Harness: "agy", Model: "gemini-4-neon-high"},
			{Name: "publish", Run: "publish.sh", Harness: "claude", Model: "opus"},
		},
		TimeoutMillis: time.Minute.Milliseconds(),
		GitName:       "machinist-bot",
		GitEmail:      "machinist-bot@users.noreply.github.com",
	}

	root := t.TempDir()
	workspace, err := runner.PrepareWorkspace(runner.WorkspaceOptions{
		Request:     request,
		Root:        root,
		GitHubToken: "token",
		Environment: []string{"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH")},
		Home:        filepath.Join(root, "home"),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := runner.RunSteps(t.Context(), request, workspace, t.TempDir())
	if result.State != runner.StateSucceeded {
		t.Fatalf("result = %+v", result)
	}

	wantReview := []protocol.Agent{{Name: "review", Harness: "agy", Model: "gemini-4-neon-high"}}
	wantRevise := []protocol.Agent{
		{Name: "revise", Harness: "agy", Model: "gemini-4-neon-high"},
		{Name: "review", Harness: "agy", Model: "gemini-4-neon-high"},
	}
	wantPublish := []protocol.Agent{{Name: "publish", Harness: "claude", Model: "opus"}}
	if !slices.Equal(result.Stages[0].Agents, wantReview) {
		t.Fatalf("review stage agents = %+v, want %+v", result.Stages[0].Agents, wantReview)
	}
	if !slices.Equal(result.Stages[1].Agents, wantRevise) {
		t.Fatalf("revise stage agents = %+v, want %+v", result.Stages[1].Agents, wantRevise)
	}
	if !slices.Equal(result.Stages[2].Agents, wantPublish) {
		t.Fatalf("publish stage agents = %+v, want %+v", result.Stages[2].Agents, wantPublish)
	}
	if result.Stages[0].Tokens == nil || *result.Stages[0].Tokens != 120 || !result.Stages[0].CostUnknown {
		t.Fatalf("review usage = %+v", result.Stages[0])
	}
	if result.Stages[1].Tokens == nil || *result.Stages[1].Tokens != 240 {
		t.Fatalf("revise usage = %+v", result.Stages[1])
	}
	if result.Stages[2].Tokens == nil || *result.Stages[2].Tokens != 60 || result.Stages[2].Cost == nil || *result.Stages[2].Cost != 0.42 {
		t.Fatalf("publish usage = %+v", result.Stages[2])
	}

	invocations, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(invocations)), "\n")
	wantLines := []string{
		"agy gemini-4-neon-high",
		"agy gemini-4-neon-high",
		"agy gemini-4-neon-high",
		"claude opus",
	}
	if !slices.Equal(lines, wantLines) {
		t.Fatalf("invocations = %q, want %q", lines, wantLines)
	}

	// Verify direct `machinist agent` records the overridden harness/model in usage.json.
	control := t.TempDir()
	if err := os.MkdirAll(filepath.Join(control, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "agents", "review.md"), []byte("---\nharness: claude\n---\nReview.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "prompt.md"), []byte("Check it."), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MACHINIST_DIR", control)
	t.Setenv("MACHINIST_HARNESS", "agy")
	t.Setenv("MACHINIST_MODEL", "gemini-4-neon-high")
	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"agent", "review"}, strings.NewReader(""), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	rawUsage, err := os.ReadFile(filepath.Join(control, harness.UsageFile))
	if err != nil {
		t.Fatal(err)
	}
	var usage harness.Usage
	if err := json.Unmarshal(rawUsage, &usage); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(usage.Agents, wantReview) || usage.Tokens != 120 {
		t.Fatalf("usage.json = %+v", usage)
	}
}
