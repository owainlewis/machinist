package e2e

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/owainlewis/machinist/internal/tickets"
)

// The hello preset proves a control plane and a runner work together, with no
// clone and no agent.
func TestHelloPresetFinishesWithItsResult(t *testing.T) {
	s := start(t)
	status, body := s.call(t, http.MethodPost, "/machinist/run", map[string]any{
		"key": "hello-1", "preset": "hello", "vars": map[string]string{"repo": "owner/repo"}, "prompt": "Say hello",
	})
	var created jobView
	if err := json.Unmarshal(body, &created); err != nil || status != http.StatusCreated {
		t.Fatalf("status = %d: %s", status, body)
	}
	s.waitForState(t, created.ID, tickets.StateDone)
	if view := s.job(t, created.ID); view.State != "done" || view.PRNumber != 0 {
		t.Fatalf("job = %+v", view)
	}
	runs, err := s.store.Runs(t.Context(), created.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	if got := stepStates(runs[0]); got != "hello=succeeded finish=succeeded" {
		t.Fatalf("steps = %s", got)
	}
	if runs[0].Report == nil || runs[0].Report.Summary != "Hello from Machinist." {
		t.Fatalf("report = %+v", runs[0].Report)
	}
}
