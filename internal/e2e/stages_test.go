package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

// stageScript is the builder or the checker, by the playbook in its prompt. The
// builder leaves a file that it does not commit. The checker records what the
// script stage left in the clone.
func stageScript(output string) string {
	return `#!/bin/sh
prompt=$(cat)
case "$prompt" in
*'THE CHECKER PLAYBOOK'*)
  echo checker >> ` + output + `/order
  cat tested.txt > ` + output + `/checker-saw
  report='{"outcome":"no_change","summary":"Looks right"}';;
*)
  echo builder >> ` + output + `/order
  echo built > built.txt
  report='{"outcome":"no_change","summary":"Built it"}';;
esac
printf '%s\n' '{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}'
printf '%s\n' "{\"type\":\"result\",\"is_error\":false,\"result\":\"done\",\"total_cost_usd\":0.5,\"usage\":{\"input_tokens\":10,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":0,\"output_tokens\":5},\"structured_output\":$report}"
`
}

// startJob starts a system whose factory has the job ship: the default agent,
// then script, then the checker agent.
func startJob(t *testing.T, script string) (*system, tickets.Ticket) {
	t.Helper()
	s := startWith(t, "claude", options{claude: stageScript, maxTurns: 5, factory: func(t *testing.T, directory string) {
		writeFile(t, filepath.Join(directory, "agents/checker/agent.toml"), "harness = \"claude\"\n", 0o600)
		writeFile(t, filepath.Join(directory, "agents/checker/playbook.md"), "THE CHECKER PLAYBOOK\n", 0o600)
		job := fmt.Sprintf("[[stages]]\nname = \"build\"\nagent = \"default\"\n\n[[stages]]\nname = \"test\"\nscript = %q\n\n[[stages]]\nname = \"check\"\nagent = \"checker\"\n", script)
		writeFile(t, filepath.Join(directory, "jobs/ship.toml"), job, 0o600)
	}})
	ticket, created, err := s.store.CreateTicket(t.Context(), tickets.NewTicket{Source: "api", Repo: "owner/repo", Title: "Add a thing", Body: "THE REQUEST", Agent: "ship", CreatedBy: "alice", SourceKey: "api:1"})
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	return s, ticket
}

func stageStates(run tickets.Run) string {
	states := []string{}
	for _, stage := range run.Stages {
		states = append(states, stage.Name+"="+stage.State)
	}
	return strings.Join(states, " ")
}

func TestJobRunsItsStagesInOrderInOneClone(t *testing.T) {
	s, ticket := startJob(t, "test -f built.txt && echo tested > tested.txt")
	s.waitForState(t, ticket.ID, tickets.StateDone)

	runs, err := s.store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	run := runs[0]
	if run.Status != tickets.RunSucceeded || run.Report.Summary != "Looks right" || *run.Cost != 1 || *run.Tokens != 30 {
		t.Fatalf("run = %+v", run)
	}
	if got := stageStates(run); got != "build=succeeded test=succeeded check=succeeded" {
		t.Fatalf("stages = %s", got)
	}
	if script := run.Stages[1]; script.Kind != "script" || script.Tokens != nil || run.Stages[0].Tokens == nil {
		t.Fatalf("stages = %+v", run.Stages)
	}
	if order := s.read(t, "order"); order != "builder\nchecker\n" {
		t.Fatalf("order = %q", order)
	}
	if saw := s.read(t, "checker-saw"); saw != "tested\n" {
		t.Fatalf("the checker saw %q", saw)
	}
}

func TestAFailedScriptStageEndsTheRun(t *testing.T) {
	s, ticket := startJob(t, "echo the tests failed >&2; exit 3")
	s.waitForState(t, ticket.ID, tickets.StateNeedsYou)

	runs, err := s.store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	run := runs[0]
	if run.Status != tickets.RunFailed || run.Report != nil || run.Stages[1].Error == "" {
		t.Fatalf("run = %+v", run)
	}
	entries, err := s.store.Entries(t.Context(), ticket.ID)
	if err != nil || !strings.Contains(entries[len(entries)-1].Body, "stage test: ") {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	if got := stageStates(run); got != "build=succeeded test=failed check="+protocol.StageSkipped {
		t.Fatalf("stages = %s", got)
	}
	if order := s.read(t, "order"); order != "builder\n" {
		t.Fatalf("the checker ran after a failed stage: %q", order)
	}
}
