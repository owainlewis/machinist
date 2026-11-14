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
	automation := factory.Automation{Name: "nightly", Repo: "owner/repo", Agent: "maintainer", Prompt: "THE PROMPT", Cron: cron}
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
	listed, err := c.store.ListTickets(t.Context(), "", "")
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
	if len(got) != 1 || got[0].Source != "schedule:nightly" || got[0].Agent != "maintainer" || got[0].Repo != "owner/repo" || got[0].CreatedBy != "schedule" {
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
	first, created, err := c.schedule.RunNow(t.Context(), "nightly", "alice")
	if err != nil || !created || first.CreatedBy != "alice" {
		t.Fatalf("ticket = %+v, created = %v, err = %v", first, created, err)
	}
	again, created, err := c.schedule.RunNow(t.Context(), "nightly", "bob")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("ticket = %+v, created = %v, err = %v", again, created, err)
	}
	if _, _, err := c.schedule.RunNow(t.Context(), "missing", "alice"); !errors.Is(err, tickets.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestStatusShowsNextDueAndLastTicket(t *testing.T) {
	c := newSchedule(t, "0 9 * * *")
	statuses, err := c.schedule.Status(t.Context())
	if err != nil || len(statuses) != 1 || statuses[0].LastTicket != nil || statuses[0].Schedule != "0 9 * * *" || statuses[0].Timezone != "UTC" {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
	if want := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC); !statuses[0].NextDue.Equal(want) {
		t.Fatalf("next = %v", statuses[0].NextDue)
	}
	ticket, _, err := c.schedule.RunNow(t.Context(), "nightly", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed, Message: "Could not reach the API"}); err != nil {
		t.Fatal(err)
	}
	statuses, err = c.schedule.Status(t.Context())
	if err != nil || statuses[0].LastTicket == nil || statuses[0].LastTicket.ID != ticket.ID || !strings.Contains(statuses[0].LastTicket.Outcome, "Could not reach") {
		t.Fatalf("statuses = %+v, err = %v", statuses, err)
	}
}
