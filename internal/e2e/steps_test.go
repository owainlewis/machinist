package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

// startSteps starts a system whose ship preset clones, builds, runs the test
// command given, and checks what the test left in the workspace.
func startSteps(t *testing.T, test string) (*system, tickets.Ticket) {
	t.Helper()
	s := startWith(t, options{factory: func(t *testing.T, directory string) {
		preset := fmt.Sprintf(`description: Test.
steps:
  - name: checkout
    run: gh repo clone "$MACHINIST_REPO" . -- --quiet
  - name: build
    run: echo built > built.txt
  - name: test
    run: %q
  - name: check
    run: cat tested.txt > "$MACHINIST_DIR/result.md"
`, test)
		writeFile(t, filepath.Join(directory, "presets/ship.yaml"), preset, 0o600)
	}})
	ticket, created, err := s.store.CreateTicket(t.Context(), tickets.NewTicket{Source: "api", Repo: "owner/repo", Title: "Add a thing", Body: "THE REQUEST", Agent: "ship", CreatedBy: "alice", SourceKey: "api:1"})
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	return s, ticket
}

func TestStepsRunInOrderInOneWorkspace(t *testing.T) {
	s, ticket := startSteps(t, "test -f built.txt && echo tested > tested.txt")
	s.waitForState(t, ticket.ID, tickets.StateDone)

	runs, err := s.store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	run := runs[0]
	if run.Status != tickets.RunSucceeded || run.Report.Summary != "tested" {
		t.Fatalf("run = %+v", run)
	}
	if got := stepStates(run); got != "checkout=succeeded build=succeeded test=succeeded check=succeeded" {
		t.Fatalf("steps = %s", got)
	}
	if run.Stages[0].Kind != "step" {
		t.Fatalf("steps = %+v", run.Stages)
	}
}

func TestAFailedStepEndsTheRun(t *testing.T) {
	s, ticket := startSteps(t, "echo the tests failed >&2; exit 3")
	s.waitForState(t, ticket.ID, tickets.StateFailed)

	runs, err := s.store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	run := runs[0]
	if run.Status != tickets.RunFailed || run.Report != nil || run.Stages[2].Error == "" {
		t.Fatalf("run = %+v", run)
	}
	entries, err := s.store.Entries(t.Context(), ticket.ID)
	if err != nil || !strings.Contains(entries[len(entries)-1].Body, "step test: ") {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	if got := stepStates(run); got != "checkout=succeeded build=succeeded test=failed check="+protocol.StageSkipped {
		t.Fatalf("steps = %s", got)
	}
}
