package e2e

import (
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *system) agentRuns(t *testing.T, id string) []tickets.Run {
	t.Helper()
	runs, err := s.store.Runs(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	agent := []tickets.Run{}
	for _, run := range runs {
		if run.Kind == tickets.RunKindAgent {
			agent = append(agent, run)
		}
	}
	return agent
}

// waitForRuns waits until a ticket has this many finished agent runs and is back in a state.
func (s *system) waitForRuns(t *testing.T, id string, count int, state string) {
	t.Helper()
	eventually(t, "the runs to finish", func() bool {
		ticket, err := s.store.Ticket(t.Context(), id)
		if err != nil || ticket.State != state {
			return false
		}
		runs := s.agentRuns(t, id)
		return len(runs) == count && runs[count-1].EndedAt != nil
	})
}
