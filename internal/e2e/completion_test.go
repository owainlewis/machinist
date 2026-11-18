package e2e

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

// A finished run that cannot be recorded, here because GitHub refuses the pull
// request lookup, says why in the control plane's log. Once GitHub answers, the
// runner's retry records it.
func TestAFailedCompletionIsLoggedAndRetried(t *testing.T) {
	s := startWith(t, options{factory: func(t *testing.T, directory string) {
		writeFile(t, filepath.Join(directory, "presets", "quick.toml"), "timeout = \"1m\"\n\n[[steps]]\nname = \"quick\"\nrun = \"true\"\n", 0o600)
	}})
	s.github.FailOn("pr list", "HTTP 401: Bad credentials")
	status, body := s.call(t, http.MethodPost, "/machinist/run", map[string]any{
		"key": "quick-1", "preset": "quick", "vars": map[string]string{"repo": "owner/repo"}, "prompt": "Say hello",
	})
	var created jobView
	if err := json.Unmarshal(body, &created); err != nil || status != http.StatusCreated {
		t.Fatalf("status = %d: %s", status, body)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(s.log.String(), "recording it failed and the runner will retry") {
		if time.Now().After(deadline) {
			t.Fatalf("log:\n%s", s.log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(s.log.String(), "Bad credentials") {
		t.Fatalf("the log does not say why:\n%s", s.log.String())
	}
	if view := s.job(t, created.ID); view.State != "running" {
		t.Fatalf("job = %+v", view)
	}
	s.github.Recover()
	s.waitForState(t, created.ID, tickets.StateDone)
}
