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
		Factory:    &factory.Factory{Presets: map[string]factory.Preset{"default": testPreset()}},
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
	request, err := e.Assign(t.Context(), "runner-1", []string{"steps"})
	if err != nil || request == nil {
		t.Fatalf("request = %v, err = %v", request, err)
	}
	return request
}

func testPreset() factory.Preset {
	return factory.Preset{Name: "default", Steps: []runner.Step{{Name: "checkout", Run: "gh repo clone", TimeoutMillis: 1000}, {Name: "work", Run: "claude -p"}}}
}

// prompt is the prompt a run's steps receive: the ticket log.
func prompt(t *testing.T, request *runner.RunRequest) string {
	t.Helper()
	return request.Prompt
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

func succeed(outcome, summary string) protocol.Completion {
	return protocol.Completion{State: tickets.RunSucceeded, Report: &protocol.Report{Outcome: outcome, Summary: summary}}
}

func TestAssignBuildsTheRunRequest(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if request.TicketID != ticket.ID || request.Preset != "default" || request.Repository != "owner/repo" || request.Branch != "machinist/"+ticket.ID || request.Vars["repo"] != "owner/repo" || request.Vars["auto_merge"] != "false" {
		t.Fatalf("request = %+v", request)
	}
	if request.TimeoutMillis != time.Hour.Milliseconds() || request.GitName != "machinist-bot" || request.GitEmail != "machinist-bot@users.noreply.github.com" {
		t.Fatalf("request = %+v", request)
	}
	if len(request.Steps) != 2 || request.Steps[0] != testPreset().Steps[0] {
		t.Fatalf("steps = %+v", request.Steps)
	}
	if request.GitHubToken != "" {
		t.Fatalf("a run without a GitHub App got token %q", request.GitHubToken)
	}
	if !runner.ValidRunID(request.RunID) {
		t.Fatalf("run ID = %q", request.RunID)
	}
	if !strings.Contains(prompt(t, request), "THE REQUEST") {
		t.Fatalf("prompt = %q", prompt(t, request))
	}
}

func TestAssignGivesTheRunATokenForItsRepository(t *testing.T) {
	e := newEngine(t)
	asked := ""
	e.GitHubToken = func(_ context.Context, repository string) (string, error) {
		asked = repository
		return "ghs_scoped", nil
	}
	newTicket(t, e, "api")
	if request := assignRun(t, e); request.GitHubToken != "ghs_scoped" || asked != "owner/repo" {
		t.Fatalf("token = %q for %q", request.GitHubToken, asked)
	}
}

func TestAssignFailsTheJobWhenNoTokenCanBeMade(t *testing.T) {
	e := newEngine(t)
	e.GitHubToken = func(context.Context, string) (string, error) {
		return "", errors.New("the GitHub App is not installed on owner/repo")
	}
	ticket := newTicket(t, e, "api")
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if request, err := e.Assign(t.Context(), "runner-1", []string{"steps"}); err != nil || request != nil {
		t.Fatalf("request = %+v, err = %v", request, err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateFailed {
		t.Fatalf("state = %q", got)
	}
	if message := lastMessage(t, e, ticket.ID); !strings.Contains(message, "not installed on owner/repo") {
		t.Fatalf("message = %q", message)
	}
}

func TestAPresetTimeoutReplacesTheRunDeadline(t *testing.T) {
	e := newEngine(t)
	preset := testPreset()
	preset.Timeout = 2 * time.Minute
	e.Factory.Presets["default"] = preset
	newTicket(t, e, "api")
	if request := assignRun(t, e); request.TimeoutMillis != (2 * time.Minute).Milliseconds() {
		t.Fatalf("timeout = %d", request.TimeoutMillis)
	}
}

func TestAssignReturnsNothingWithoutMatchingWork(t *testing.T) {
	e := newEngine(t)
	newTicket(t, e, "api")
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if request, err := e.Assign(t.Context(), "runner-1", []string{"claude"}); err != nil || request != nil {
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
		{"pr ready with PR", succeed("pr_ready", "Opened"), found, tickets.StatePRReady, ""},
		{"pr ready without PR", succeed("pr_ready", "Opened"), nil, tickets.StateFailed, "Reported a PR but none is open for this branch."},
		{"pr ready with only a closed PR", succeed("pr_ready", "Opened"), &PR{Number: 42, Closed: true}, tickets.StateFailed, "Reported a PR but none is open for this branch."},
		{"no change", succeed("no_change", "Nothing to do"), nil, tickets.StateDone, "Nothing to do"},
		{"no change with an open PR", succeed("no_change", "Left open: needs a decision"), found, tickets.StatePRReady, "Left open: needs a decision"},
		{"no change with a merged PR", succeed("no_change", "Merged"), &PR{Number: 42, Merged: true}, tickets.StateDone, "Merged"},
		{"no change with a closed PR", succeed("no_change", "Closed"), &PR{Number: 42, Closed: true}, tickets.StateDone, "Closed"},
		{"failed report", succeed("failed", "Tests cannot run"), nil, tickets.StateFailed, "Tests cannot run"},
		{"no report", protocol.Completion{State: tickets.RunSucceeded}, nil, tickets.StateFailed, "The run ended without a report: succeeded"},
		{"malformed report", succeed("complete", "old"), nil, tickets.StateFailed, "The run ended without a report: report outcome"},
		{"run failed", protocol.Completion{State: tickets.RunFailed, Error: "exit 7"}, nil, tickets.StateFailed, "The run ended without a report: exit 7"},
		{"deadline", protocol.Completion{State: tickets.RunTimedOut, Error: "command exceeded timeout"}, nil, tickets.StateFailed, "The run ended without a report: command exceeded"},
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
	for _, completion := range []protocol.Completion{succeed("failed", "Broken"), succeed("pr_ready", "Opened"), {State: tickets.RunFailed, Error: "exit 1"}} {
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
	if err := e.Complete(t.Context(), request.RunID, succeed("no_change", "Done")); err != nil {
		t.Fatal(err)
	}
	if err := e.Complete(t.Context(), request.RunID, succeed("no_change", "Done")); !errors.Is(err, tickets.ErrRunNotActive) {
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
	if err := e.Complete(t.Context(), request.RunID, succeed("no_change", "Done")); err == nil {
		t.Fatal("lookup failure hidden")
	}
	if got := state(t, e, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
	if err := e.Complete(t.Context(), request.RunID, succeed("no_change", "Done")); err != nil {
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

func TestALostRunnerIsRetriedAtMostThreeTimes(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	for range maxAttempts {
		request := assignRun(t, e)
		if err := e.Complete(t.Context(), request.RunID, protocol.Completion{State: tickets.RunCancelled}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateFailed {
		t.Fatalf("state = %s", got)
	}
	if message := lastMessage(t, e, ticket.ID); !strings.Contains(message, "Stopped after 3 attempts") {
		t.Fatalf("message = %q", message)
	}
}

func TestEveryJobEndsDoneOrFailed(t *testing.T) {
	for _, outcome := range []string{"pr_ready", "no_change", "failed"} {
		e := newEngine(t)
		e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: 1}, nil }
		ticket := newTicket(t, e, "api")
		request := assignRun(t, e)
		if err := e.Complete(t.Context(), request.RunID, succeed(outcome, "Summary")); err != nil {
			t.Fatal(err)
		}
		if got := state(t, e, ticket.ID); got == tickets.StateWorking {
			t.Fatalf("%s: the job is still working", outcome)
		}
	}
	e := newEngine(t)
	newTicket(t, e, "api")
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, succeed("needs_input", "Which API?")); err != nil {
		t.Fatal(err)
	}
	if message := lastMessage(t, e, request.TicketID); !strings.Contains(message, "report outcome \"needs_input\" is not one of") {
		t.Fatalf("message = %q", message)
	}
}

func TestMissingPresetFailsTheJob(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	delete(e.Factory.Presets, "default")
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateFailed {
		t.Fatalf("state = %s", got)
	}
	if message := lastMessage(t, e, ticket.ID); !strings.Contains(message, "default") {
		t.Fatalf("message = %q", message)
	}
}

func TestPresetRemovedAfterQueueingFailsTheJob(t *testing.T) {
	e := newEngine(t)
	ticket := newTicket(t, e, "api")
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	delete(e.Factory.Presets, "default")
	if request, err := e.Assign(t.Context(), "runner-1", []string{"steps"}); err != nil || request != nil {
		t.Fatalf("request = %v, err = %v", request, err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateFailed {
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
