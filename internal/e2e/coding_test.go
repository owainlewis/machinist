package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/tickets"
)

// codingClaude plays each agent step of the coding preset, by the step its
// system prompt names, and records the order it was called in.
func codingClaude(output string) string {
	return `#!/bin/sh
cat > /dev/null
case "$*" in
*'Your step is the plan'*) echo plan >> ` + output + `/order; echo 'Add hello.go.' > "$MACHINIST_DIR/plan.md";;
*'Your step is to implement'*) echo implement >> ` + output + `/order; printf 'package hello\n' > hello.go; git add hello.go; git commit --quiet -m 'Add hello';;
*'Your step is the review'*) echo review >> ` + output + `/order; echo 'Add a doc comment.' > "$MACHINIST_DIR/review.md";;
*'Your step is to fix'*) echo fix >> ` + output + `/order; printf '// Package hello says hello.\npackage hello\n' > hello.go; git commit --quiet -am 'Document hello';;
*) echo "unknown step: $*" >&2; exit 1;;
esac
`
}

// The coding preset as shipped: plan, implement, test, review, fix, retest and
// one draft pull request, on a Go repository.
func TestCodingPresetOpensOneReviewedPullRequest(t *testing.T) {
	s := startWith(t, options{claude: codingClaude})
	seed := filepath.Join(t.TempDir(), "seed")
	git(t, filepath.Dir(seed), "clone", "--quiet", s.remote, seed)
	writeFile(t, filepath.Join(seed, "go.mod"), "module example.test/hello\n\ngo 1.22\n", 0o600)
	git(t, seed, "add", "go.mod")
	git(t, seed, "commit", "--quiet", "-m", "Add go.mod")
	git(t, seed, "push", "--quiet", "origin", "main")

	ticket, created, err := s.store.CreateTicket(t.Context(), tickets.NewTicket{Source: "api", Repo: "owner/repo", Title: "Add hello", Body: "Add a hello package.", Agent: "coding", CreatedBy: "alice", SourceKey: "api:coding"})
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	finished := s.waitForState(t, ticket.ID, tickets.StateDone)
	if finished.PRNumber != 7 {
		t.Fatalf("ticket = %+v", finished)
	}
	runs, err := s.store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	if got := stepStates(runs[0]); got != "checkout=succeeded plan=succeeded implement=succeeded test=succeeded review=succeeded fix=succeeded retest=succeeded publish=succeeded" {
		t.Fatalf("steps = %s", got)
	}
	if order := s.read(t, "order"); order != "plan\nimplement\nreview\nfix\n" {
		t.Fatalf("order = %q", order)
	}
	if history := git(t, s.remote, "log", "--format=%s", "main.."+finished.Branch()); history != "Document hello\nAdd hello" {
		t.Fatalf("history = %q", history)
	}
	var create string
	for _, call := range s.github.Calls() {
		if strings.HasPrefix(call, "pr create") {
			create = call
		}
	}
	if !strings.Contains(create, "--draft") || !strings.Contains(create, "--title Add hello") {
		t.Fatalf("pr create = %q", create)
	}
	if _, err := os.Stat(filepath.Join(s.data, "work")); err == nil {
		s.waitForCleanup(t, runs[0].ID)
	}
}
