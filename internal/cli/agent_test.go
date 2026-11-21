package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
