package tickets

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestADatabaseFromBeforePRStatesOpensAndKeepsItsJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE tickets (
  id TEXT PRIMARY KEY, source TEXT NOT NULL, repo TEXT NOT NULL, issue_number INTEGER, title TEXT NOT NULL,
  state TEXT NOT NULL, agent TEXT NOT NULL, pr_number INTEGER, created_by TEXT NOT NULL, human_commits INTEGER,
  merged_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO tickets VALUES ('t_old','api','owner/repo',NULL,'Old','pr_ready','default',7,'alice',NULL,NULL,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	for range 2 {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		open, err := store.TicketsWithOpenPRs(t.Context())
		store.Close()
		if err != nil || len(open) != 1 || open[0].ID != "t_old" || open[0].PRNumber != 7 || open[0].PRState != "" {
			t.Fatalf("open = %+v, err = %v", open, err)
		}
	}
}

func TestAPRResultFromBeforeARerunIsIgnored(t *testing.T) {
	store := openStore(t)
	now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	ticket := createTicket(t, store, "k1")
	ctx := context.Background()
	if err := store.Apply(ctx, ticket.ID, ticket.State, Transition{State: StatePRReady, PRNumber: 7}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.TicketsWithOpenPRs(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	// While GitHub is asked about #7, the job runs again and opens #8.
	now = now.Add(time.Minute)
	if _, err := store.Rerun(ctx, ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 8}); err != nil {
		t.Fatal(err)
	}
	if _, same, err := store.SetPRState(ctx, listed[0], PRUpdate{Number: 7, State: PRMerged, At: now}); err != nil || same {
		t.Fatalf("same = %v, err = %v", same, err)
	}
	got, err := store.Ticket(ctx, ticket.ID)
	if err != nil || got.State != StatePRReady || got.PRNumber != 8 || got.PRState != PROpen {
		t.Fatalf("ticket = %+v, err = %v", got, err)
	}
}

func TestALookAtChecksFromBeforeARerunIsIgnored(t *testing.T) {
	store := openStore(t)
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	ticket := createTicket(t, store, "k1")
	ctx := context.Background()
	if err := store.Apply(ctx, ticket.ID, ticket.State, Transition{State: StatePRReady, PRNumber: 7, Shipping: `{"since":"x"}`}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.TicketsWithOpenPRs(ctx)
	if err != nil || len(listed) != 1 || listed[0].Shipping != `{"since":"x"}` {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	// While the checks are read, the job runs again and opens #8, with
	// nothing handed over.
	now = now.Add(time.Minute)
	if _, err := store.Rerun(ctx, ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 8}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetShipping(ctx, listed[0], Shipped{Merged: true, At: now, Message: "Merged"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Ticket(ctx, ticket.ID)
	if err != nil || got.State != StatePRReady || got.PRNumber != 8 || got.MergedAt != nil || got.Shipping != "" {
		t.Fatalf("ticket = %+v, err = %v", got, err)
	}
}

func TestSetShippingKeepsTheWatchOrEndsTheJob(t *testing.T) {
	ctx := context.Background()
	for name, test := range map[string]struct {
		shipped Shipped
		state   string
		keep    string
	}{
		"waiting": {Shipped{Shipping: `{"updates":1}`}, StatePRReady, `{"updates":1}`},
		"left":    {Shipped{Message: "Left open"}, StatePRReady, ""},
		"merged":  {Shipped{Merged: true, At: time.Now(), Message: "Merged"}, StateDone, ""},
		"failed":  {Shipped{Failed: true, Message: "Failed"}, StateFailed, ""},
	} {
		t.Run(name, func(t *testing.T) {
			store := openStore(t)
			ticket := createTicket(t, store, "k1")
			if err := store.Apply(ctx, ticket.ID, ticket.State, Transition{State: StatePRReady, PRNumber: 7, Shipping: `{}`}); err != nil {
				t.Fatal(err)
			}
			listed, err := store.TicketsWithOpenPRs(ctx)
			if err != nil || len(listed) != 1 {
				t.Fatalf("listed = %+v, err = %v", listed, err)
			}
			if err := store.SetShipping(ctx, listed[0], test.shipped); err != nil {
				t.Fatal(err)
			}
			got, err := store.Ticket(ctx, ticket.ID)
			if err != nil || got.State != test.state || got.Shipping != test.keep || (test.shipped.Merged != (got.MergedAt != nil)) {
				t.Fatalf("ticket = %+v, err = %v", got, err)
			}
			if message, _ := store.LatestMessage(ctx, ticket.ID); message != test.shipped.Message {
				t.Fatalf("message = %q", message)
			}
		})
	}
}

// The job SetPRState returns carries the new pr_state_at, so a look at the
// checks right after it is still recorded.
func TestSetPRStateReturnsTheJobAsItNowStands(t *testing.T) {
	store := openStore(t)
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	ticket := createTicket(t, store, "k1")
	ctx := context.Background()
	if err := store.Apply(ctx, ticket.ID, ticket.State, Transition{State: StatePRReady, PRNumber: 7, Shipping: `{}`}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.TicketsWithOpenPRs(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	// GitHub now reports #8 on the branch.
	current, same, err := store.SetPRState(ctx, listed[0], PRUpdate{Number: 8, State: PROpen, At: now.Add(time.Minute)})
	if err != nil || !same || current.PRNumber != 8 || current.PRStateAt == nil || !current.PRStateAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("current = %+v, same = %v, err = %v", current, same, err)
	}
	if err := store.SetShipping(ctx, current, Shipped{Shipping: `{"updates":1}`}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Ticket(ctx, ticket.ID); err != nil || got.Shipping != `{"updates":1}` {
		t.Fatalf("ticket = %+v, err = %v", got, err)
	}
}
