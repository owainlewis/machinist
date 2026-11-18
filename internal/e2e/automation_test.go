package e2e

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/tickets"
)

// maintainerScript records its prompt and arguments, merges the PRs and prints what GitHub said.
func maintainerScript(output string) string {
	return `#!/bin/sh
cat >> ` + output + `/prompts
printf '%s' "$*" >> ` + output + `/prompts
for number in 11 12; do
  if message=$(gh pr merge "$number" --squash 2>&1); then echo "merged $number."; else echo "refused $number: $message."; fi
done
`
}

// installStarterAutomation turns on the shipped merge template.
func installStarterAutomation(t *testing.T, directory string) {
	t.Helper()
	files, err := fs.Sub(machinistexamples.Files, "factory")
	if err != nil {
		t.Fatal(err)
	}
	template, err := fs.ReadFile(files, "automation-templates/review-open-prs.md")
	if err != nil {
		t.Fatal(err)
	}
	active := strings.Replace(string(template), "repo: owner/name", "repo: owner/repo", 1)
	if active == string(template) {
		t.Fatal("the template's repo line changed")
	}
	writeFile(t, filepath.Join(directory, "automations/review-open-prs.md"), active, 0o600)
}

// CUJ 6.
func TestAutomationMergesWhatBranchProtectionAllows(t *testing.T) {
	s := startWith(t, options{claude: maintainerScript, factory: installStarterAutomation})
	s.github.RefuseMerge(12, "Protected branch: review required")

	request, err := http.NewRequest(http.MethodPost, "http://"+s.address+"/api/v1/automations/review-open-prs/run", bytes.NewReader(nil))
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
	if ticket.Source != "schedule:review-open-prs" || ticket.Agent != "maintainer" {
		t.Fatalf("ticket = %+v", ticket)
	}

	s.waitForState(t, ticket.ID, tickets.StateDone)
	if merged := s.github.Merged(); len(merged) != 1 || merged[0] != "11" {
		t.Fatalf("merged = %v", merged)
	}
	runs, err := s.store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 || !strings.Contains(runs[0].Report.Summary, "merged 11.\nrefused 12: Protected branch") {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	prompts := s.read(t, "prompts")
	for _, want := range []string{"You may merge them", "Review open pull requests"} {
		if !strings.Contains(prompts, want) {
			t.Fatalf("missing %q in:\n%s", want, prompts)
		}
	}
}
