package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

// deadlineGrace is how long before the test binary's deadline a wait gives up,
// so the test fails with its own message rather than the binary's panic.
const deadlineGrace = 10 * time.Second

// waitDeadline is when a wait gives up: just before the test binary must end,
// as go test -timeout sets it. A loaded machine runs the same steps slower, so
// a fixed wait fails runs that are still making progress.
func waitDeadline(t *testing.T) time.Time {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		return deadline.Add(-deadlineGrace)
	}
	return time.Now().Add(10 * time.Minute)
}

// stateReached says whether a ticket in state has reached want. It returns an
// error when the ticket has ended in another final state, since it will not
// move again without a person.
func stateReached(state, want string) (bool, error) {
	if state == want {
		return true, nil
	}
	switch state {
	case tickets.StateDone, tickets.StateFailed, tickets.StateCancelled:
		return false, fmt.Errorf("ticket ended %s, want %s", state, want)
	}
	return false, nil
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := waitDeadline(t)
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

func TestStateReachedWaitsOnlyWhileTheTicketCanStillMove(t *testing.T) {
	for _, test := range []struct {
		state, want string
		reached     bool
		ended       bool
	}{
		{tickets.StateDone, tickets.StateDone, true, false},
		{tickets.StateWorking, tickets.StateDone, false, false},
		{tickets.StatePRReady, tickets.StateDone, false, false},
		{tickets.StateFailed, tickets.StateDone, false, true},
		{tickets.StateCancelled, tickets.StateDone, false, true},
		{tickets.StateDone, tickets.StateFailed, false, true},
		{tickets.StateFailed, tickets.StateFailed, true, false},
	} {
		reached, err := stateReached(test.state, test.want)
		if reached != test.reached || (err != nil) != test.ended {
			t.Errorf("stateReached(%s, %s) = %v, %v", test.state, test.want, reached, err)
		}
	}
}
