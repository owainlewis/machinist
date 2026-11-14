package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/tickets"
)

// CUJ 10: another runner takes over after the lost runner's lease expires.
func TestLostLeaseInterruptsRunAndQueuesANewOne(t *testing.T) {
	e := newEngine(t)
	now := time.Now()
	e.now = func() time.Time { return now.Add(-LeaseDuration - time.Minute) }
	ticket := newTicket(t, e, "api")
	lost := assignRun(t, e)
	e.now = time.Now

	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	run, err := e.Store.Run(t.Context(), lost.RunID)
	if err != nil || run.Status != tickets.RunInterrupted {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
	replacement, err := e.Assign(t.Context(), "runner-2", []string{"claude"})
	if err != nil || replacement == nil || replacement.RunID == lost.RunID || replacement.Branch != lost.Branch {
		t.Fatalf("replacement = %+v, err = %v", replacement, err)
	}
	if err := e.Complete(t.Context(), lost.RunID, succeed("no_change", "Late", "")); err == nil {
		t.Fatal("expired run completed")
	}
}

func TestHeartbeatKeepsLease(t *testing.T) {
	e := newEngine(t)
	newTicket(t, e, "api")
	now := time.Now()
	e.now = func() time.Time { return now.Add(-LeaseDuration + time.Minute) }
	request := assignRun(t, e)
	e.now = time.Now
	if _, err := e.Heartbeat(t.Context(), request.RunID); err != nil {
		t.Fatal(err)
	}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	run, _ := e.Store.Run(t.Context(), request.RunID)
	if run.Status != tickets.RunRunning {
		t.Fatalf("status = %s", run.Status)
	}
}

func TestLeasesSurviveControlPlaneRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	store, err := tickets.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	agents := map[string]factory.Agent{"default": {Name: "default", Harness: "claude", Playbook: "P"}}
	options := Options{Store: store, Factory: &factory.Factory{Agents: agents}, MaxTurns: 3, RunTimeout: time.Hour, Handle: "bot"}
	first := New(options)
	if _, _, err = store.CreateTicket(t.Context(), tickets.NewTicket{Source: "api", Repo: "o/r", Title: "T", Body: "B", Agent: "default", CreatedBy: "a", SourceKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if err = first.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	request, err := first.Assign(t.Context(), "runner-1", []string{"claude"})
	if err != nil || request == nil {
		t.Fatalf("request = %v, err = %v", request, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := tickets.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	options.Store = reopened
	second := New(options)
	if err = second.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = second.Heartbeat(t.Context(), request.RunID); err != nil {
		t.Fatalf("lease lost in restart: %v", err)
	}
	if err = second.Complete(t.Context(), request.RunID, protocol.Completion{State: tickets.RunSucceeded, Report: &protocol.Report{Outcome: "no_change", Summary: "Done"}}); err != nil {
		t.Fatal(err)
	}
}
