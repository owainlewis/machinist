package agentrunner

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

func TestCheckPrintsCredentialSourcePerHarness(t *testing.T) {
	var requests []runner.RunRequest
	executor := executorFunc(func(_ context.Context, request runner.RunRequest) runner.RunResult {
		requests = append(requests, request)
		return runner.RunResult{State: runner.StateSucceeded, Report: &protocol.Report{Outcome: "no_change", Summary: "ok"}, CredentialSource: "source-" + request.Harness}
	})
	var output bytes.Buffer
	cfg := config.Runner{DataDirectory: t.TempDir(), Credentials: map[string]config.Credential{"claude": {Login: true}, "codex": {Login: true}}}
	if err := Check(t.Context(), executor, cfg, "owner/repo", &output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "claude: ok, credential source: source-claude\ncodex: ok, credential source: source-codex\n" {
		t.Fatalf("output = %q", got)
	}
	for _, request := range requests {
		if !runner.ValidRunID(request.RunID) || request.Repository != "owner/repo" || !strings.HasPrefix(request.Branch, "machinist/check-") {
			t.Fatalf("request = %+v", request)
		}
		if !strings.Contains(request.Prompt, `{"outcome":"no_change","summary":"ok"}`) {
			t.Errorf("%s prompt is missing the report object: %q", request.Harness, request.Prompt)
		}
		if !strings.Contains(request.Prompt, "exactly one JSON object and nothing else") {
			t.Errorf("%s prompt is missing the JSON-only instruction: %q", request.Harness, request.Prompt)
		}
	}
}

func TestCheckPrintsErrorAndFails(t *testing.T) {
	executor := executorFunc(func(context.Context, runner.RunRequest) runner.RunResult {
		return runner.RunResult{State: runner.StateFailed, Error: "git clone: denied"}
	})
	var output bytes.Buffer
	cfg := config.Runner{DataDirectory: t.TempDir(), Credentials: map[string]config.Credential{"claude": {Login: true}}}
	if err := Check(t.Context(), executor, cfg, "owner/repo", &output); err == nil || !strings.Contains(output.String(), "claude: failed: git clone: denied") {
		t.Fatalf("err = %v, output = %q", err, output.String())
	}
}
