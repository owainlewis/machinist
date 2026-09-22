package runner

import (
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRevisionPromptKeepsFeedbackLiteralAndIncludesSavedFiles(t *testing.T) {
	r := &protocol.Revision{PreviousRunID: "run_old", Feedback: "Keep {{task.spec}} literal", PreviousSummary: "Original plan", PriorFeedback: []string{"Keep compatibility"}, Artifacts: map[string]protocol.Artifact{"old": {Path: "plan.md"}}}
	got := revisionPrompt(r, map[string]string{"old": "/private/inputs/old"})
	for _, want := range []string{"run_old", "Keep {{task.spec}} literal", "Keep compatibility", "plan.md", "/private/inputs/old"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}

func TestScriptRevisionPreservesJSONInput(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	for _, task := range []*protocol.Task{nil, {Spec: "requirements"}} {
		script := `import json, os, sys
assert json.load(sys.stdin) == {"action": "classify"}
with open(os.environ["MACHINIST_REVISION_PATH"]) as f:
    assert json.load(f)["feedback"] == "Recheck risk"
with open(os.environ["MACHINIST_STEP_RESULT_PATH"], "w") as f:
    json.dump({"outcome": "complete", "summary": "Rechecked"}, f)
`
		result, err := Execute(t.Context(), Options{
			Workflow: true, Task: task, Revision: &protocol.Revision{Feedback: "Recheck risk"},
			Command:    config.ResolvedCommand{Name: "policy", Executor: "script", Command: []string{python, "-c", script}, Prompt: `{"action":"classify"}`, Timeout: 5 * time.Second},
			Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
		})
		if err != nil || result.State != StateSucceeded {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}
