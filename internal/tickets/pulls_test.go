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
	if _, _, err := store.Apply(ctx, ticket.ID, ticket.State, Transition{State: StatePRReady, PRNumber: 7}); err != nil {
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
	if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 8}); err != nil {
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
	if _, _, err := store.Apply(ctx, ticket.ID, ticket.State, Transition{State: StatePRReady, PRNumber: 7, Shipping: `{"since":"x"}`}); err != nil {
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
	if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 8}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetShipping(ctx, listed[0], Shipped{Merged: true, At: now, Message: "Merged"}); err != nil {
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
			if _, _, err := store.Apply(ctx, ticket.ID, ticket.State, Transition{State: StatePRReady, PRNumber: 7, Shipping: `{}`}); err != nil {
				t.Fatal(err)
			}
			listed, err := store.TicketsWithOpenPRs(ctx)
			if err != nil || len(listed) != 1 {
				t.Fatalf("listed = %+v, err = %v", listed, err)
			}
			if _, _, err := store.SetShipping(ctx, listed[0], test.shipped); err != nil {
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
	if _, _, err := store.Apply(ctx, ticket.ID, ticket.State, Transition{State: StatePRReady, PRNumber: 7, Shipping: `{}`}); err != nil {
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
	if _, _, err := store.SetShipping(ctx, current, Shipped{Shipping: `{"updates":1}`}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Ticket(ctx, ticket.ID); err != nil || got.Shipping != `{"updates":1}` {
		t.Fatalf("ticket = %+v, err = %v", got, err)
	}
}

func TestADatabaseFromBeforeMergeGateColumnsMigratesCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE tickets (
  id TEXT PRIMARY KEY, source TEXT NOT NULL, repo TEXT NOT NULL, issue_number INTEGER, title TEXT NOT NULL,
  state TEXT NOT NULL, agent TEXT NOT NULL, pr_number INTEGER, created_by TEXT NOT NULL, human_commits INTEGER,
  merged_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, pr_state TEXT, pr_state_at TEXT,
  origin TEXT NOT NULL DEFAULT '', source_url TEXT NOT NULL DEFAULT '', shipping TEXT,
  pr_opened_at TEXT, first_review_at TEXT, human_reviews INTEGER);
INSERT INTO tickets(id,source,repo,title,state,agent,pr_number,created_by,created_at,updated_at,pr_state,origin)
VALUES ('t_legacy','api','owner/repo','Legacy PR','pr_ready','default',12,'alice','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z','open','api');`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	// Opening twice verifies the ALTER TABLE migration is idempotent.
	for range 2 {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.Ticket(t.Context(), "t_legacy")
		store.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != "t_legacy" || got.PRNumber != 12 || got.MergeGateDecision != "" || got.MergeGateRisk != "" || got.MergeGateReason != "" {
			t.Fatalf("legacy ticket = %+v", got)
		}
	}

	// Migrated database accepts and returns merge-gate verdicts.
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.Apply(t.Context(), "t_legacy", StatePRReady, Transition{
		State:     StatePRReady,
		PRNumber:  12,
		MergeGate: &MergeGate{Decision: "human_review", Risk: "high", Reason: "Touches auth middleware."},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Ticket(t.Context(), "t_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeGateDecision != "human_review" || got.MergeGateRisk != "high" || got.MergeGateReason != "Touches auth middleware." {
		t.Fatalf("after update = %+v", got)
	}
}

func TestMergeGateVerdictsPersistAcrossLevelsAndClearOnRerun(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		decision string
		risk     string
		reason   string
	}{
		{"low", "approve", "low", "Adds unit tests only."},
		{"medium", "approve", "medium", "Adds new API response fields."},
		{"high", "human_review", "high", "Drops a database table."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t)
			now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
			store.SetClock(func() time.Time { return now })
			ticket := createTicket(t, store, "k-"+tc.name)

			if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{
				State:     StatePRReady,
				PRNumber:  15,
				Shipping:  `{}`,
				MergeGate: &MergeGate{Decision: tc.decision, Risk: tc.risk, Reason: tc.reason},
			}); err != nil {
				t.Fatal(err)
			}

			got, err := store.Ticket(ctx, ticket.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.MergeGateDecision != tc.decision || got.MergeGateRisk != tc.risk || got.MergeGateReason != tc.reason {
				t.Fatalf("Ticket() = %+v, want %s/%s/%q", got, tc.decision, tc.risk, tc.reason)
			}

			// ListTickets returns the persisted merge-gate fields too.
			listed, err := store.ListTickets(ctx, TicketFilter{})
			if err != nil || len(listed) != 1 || listed[0].MergeGateDecision != tc.decision || listed[0].MergeGateRisk != tc.risk || listed[0].MergeGateReason != tc.reason {
				t.Fatalf("ListTickets() = %+v, err = %v", listed, err)
			}

			// Merging the PR keeps the recorded merge-gate verdict.
			if _, _, err := store.SetShipping(ctx, got, Shipped{Merged: true, At: now.Add(time.Minute), Message: "Merged"}); err != nil {
				t.Fatal(err)
			}
			merged, err := store.Ticket(ctx, ticket.ID)
			if err != nil {
				t.Fatal(err)
			}
			if merged.State != StateDone || merged.MergeGateDecision != tc.decision || merged.MergeGateRisk != tc.risk || merged.MergeGateReason != tc.reason {
				t.Fatalf("merged ticket lost merge-gate verdict: %+v", merged)
			}

			// Rerunning clears the stale merge-gate verdict.
			rerun, err := store.Rerun(ctx, ticket.ID, "alice")
			if err != nil {
				t.Fatal(err)
			}
			if rerun.MergeGateDecision != "" || rerun.MergeGateRisk != "" || rerun.MergeGateReason != "" {
				t.Fatalf("rerun kept stale merge-gate verdict: %+v", rerun)
			}
		})
	}
}
