package worker

import (
	"errors"
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

func TestStageStartFailureReasonHonorsCancellation(t *testing.T) {
	tests := []struct {
		name, current, code, want string
		err                       error
	}{
		{name: "cancelled by control plane", code: "cancellation_requested", want: "cancelled"},
		{name: "lease lost", code: "lease_not_owner", want: "lease_lost"},
		{name: "other API error", code: "stage_not_pending", want: "failed"},
		{name: "transport error", err: errors.New("connection closed"), want: "failed"},
		{name: "existing timeout wins", current: "timeout", code: "cancellation_requested", want: "timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.err
			if err == nil {
				err = &APIError{Code: test.code}
			}
			if got := stageStartFailureReason(test.current, err); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}
}
