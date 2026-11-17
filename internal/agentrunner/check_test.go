package agentrunner

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/runner"
)

func TestCheckRunsOneStepThatReachesTheRepository(t *testing.T) {
	var requests []runner.RunRequest
	executor := executorFunc(func(_ context.Context, request runner.RunRequest) runner.RunResult {
		requests = append(requests, request)
		return runner.RunResult{State: runner.StateSucceeded}
	})
	var output bytes.Buffer
	cfg := config.Runner{DataDirectory: t.TempDir(), Env: []string{"ANTHROPIC_API_KEY"}}
	if err := Check(t.Context(), executor, cfg, "owner/repo", &output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "ok: a step reached owner/repo, with 1 variables from env\n" {
		t.Fatalf("output = %q", got)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %+v", requests)
	}
	request := requests[0]
	if !runner.ValidRunID(request.RunID) || request.Repository != "owner/repo" || request.Vars["repo"] != "owner/repo" || !strings.HasPrefix(request.Branch, "machinist/check-") {
		t.Fatalf("request = %+v", request)
	}
	if len(request.Steps) != 1 || !strings.Contains(request.Steps[0].Run, "git ls-remote") {
		t.Fatalf("steps = %+v", request.Steps)
	}
}

func TestCheckPrintsErrorAndFails(t *testing.T) {
	executor := executorFunc(func(context.Context, runner.RunRequest) runner.RunResult {
		return runner.RunResult{State: runner.StateFailed, Error: "step reach-repository: command exited with status 128"}
	})
	var output bytes.Buffer
	cfg := config.Runner{DataDirectory: t.TempDir()}
	if err := Check(t.Context(), executor, cfg, "owner/repo", &output); err == nil || !strings.Contains(output.String(), "failed: step reach-repository") {
		t.Fatalf("err = %v, output = %q", err, output.String())
	}
}
