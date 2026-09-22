package runner

import (
	"fmt"
	"github.com/owainlewis/machinist/internal/protocol"
	"sort"
	"strings"
)

// Feedback is appended after template expansion: braces in feedback are literal.
func revisionPrompt(r *protocol.Revision, inputs map[string]string) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nHuman review: revise your previous work from run %s.\nPrevious result: %s\n", r.PreviousRunID, r.PreviousSummary)
	for _, feedback := range r.PriorFeedback {
		fmt.Fprintf(&b, "Earlier review feedback: %s\n", feedback)
	}
	fmt.Fprintf(&b, "Requested changes:\n%s\n", r.Feedback)
	aliases := make([]string, 0, len(r.Artifacts))
	for alias := range r.Artifacts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		fmt.Fprintf(&b, "Previous output %q is available at %q\n", r.Artifacts[alias].Path, inputs[alias])
	}
	b.WriteString("Use the original task requirements and the review feedback. Revise the existing work, preserve unrelated changes, and publish the revised deliverables to your output directory. Historical snapshots are preserved by Machinist. Update the files in your output directory. Report what changed. Completion will return this result for human review.\n")
	return b.String()
}
