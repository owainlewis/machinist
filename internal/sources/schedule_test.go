package sources

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/owainlewis/machinist/internal/triggers"
)

type clockedSchedule struct {
	schedule *Schedule
	store    *tickets.Store
	now      time.Time
}

func newSchedule(t *testing.T, expression string) *clockedSchedule {
	t.Helper()
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cron, err := triggers.ParseCron(expression, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	automation := factory.Automation{Name: "nightly", Repo: "owner/repo", Preset: "maintainer", Prompt: "THE PROMPT", Cron: cron}
	c := &clockedSchedule{schedule: NewSchedule(store, []factory.Automation{automation}, nil), store: store, now: time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)}
	c.schedule.now = func() time.Time { return c.now }
	store.SetClock(c.schedule.now)
	return c
}

func (c *clockedSchedule) poll(t *testing.T) {
	t.Helper()
	if err := c.schedule.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (c *clockedSchedule) tickets(t *testing.T) []tickets.Ticket {
	t.Helper()
	listed, err := c.store.ListTickets(t.Context(), tickets.TicketFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return listed
}

func TestFirstSightWaitsForTheNextOccurrence(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	c.poll(t)
	c.now = c.now.Add(30 * time.Minute)
	c.poll(t)
	if got := c.tickets(t); len(got) != 0 {
		t.Fatalf("tickets = %+v", got)
	}
	c.now = time.Date(2026, 3, 1, 9, 0, 10, 0, time.UTC)
	c.poll(t)
	got := c.tickets(t)
	if len(got) != 1 || got[0].Source != "schedule:nightly" || got[0].Origin != tickets.OriginSchedule || got[0].Agent != "maintainer" || got[0].Repo != "owner/repo" || got[0].CreatedBy != "schedule" {
		t.Fatalf("tickets = %+v", got)
	}
	entries, _ := c.store.Entries(t.Context(), got[0].ID)
	if entries[0].Body != "THE PROMPT" {
		t.Fatalf("entries = %+v", entries)
	}
}

func endAll(t *testing.T, c *clockedSchedule) {
	t.Helper()
	for _, ticket := range c.tickets(t) {
		if ticket.State == tickets.StateWorking {
			if _, err := c.store.Cancel(t.Context(), ticket.ID, "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMissedOccurrencesCollapseIntoOne(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	c.poll(t)
	c.store.SetCursor(t.Context(), scheduleCursor, "nightly", c.now)
	c.now = c.now.Add(5 * 24 * time.Hour)
	c.poll(t)
	endAll(t, c)
	c.poll(t)
	if got := c.tickets(t); len(got) != 1 {
		t.Fatalf("tickets = %+v", got)
	}
}

func TestNoTicketStartsWhileTheLastIsActive(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	c.poll(t)
	c.now = time.Date(2026, 3, 1, 9, 0, 1, 0, time.UTC)
	c.poll(t)
	c.now = c.now.Add(48 * time.Hour)
	c.poll(t)
	if got := c.tickets(t); len(got) != 1 {
		t.Fatalf("tickets = %+v", got)
	}
	endAll(t, c)
	c.poll(t)
	if got := c.tickets(t); len(got) != 2 {
		t.Fatalf("the due run never started: %+v", got)
	}
}

func TestRepeatedPollCreatesOneTicket(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	c.poll(t)
	c.now = time.Date(2026, 3, 1, 9, 0, 1, 0, time.UTC)
	for range 3 {
		c.poll(t)
	}
	if got := c.tickets(t); len(got) != 1 {
		t.Fatalf("tickets = %+v", got)
	}
}

func TestRunNowReturnsTheActiveTicket(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	first, created, err := c.schedule.RunNow(t.Context(), "nightly", "alice", RunOverrides{})
	if err != nil || !created || first.CreatedBy != "alice" {
		t.Fatalf("ticket = %+v, created = %v, err = %v", first, created, err)
	}
	again, created, err := c.schedule.RunNow(t.Context(), "nightly", "bob", RunOverrides{})
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("ticket = %+v, created = %v, err = %v", again, created, err)
	}
	if _, _, err := c.schedule.RunNow(t.Context(), "missing", "alice", RunOverrides{}); !errors.Is(err, tickets.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunNowPromptOverrideAppliesOnlyToThatRun(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	custom, created, err := c.schedule.RunNow(t.Context(), "nightly", "alice", RunOverrides{Prompt: "ONE-OFF PROMPT"})
	if err != nil || !created {
		t.Fatalf("ticket = %+v, created = %v, err = %v", custom, created, err)
	}
	entries, err := c.store.Entries(t.Context(), custom.ID)
	if err != nil || len(entries) != 1 || entries[0].Body != "ONE-OFF PROMPT" {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	endAll(t, c)
	c.now = c.now.Add(time.Second)

	statuses, err := c.schedule.Status(t.Context())
	if err != nil || len(statuses) != 1 || statuses[0].Prompt != "THE PROMPT" {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
	saved, created, err := c.schedule.RunNow(t.Context(), "nightly", "alice", RunOverrides{})
	if err != nil || !created {
		t.Fatalf("ticket = %+v, created = %v, err = %v", saved, created, err)
	}
	entries, err = c.store.Entries(t.Context(), saved.ID)
	if err != nil || len(entries) != 1 || entries[0].Body != "THE PROMPT" {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
}

func TestStatusShowsNextDueAndLastTicket(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	statuses, err := c.schedule.Status(t.Context())
	if err != nil || len(statuses) != 1 || statuses[0].LastTicket != nil || statuses[0].Schedule != "0 9 * * *" || statuses[0].Timezone != "UTC" || statuses[0].Prompt != "THE PROMPT" || statuses[0].Editable {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
	if want := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC); statuses[0].NextDue == nil || !statuses[0].NextDue.Equal(want) {
		t.Fatalf("next = %v", statuses[0].NextDue)
	}
	ticket, _, err := c.schedule.RunNow(t.Context(), "nightly", "alice", RunOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed, Message: "Could not reach the API"}); err != nil {
		t.Fatal(err)
	}
	statuses, err = c.schedule.Status(t.Context())
	if err != nil || statuses[0].LastTicket == nil || statuses[0].LastTicket.ID != ticket.ID || !strings.Contains(statuses[0].LastTicket.Outcome, "Could not reach") {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
}

func TestStoredManualAutomationAndRunOverrides(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	if _, err := c.store.CreateAutomation(t.Context(), tickets.Automation{
		Name:    "adhoc-audit",
		Repo:    "owner/app",
		Preset:  "default",
		Harness: "claude",
		Model:   "opus",
		Prompt:  "Audit security.",
	}); err != nil {
		t.Fatal(err)
	}

	// Poll ignores manual-only automations without a schedule.
	c.poll(t)
	c.now = c.now.Add(24 * time.Hour)
	c.poll(t)
	for _, ticket := range c.tickets(t) {
		if ticket.Source == "schedule:adhoc-audit" {
			t.Fatalf("manual automation should not be started by Poll: %+v", ticket)
		}
	}

	statuses, err := c.schedule.Status(t.Context())
	if err != nil || len(statuses) != 2 {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
	if statuses[0].Name != "adhoc-audit" || !statuses[0].Editable || statuses[0].NextDue != nil || statuses[0].Schedule != "" || statuses[0].Harness != "claude" || statuses[0].Model != "opus" {
		t.Fatalf("manual status = %+v", statuses[0])
	}

	// Switching harness without specifying model drops the saved model for the old harness.
	ticket, created, err := c.schedule.RunNow(t.Context(), "adhoc-audit", "alice", RunOverrides{
		Repo:    "owner/other",
		Preset:  "neon",
		Harness: "codex",
		Prompt:  "Audit auth only.",
	})
	if err != nil || !created {
		t.Fatalf("run = %+v, %v, %v", ticket, created, err)
	}
	if ticket.Repo != "owner/other" || ticket.Agent != "neon" || ticket.Harness != "codex" || ticket.Model != "" {
		t.Fatalf("ticket overrides = %+v", ticket)
	}
}

func TestDisconnectedProjectStopsScheduledAndManualRuns(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	c.schedule.Automations = nil
	c.schedule.Intake = &Intake{Store: c.store}

	if err := c.store.AddProject(t.Context(), "owner/dynamic", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.store.CreateAutomation(t.Context(), tickets.Automation{
		Name:     "nightly-dynamic",
		Repo:     "owner/dynamic",
		Preset:   "default",
		Prompt:   "Check dynamic repo.",
		Schedule: "0 9 * * *",
		Timezone: "UTC",
	}); err != nil {
		t.Fatal(err)
	}

	// First poll seeds the cursor; removing the project before the next due
	// occurrence must block both scheduled polls and manual runs.
	c.poll(t)
	if removed, err := c.store.RemoveProject(t.Context(), "owner/dynamic"); err != nil || !removed {
		t.Fatalf("remove project = %v, %v", removed, err)
	}

	c.now = time.Date(2026, 3, 1, 9, 0, 10, 0, time.UTC)
	if err := c.schedule.Poll(t.Context()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Poll err = %v, want ErrInvalidRequest", err)
	}
	if got := c.tickets(t); len(got) != 0 {
		t.Fatalf("disconnected scheduled poll created tickets: %+v", got)
	}
	if _, _, err := c.schedule.RunNow(t.Context(), "nightly-dynamic", "alice", RunOverrides{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("RunNow err = %v, want ErrInvalidRequest", err)
	}
	if got := c.tickets(t); len(got) != 0 {
		t.Fatalf("disconnected RunNow created tickets: %+v", got)
	}
}
