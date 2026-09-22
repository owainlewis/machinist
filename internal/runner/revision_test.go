package runner

import (
	"github.com/owainlewis/machinist/internal/protocol"
	"strings"
	"testing"
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
