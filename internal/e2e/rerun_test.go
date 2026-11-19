package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/owainlewis/machinist/internal/tickets"
)

// A failed job runs its preset again from the first step when someone asks.
func TestRerunStartsAFailedJobAgain(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran-once")
	s := startWith(t, options{factory: func(t *testing.T, directory string) {
		step := fmt.Sprintf("test -e %q || { touch %q; exit 1; }", marker, marker)
		writeFile(t, filepath.Join(directory, "presets", "flaky.yaml"), fmt.Sprintf("timeout: 1m\nsteps:\n  - name: flaky\n    run: '%s'\n", step), 0o600)
	}})
	status, body := s.call(t, http.MethodPost, "/machinist/run", map[string]any{
		"key": "flaky-1", "preset": "flaky", "vars": map[string]string{"repo": "owner/repo"}, "prompt": "Try it",
	})
	var created jobView
	if err := json.Unmarshal(body, &created); err != nil || status != http.StatusCreated {
		t.Fatalf("status = %d: %s", status, body)
	}
	s.waitForState(t, created.ID, tickets.StateFailed)
	if status, body := s.call(t, http.MethodPost, "/machinist/jobs/"+created.ID+"/rerun", nil); status != http.StatusOK {
		t.Fatalf("rerun: status = %d: %s", status, body)
	}
	s.waitForState(t, created.ID, tickets.StateDone)
	if status, _ := s.call(t, http.MethodPost, "/machinist/jobs/missing/rerun", nil); status != http.StatusNotFound {
		t.Fatalf("missing: status = %d", status)
	}
}
