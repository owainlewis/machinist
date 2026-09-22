package runner

import (
	"github.com/owainlewis/machinist/internal/config"
	"io"
	"os/exec"
	"testing"
	"time"
)

func TestWorkflowResultContract(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("requires sh")
	}
	for _, test := range []struct {
		name, script, outcome string
		fail                  bool
	}{
		{"complete", `input=$(cat); test "$input" = 'issue URL' || exit 9; test "$MACHINIST_JOB_ID" = job_test || exit 8; printf '%s' '{"outcome":"complete","summary":"PR created"}' > "$MACHINIST_STEP_RESULT_PATH"`, "complete", false},
		{"blocked", `cat >/dev/null; printf '%s' '{"outcome":"blocked","summary":"Need requirements"}' > "$MACHINIST_STEP_RESULT_PATH"`, "blocked", false},
		{"missing", `cat >/dev/null`, "", true},
		{"invalid", `cat >/dev/null; printf '%s' '{"outcome":"approved","summary":"ok"}' > "$MACHINIST_STEP_RESULT_PATH"`, "", true},
		{"nonzero overrides result", `cat >/dev/null; printf '%s' '{"outcome":"complete","summary":"ok"}' > "$MACHINIST_STEP_RESULT_PATH"; exit 1`, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Execute(t.Context(), Options{Workflow: true, JobID: "job_test", Command: config.ResolvedCommand{Name: "build", Executor: "script", Command: []string{shell, "-c", test.script}, Prompt: "issue URL", Timeout: time.Second}, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
			if (err != nil) != test.fail {
				t.Fatalf("result %#v error %v", result, err)
			}
			if !test.fail && (result.StepResult == nil || result.StepResult.Outcome != test.outcome) {
				t.Fatalf("result %#v", result)
			}
		})
	}
}
