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

// maintainerScript merges the PRs its prompt allows and reports what GitHub said.
func maintainerScript(output string) string {
	return `#!/bin/sh
prompt=$(cat)
printf '%s' "$prompt" >> ` + output + `/prompts
result=
for number in 11 12; do
  if message=$(gh pr merge "$number" --squash 2>&1); then result="$result merged $number."; else result="$result refused $number: $message."; fi
done
printf '%s\n' '{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}'
printf '%s\n' "{\"type\":\"result\",\"is_error\":false,\"result\":\"done\",\"usage\":{\"input_tokens\":1,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0,\"output_tokens\":1},\"structured_output\":{\"outcome\":\"no_change\",\"summary\":\"$result\"}}"
`
}

// installStarterAutomation copies the shipped maintainer agent and merge template.
func installStarterAutomation(t *testing.T, directory string) {
	t.Helper()
	files, err := fs.Sub(machinistexamples.Files, "factory")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agents/maintainer/agent.toml", "agents/maintainer/playbook.md"} {
		body, err := fs.ReadFile(files, name)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(directory, filepath.FromSlash(name)), string(body), 0o600)
	}
	template, err := fs.ReadFile(files, "automation-templates/review-open-prs.md")
	if err != nil {
		t.Fatal(err)
	}
	active := strings.Replace(string(template), `repo = "owner/name"`, `repo = "owner/repo"`, 1)
	if active == string(template) {
		t.Fatal("the template's repo line changed")
	}
	writeFile(t, filepath.Join(directory, "automations/review-open-prs.md"), active, 0o600)
}

// CUJ 6.
func TestAutomationMergesWhatBranchProtectionAllows(t *testing.T) {
	s := startWith(t, "claude", options{claude: maintainerScript, maxTurns: 5, factory: installStarterAutomation})
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
	if err != nil || len(runs) != 1 || !strings.Contains(runs[0].Report.Summary, "refused 12: Protected branch") {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	prompts := s.read(t, "prompts")
	for _, want := range []string{"You may merge them", "Review open pull requests"} {
		if !strings.Contains(prompts, want) {
			t.Fatalf("missing %q in:\n%s", want, prompts)
		}
	}
}
