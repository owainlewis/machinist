package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/owainlewis/machinist/internal/tickets"
)

// installAutomation adds a weekday automation that runs the default preset.
func installAutomation(t *testing.T, directory string) {
	t.Helper()
	writeFile(t, filepath.Join(directory, "automations/tidy-readme.md"), "---\nschedule: \"0 9 * * 1-5\"\ntimezone: UTC\nrepo: owner/repo\npreset: default\n---\nTidy the README.\n", 0o600)
}

// CUJ 6: an automation run now starts a job that opens a pull request.
func TestAutomationRunsTheDefaultPreset(t *testing.T) {
	s := startWith(t, options{claude: claudeScript, factory: installAutomation})
	request, err := http.NewRequest(http.MethodPost, "http://"+s.address+"/api/v1/automations/tidy-readme/run", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var ticket tickets.Ticket
	if err := json.NewDecoder(response.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	if ticket.Source != "schedule:tidy-readme" || ticket.Agent != "default" {
		t.Fatalf("ticket = %+v", ticket)
	}
	s.waitForState(t, ticket.ID, tickets.StateDone)
	if view := s.job(t, ticket.ID); view.PRNumber == 0 {
		t.Fatalf("job = %+v", view)
	}
	if prompt := s.read(t, "prompt"); !bytes.Contains([]byte(prompt), []byte("Tidy the README.")) {
		t.Fatalf("prompt = %q", prompt)
	}
}
