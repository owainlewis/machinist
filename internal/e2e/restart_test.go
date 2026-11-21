package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/owainlewis/machinist/internal/tickets"
)

// pausingScript waits for the test to release it, so a restart lands mid-run.
func pausingScript(output string) string {
	return `#!/bin/sh
` + fakeLaterSteps(output) + `
cat > /dev/null
: > ` + output + `/started
while [ ! -f ` + output + `/release ]; do sleep 0.05; done
`
}

// CUJ 8.
func TestControlPlaneRestartDoesNotDisturbARun(t *testing.T) {
	s := startWith(t, options{claude: pausingScript})
	ticket := s.createTicket(t)
	eventually(t, "the work step to start", func() bool {
		_, err := os.Stat(filepath.Join(s.output, "started"))
		return err == nil
	})
	s.restart(t)
	writeFile(t, filepath.Join(s.output, "release"), "", 0o600)
	s.waitForState(t, ticket.ID, tickets.StateDone)
	runs, err := s.store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != tickets.RunSucceeded || runs[0].Report.Summary != "The work needed no change." {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	listed, err := s.store.ListTickets(t.Context(), "", "")
	if err != nil || len(listed) != 1 {
		t.Fatalf("tickets = %+v, err = %v", listed, err)
	}
}
