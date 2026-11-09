package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/tickets"
)

func newEngine(t *testing.T) *Engine {
	t.Helper()
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(Options{
		Store:      store,
		Factory:    &factory.Factory{Agents: map[string]factory.Agent{"default": {Name: "default", Harness: "claude", Model: "sonnet", Playbook: "THE PLAYBOOK", Files: []runner.AgentFile{{Path: ".claude/agents/x.md", Body: "x"}}}}},
		MaxTurns:   3,
		RunTimeout: time.Hour,
		Handle:     "machinist-bot",
	})
}

func newTicket(t *testing.T, e *Engine, source string) tickets.Ticket {
	t.Helper()
	ticket, _, err := e.Store.CreateTicket(t.Context(), tickets.NewTicket{Source: source, Repo: "owner/repo", Title: "Title", Body: "THE REQUEST", Agent: "default", CreatedBy: "alice", SourceKey: "key:" + t.Name() + source})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func assignRun(t *testing.T, e *Engine) *runner.RunRequest {
	t.Helper()
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	request, err := e.Assign(t.Context(), "runner-1", []string{"claude"})
	if err != nil || request == nil {
		t.Fatalf("request = %v, err = %v", request, err)
	}
	return request
}

func state(t *testing.T, e *Engine, id string) string {
	t.Helper()
	ticket, err := e.Store.Ticket(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ticket.State
}

func lastMessage(t *testing.T, e *Engine, id string) string {
	t.Helper()
	entries, err := e.Store.Entries(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Kind == tickets.EntryMessage {
			return entries[index].Body
		}
	}
	return ""
}

func succeed(outcome, summary, question string) protocol.Completion {
	return protocol.Completion{State: tickets.RunSucceeded, Report: &protocol.Report{Outcome: outcome, Summary: summary, Question: question}}
}

func TestAssignBuildsTheRunRequest(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if request.TicketID != ticket.ID || request.Repository != "owner/repo" || request.Branch != "machinist/"+ticket.ID || request.Harness != "claude" || request.Model != "sonnet" {
		t.Fatalf("request = %+v", request)
	}
	if request.TimeoutMillis != time.Hour.Milliseconds() || request.GitName != "machinist-bot" || request.GitEmail != "machinist-bot@users.noreply.github.com" || len(request.AgentFiles) != 1 {
		t.Fatalf("request = %+v", request)
	}
	if !runner.ValidRunID(request.RunID) {
		t.Fatalf("run ID = %q", request.RunID)
	}
	order := []string{"THE PLAYBOOK", "machinist/" + ticket.ID, `"outcome"`, "THE REQUEST"}
	position := -1
	for _, part := range order {
		next := strings.Index(request.Prompt, part)
		if next <= position {
			t.Fatalf("%q is out of order in:\n%s", part, request.Prompt)
		}
		position = next
	}
}

func TestAssignReturnsNothingWithoutMatchingWork(t *testing.T) {
	e := newEngine(t)
	newTicket(t, e, "api")
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if request, err := e.Assign(t.Context(), "runner-1", []string{"codex"}); err != nil || request != nil {
		t.Fatalf("request = %v, err = %v", request, err)
	}
}

func TestTickQueuesOneRunPerTicket(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	for range 3 {
		if err := e.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := e.Store.Runs(t.Context(), ticket.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != tickets.RunQueued || runs[0].Kind != tickets.RunKindAgent {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
}

func TestReportRows(t *testing.T) {
	found := &PR{Number: 42}
	for _, test := range []struct {
		name       string
		completion protocol.Completion
		pr         *PR
		want       string
		message    string
	}{
		{"pr ready with PR", succeed("pr_ready", "Opened", ""), found, tickets.StatePRReady, ""},
		{"pr ready without PR", succeed("pr_ready", "Opened", ""), nil, tickets.StateNeedsYou, "Reported a PR but none exists for this branch."},
		{"needs input", succeed("needs_input", "Stuck", "Which API?"), nil, tickets.StateNeedsYou, "Which API?"},
		{"no change", succeed("no_change", "Nothing to do", ""), nil, tickets.StateDone, "Nothing to do"},
		{"failed report", succeed("failed", "Tests cannot run", ""), nil, tickets.StateNeedsYou, "Tests cannot run"},
		{"no report", protocol.Completion{State: tickets.RunSucceeded}, nil, tickets.StateNeedsYou, "The run ended without a report: succeeded"},
		{"malformed report", succeed("complete", "old", ""), nil, tickets.StateNeedsYou, "The run ended without a report: report outcome"},
		{"run failed", protocol.Completion{State: tickets.RunFailed, Error: "exit 7"}, nil, tickets.StateNeedsYou, "The run ended without a report: exit 7"},
		{"deadline", protocol.Completion{State: tickets.RunTimedOut, Error: "command exceeded timeout"}, nil, tickets.StateNeedsYou, "The run ended without a report: command exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := newEngine(t)
			e.FindPR = func(context.Context, string, string) (*PR, error) { return test.pr, nil }
			ticket := newTicket(t, e, "api")
			request := assignRun(t, e)
			if err := e.Complete(t.Context(), request.RunID, test.completion); err != nil {
				t.Fatal(err)
			}
			got, _ := e.Store.Ticket(t.Context(), ticket.ID)
			if got.State != test.want {
				t.Fatalf("state = %s, want %s", got.State, test.want)
			}
			if message := lastMessage(t, e, ticket.ID); !strings.HasPrefix(message, test.message) || (test.message == "" && message != "") {
				t.Fatalf("message = %q, want %q", message, test.message)
			}
			if test.pr != nil && got.PRNumber != 42 {
				t.Fatalf("PR = %d", got.PRNumber)
			}
		})
	}
}

func TestAutomationNeverWaitsForAPerson(t *testing.T) {
	for _, completion := range []protocol.Completion{succeed("needs_input", "Stuck", "Which?"), succeed("failed", "Broken", ""), succeed("pr_ready", "Opened", ""), {State: tickets.RunFailed, Error: "exit 1"}} {
		e := newEngine(t)
		ticket := newTicket(t, e, "schedule:nightly")
		request := assignRun(t, e)
		if err := e.Complete(t.Context(), request.RunID, completion); err != nil {
			t.Fatal(err)
		}
		if got := state(t, e, ticket.ID); got != tickets.StateFailed {
			t.Fatalf("%+v: state = %s", completion, got)
		}
	}
}

func TestCompleteRejectsUnknownStateAndInactiveRuns(t *testing.T) {
	e := newEngine(t)
	newTicket(t, e, "api")
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, protocol.Completion{State: "exploded"}); !errors.Is(err, ErrInvalidCompletion) {
		t.Fatalf("error = %v", err)
	}
	if err := e.Complete(t.Context(), request.RunID, succeed("no_change", "Done", "")); err != nil {
		t.Fatal(err)
	}
	if err := e.Complete(t.Context(), request.RunID, succeed("no_change", "Done", "")); !errors.Is(err, tickets.ErrRunNotActive) {
		t.Fatalf("error = %v", err)
	}
}

func TestPRLookupFailureLeavesRunActiveSoRunnerRetries(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	failures := 1
	e.FindPR = func(context.Context, string, string) (*PR, error) {
		if failures > 0 {
			failures--
			return nil, errors.New("github is down")
		}
		return nil, nil
	}
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, succeed("no_change", "Done", "")); err == nil {
		t.Fatal("lookup failure hidden")
	}
	if got := state(t, e, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
	if err := e.Complete(t.Context(), request.RunID, succeed("no_change", "Done", "")); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateDone {
		t.Fatalf("state = %s", got)
	}
}

func TestStoppedRunnerRequeuesWithoutBotheringAPerson(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	first := assignRun(t, e)
	if err := e.Complete(t.Context(), first.RunID, protocol.Completion{State: tickets.RunCancelled}); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
	run, _ := e.Store.Run(t.Context(), first.RunID)
	if run.Status != tickets.RunInterrupted {
		t.Fatalf("status = %s", run.Status)
	}
	second := assignRun(t, e)
	if second.RunID == first.RunID {
		t.Fatal("run ID reused")
	}
}

func TestTurnLimitStopsAndHumanReplyRestartsCount(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	for index := range e.MaxTurns {
		request := assignRun(t, e)
		completion := protocol.Completion{State: tickets.RunCancelled}
		if index == e.MaxTurns-1 {
			if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryCI, Author: "github", Body: "CI failed", SourceKey: "ci:1"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.Complete(t.Context(), request.RunID, completion); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
	if message := lastMessage(t, e, ticket.ID); !strings.Contains(message, "limit of 3 runs") {
		t.Fatalf("message = %q", message)
	}
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Keep going"}); err != nil {
		t.Fatal(err)
	}
	if request := assignRun(t, e); !strings.Contains(request.Prompt, "Keep going") {
		t.Fatalf("prompt = %s", request.Prompt)
	}
}

func TestTurnLimitMessageIsTheLastReport(t *testing.T) {
	e := newEngine(t)
	e.MaxTurns = 1
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryCI, Author: "github", Body: "CI failed", SourceKey: "ci:1"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateWorking, Report: "failed: Could not build"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Complete(t.Context(), request.RunID, protocol.Completion{State: tickets.RunCancelled}); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if message := lastMessage(t, e, ticket.ID); message != "failed: Could not build" {
		t.Fatalf("message = %q", message)
	}
}

func TestFollowUpRunSeesEarlierReportAndAnswer(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	first := assignRun(t, e)
	if err := e.Complete(t.Context(), first.RunID, succeed("needs_input", "Stuck on auth", "Which API?")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Use the v2 API"}); err != nil {
		t.Fatal(err)
	}
	second := assignRun(t, e)
	if second.Branch != first.Branch {
		t.Fatalf("branch changed: %s", second.Branch)
	}
	for _, want := range []string{"THE REQUEST", "Stuck on auth", "Which API?", "Use the v2 API"} {
		if !strings.Contains(second.Prompt, want) {
			t.Fatalf("missing %q in:\n%s", want, second.Prompt)
		}
	}
}

func TestMissingAgentAsksForAPerson(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	delete(e.Factory.Agents, "default")
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
	if message := lastMessage(t, e, ticket.ID); !strings.Contains(message, "default") {
		t.Fatalf("message = %q", message)
	}
}

func TestAgentRemovedAfterQueueingAsksForAPerson(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	delete(e.Factory.Agents, "default")
	if request, err := e.Assign(t.Context(), "runner-1", []string{"claude"}); err != nil || request != nil {
		t.Fatalf("request = %v, err = %v", request, err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateNeedsYou {
		t.Fatalf("state = %s", got)
	}
}

func TestRunnerIsToldToStopAfterCancel(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if cancel, err := e.Heartbeat(t.Context(), request.RunID); err != nil || cancel {
		t.Fatalf("cancel = %v, err = %v", cancel, err)
	}
	if _, err := e.Store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if cancel, err := e.Heartbeat(t.Context(), request.RunID); err != nil || !cancel {
		t.Fatalf("cancel = %v, err = %v", cancel, err)
	}
	if err := e.Complete(t.Context(), request.RunID, protocol.Completion{State: tickets.RunCancelled}); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateCancelled {
		t.Fatalf("state = %s", got)
	}
}

func TestRunStopsAtContextEnd(t *testing.T) {
	e := newEngine(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not stop")
	}
}
