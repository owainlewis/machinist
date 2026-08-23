package worker

import (
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestBuildPromptIncludesGrammaticalSafetyInstruction(t *testing.T) {
	claim := protocol.Claim{
		Session: protocol.ClaimedSession{
			TaskName: "Fix the prompt",
			Prompt:   "Keep the change focused.",
		},
		Repository: protocol.Repository{RemoteIdentity: "github.com/owainlewis/factory"},
	}
	value := worktree{Branch: "factory/123456789abc-abcdef123456", BaseBranch: "main"}

	want := "You are running in a Factory managed Git worktree.\n" +
		"Work only on the assigned Session and repository. Preserve unrelated changes and do not touch Factory state or unrelated worktrees. " +
		"Do not switch, create, rename, or delete branches or worktrees. Complete and verify the Session before returning a concise result.\n\n" +
		"Task: Fix the prompt\n" +
		"Repository: github.com/owainlewis/factory\n" +
		"Working branch: factory/123456789abc-abcdef123456\n" +
		"Target base branch: main\n\n" +
		"Keep the change focused."

	if got := buildPrompt(claim, value); got != want {
		t.Fatalf("buildPrompt() = %q, want %q", got, want)
	}
}

func TestBuildPromptAddsUpdateContractOnlyForAgentUpdateWork(t *testing.T) {
	claim := protocol.Claim{
		Session: protocol.ClaimedSession{
			TaskName: "Report progress", Prompt: "Do the work.", OutcomeContract: protocol.OutcomeAgentUpdate,
		},
		Repository: protocol.Repository{RemoteIdentity: "github.com/owainlewis/factory"},
	}
	prompt := buildPrompt(claim, worktree{Branch: "factory/local", BaseBranch: "main"})
	for _, expected := range []string{
		"This Work is unfinished until you call factory update.",
		"running", "ready", "needs-input", "failed", "no-change", "Ready requires --pr",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("agent-update prompt missing %q: %s", expected, prompt)
		}
	}
	claim.Session.OutcomeContract = protocol.OutcomeProcessExit
	if legacy := buildPrompt(claim, worktree{Branch: "factory/local", BaseBranch: "main"}); strings.Contains(legacy, "factory update") {
		t.Fatalf("legacy prompt received update contract: %s", legacy)
	}
}
