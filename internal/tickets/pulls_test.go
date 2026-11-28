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

func TestStewardRequeuePersistsCountAcrossRestartsAndSkipsHumanTouches(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tickets.db")
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.SetClock(func() time.Time { return now })

	ticket := createTicket(t, store, "k-steward")
	if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{
		State:     StatePRReady,
		PRNumber:  42,
		Shipping:  `{"head":"sha1"}`,
		MergeGate: &MergeGate{Decision: "approve", Risk: "low", Reason: "Safe."},
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Attempt 1/2 succeeds, clears shipping & stale merge gate, keeps PRNumber,
	// increments steward_retries, records EntryCI, and adds no human_touches.
	now = now.Add(time.Minute)
	updated, requeued, err := store.StewardRequeue(ctx, listed, StewardRequeueRequest{
		MaxRetries: 2,
		Prompt:     `PR Steward (attempt 1/2): check "test" failed.`,
	})
	if err != nil || !requeued {
		t.Fatalf("attempt 1: requeued = %v, err = %v", requeued, err)
	}
	if updated.State != StateWorking || updated.Shipping != "" || updated.PRNumber != 42 || updated.StewardRetries != 1 || updated.MergeGateDecision != "" {
		t.Fatalf("updated = %+v", updated)
	}
	entries, err := store.Entries(ctx, ticket.ID)
	if err != nil || len(entries) < 2 {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	last := entries[len(entries)-1]
	if last.Kind != EntryCI || last.Author != "machinist" || last.Body != `PR Steward (attempt 1/2): check "test" failed.` {
		t.Fatalf("last entry = %+v", last)
	}
	// TurnsSinceHuman resets on EntryCI so lost-runner counting starts fresh.
	if turns, err := store.TurnsSinceHuman(ctx, ticket.ID); err != nil || turns != 0 {
		t.Fatalf("TurnsSinceHuman = %d, err = %v", turns, err)
	}

	// Simulate control-plane restart by closing and reopening the SQLite store.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetClock(func() time.Time { return now })

	reloaded, err := store.Ticket(ctx, ticket.ID)
	if err != nil || reloaded.StewardRetries != 1 {
		t.Fatalf("reloaded = %+v, err = %v", reloaded, err)
	}

	// Complete steward run 1 and hand off PR again.
	now = now.Add(time.Minute)
	if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{
		State:    StatePRReady,
		PRNumber: 42,
		Shipping: `{"head":"sha2"}`,
	}); err != nil {
		t.Fatal(err)
	}
	listed, err = store.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Attempt 2/2 succeeds.
	now = now.Add(time.Minute)
	updated, requeued, err = store.StewardRequeue(ctx, listed, StewardRequeueRequest{
		MaxRetries: 2,
		Prompt:     `PR Steward (attempt 2/2): check "test" failed.`,
	})
	if err != nil || !requeued || updated.StewardRetries != 2 {
		t.Fatalf("attempt 2: updated = %+v, requeued = %v, err = %v", updated, requeued, err)
	}

	// Hand off PR again and verify attempt 3/2 is rejected at the cap.
	now = now.Add(time.Minute)
	if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{
		State:    StatePRReady,
		PRNumber: 42,
		Shipping: `{"head":"sha3"}`,
	}); err != nil {
		t.Fatal(err)
	}
	listed, err = store.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, requeued, err = store.StewardRequeue(ctx, listed, StewardRequeueRequest{
		MaxRetries: 2,
		Prompt:     "should not requeue",
	})
	if err != nil || requeued {
		t.Fatalf("beyond cap: requeued = %v, err = %v", requeued, err)
	}

	// Merging records 0 human rerun touches.
	now = now.Add(time.Minute)
	if _, _, err := store.SetShipping(ctx, listed, Shipped{Merged: true, At: now, Message: "Merged"}); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Stats(ctx, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Touches.Reruns != 0 || stats.Touches.Replies != 0 {
		t.Fatalf("human touches inflated by steward: %+v", stats.Touches)
	}

	// A human rerun resets steward_retries to 0.
	rerun, err := store.Rerun(ctx, ticket.ID, "alice")
	if err != nil || rerun.StewardRetries != 0 {
		t.Fatalf("human rerun = %+v, err = %v", rerun, err)
	}
}

func TestStewardRequeueIgnoresStaleGeneration(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	ticket := createTicket(t, store, "k-stale")
	if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 7, Shipping: `{}`}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.Ticket(ctx, ticket.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Human reruns and opens a new generation before steward writes.
	now = now.Add(time.Minute)
	if _, err := store.Rerun(ctx, ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 7, Shipping: `{}`}); err != nil {
		t.Fatal(err)
	}

	_, requeued, err := store.StewardRequeue(ctx, listed, StewardRequeueRequest{MaxRetries: 2, Prompt: "stale"})
	if err != nil || requeued {
		t.Fatalf("stale generation: requeued = %v, err = %v", requeued, err)
	}
	got, err := store.Ticket(ctx, ticket.ID)
	if err != nil || got.StewardRetries != 0 || got.State != StatePRReady {
		t.Fatalf("ticket = %+v, err = %v", got, err)
	}
}

func TestStewardRequeueDefersOnScheduleContention(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })

	firstReq := request("sched:steward:1")
	firstReq.Source = "schedule:nightly"
	first, created, err := store.CreateTicket(ctx, firstReq)
	if err != nil || !created {
		t.Fatalf("create first schedule ticket: created=%v err=%v", created, err)
	}
	if _, _, err := store.Apply(ctx, first.ID, StateWorking, Transition{
		State:    StatePRReady,
		PRNumber: 42,
		Shipping: `{"head":"sha1"}`,
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.Ticket(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Start another working ticket on the same schedule.
	secondReq := request("sched:steward:2")
	secondReq.Source = "schedule:nightly"
	second, created, err := store.CreateTicket(ctx, secondReq)
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("create second schedule ticket: second=%+v created=%v err=%v", second, created, err)
	}

	// StewardRequeue must defer cleanly without surfacing a DB constraint error
	// and without mutating the pr_ready ticket or inserting an EntryCI.
	now = now.Add(time.Minute)
	deferred, requeued, err := store.StewardRequeue(ctx, listed, StewardRequeueRequest{
		MaxRetries: 2,
		Prompt:     `PR Steward (attempt 1/2): check "test" failed.`,
	})
	if err != nil || requeued {
		t.Fatalf("StewardRequeue during schedule contention: deferred=%+v requeued=%v err=%v, want requeued=false err=nil", deferred, requeued, err)
	}
	if deferred.ID != first.ID || deferred.State != StatePRReady || deferred.Shipping != `{"head":"sha1"}` || deferred.StewardRetries != 0 {
		t.Fatalf("deferred ticket = %+v", deferred)
	}
	entries, err := store.Entries(ctx, first.ID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries after deferred StewardRequeue = %+v, err=%v", entries, err)
	}

	// Once the other scheduled ticket leaves working, StewardRequeue succeeds.
	if _, _, err := store.Apply(ctx, second.ID, StateWorking, Transition{State: StateDone}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	updated, requeued, err := store.StewardRequeue(ctx, listed, StewardRequeueRequest{
		MaxRetries: 2,
		Prompt:     `PR Steward (attempt 1/2): check "test" failed.`,
	})
	if err != nil || !requeued {
		t.Fatalf("StewardRequeue after contention cleared: updated=%+v requeued=%v err=%v", updated, requeued, err)
	}
	if updated.State != StateWorking || updated.Shipping != "" || updated.StewardRetries != 1 {
		t.Fatalf("updated = %+v", updated)
	}
	entries, err = store.Entries(ctx, first.ID)
	if err != nil || len(entries) != 2 || entries[1].Kind != EntryCI {
		t.Fatalf("entries after successful StewardRequeue = %+v, err=%v", entries, err)
	}
}

func TestTicketsWithOpenPRsIncludesFailedAndCancelledOpenPRs(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	nextTicket := func(key string) Ticket {
		now = now.Add(time.Second)
		return createTicket(t, store, key)
	}

	// pr_ready with open PR: included.
	prReady := nextTicket("k-pr-ready")
	if _, _, err := store.Apply(ctx, prReady.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 10}); err != nil {
		t.Fatal(err)
	}

	// failed after shipping check failed (pr_state = 'open'): included.
	failedOpen := nextTicket("k-failed-open")
	if _, _, err := store.Apply(ctx, failedOpen.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 11, Shipping: `{}`}); err != nil {
		t.Fatal(err)
	}
	failedListed, err := store.Ticket(ctx, failedOpen.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetShipping(ctx, failedListed, Shipped{Failed: true, Message: "check failed"}); err != nil {
		t.Fatal(err)
	}

	// failed with PR number and unset pr_state: included.
	failedUnset := nextTicket("k-failed-unset")
	if _, _, err := store.Apply(ctx, failedUnset.ID, StateWorking, Transition{State: StateFailed, PRNumber: 12, Message: "run failed"}); err != nil {
		t.Fatal(err)
	}

	// cancelled with open PR: included.
	cancelledOpen := nextTicket("k-cancelled-open")
	if _, _, err := store.Apply(ctx, cancelledOpen.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 13}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(ctx, cancelledOpen.ID, "alice"); err != nil {
		t.Fatal(err)
	}

	// failed without PR: excluded.
	failedNoPR := nextTicket("k-failed-no-pr")
	if _, _, err := store.Apply(ctx, failedNoPR.ID, StateWorking, Transition{State: StateFailed, Message: "no pr"}); err != nil {
		t.Fatal(err)
	}

	// cancelled without PR: excluded.
	cancelledNoPR := nextTicket("k-cancelled-no-pr")
	if _, err := store.Cancel(ctx, cancelledNoPR.ID, "alice"); err != nil {
		t.Fatal(err)
	}

	// working with existing PR (rerun): excluded.
	workingWithPR := nextTicket("k-working-pr")
	if _, _, err := store.Apply(ctx, workingWithPR.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 14}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rerun(ctx, workingWithPR.ID, "alice"); err != nil {
		t.Fatal(err)
	}

	// done with merged PR: excluded.
	doneMerged := nextTicket("k-done-merged")
	mergedAt := now
	if _, _, err := store.Apply(ctx, doneMerged.ID, StateWorking, Transition{State: StateDone, PRNumber: 15, MergedAt: &mergedAt}); err != nil {
		t.Fatal(err)
	}

	open, err := store.TicketsWithOpenPRs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := make([]string, len(open))
	for i, ticket := range open {
		gotIDs[i] = ticket.ID
	}
	wantIDs := []string{prReady.ID, failedOpen.ID, failedUnset.ID, cancelledOpen.ID}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("TicketsWithOpenPRs() = %v, want %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("TicketsWithOpenPRs()[%d] = %s, want %s (all: %v)", i, gotIDs[i], wantIDs[i], gotIDs)
		}
	}
}

func TestSetPRStateReconcilesFailedAndCancelledJobs(t *testing.T) {
	ctx := context.Background()

	t.Run("failed merged transitions to done with reviews and leaves open list", func(t *testing.T) {
		store := openStore(t)
		now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
		store.SetClock(func() time.Time { return now })

		ticket := createTicket(t, store, "k-failed-merged")
		if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 710, Shipping: `{}`}); err != nil {
			t.Fatal(err)
		}
		ready, err := store.Ticket(ctx, ticket.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SetShipping(ctx, ready, Shipped{Failed: true, Message: "CI failed"}); err != nil {
			t.Fatal(err)
		}

		open, err := store.TicketsWithOpenPRs(ctx)
		if err != nil || len(open) != 1 || open[0].ID != ticket.ID || open[0].State != StateFailed {
			t.Fatalf("open = %+v, err = %v", open, err)
		}

		opened := now.Add(-2 * time.Hour)
		reviewed := now.Add(-time.Hour)
		merged := now.Add(30 * time.Minute)
		updated, same, err := store.SetPRState(ctx, open[0], PRUpdate{
			Number:   710,
			State:    PRMerged,
			At:       merged,
			OpenedAt: opened,
			Reviews:  &Reviews{Count: 2, First: &reviewed},
		})
		if err != nil || !same {
			t.Fatalf("SetPRState: same = %v, err = %v", same, err)
		}
		if updated.State != StateDone || updated.PRState != PRMerged || updated.PRStateAt == nil || !updated.PRStateAt.Equal(merged) || updated.MergedAt == nil || !updated.MergedAt.Equal(merged) || updated.Shipping != "" {
			t.Fatalf("updated = %+v", updated)
		}
		stats, err := store.Stats(ctx, 7, "")
		if err != nil {
			t.Fatal(err)
		}
		if stats.Cycle.Merged != 1 || stats.Cycle.Reviewed != 1 || stats.Touches.Reviews != 2 {
			t.Fatalf("stats: cycle = %+v, touches = %+v", stats.Cycle, stats.Touches)
		}
		remaining, err := store.TicketsWithOpenPRs(ctx)
		if err != nil || len(remaining) != 0 {
			t.Fatalf("remaining = %+v, err = %v", remaining, err)
		}
	})

	t.Run("failed closed stays failed and leaves open list", func(t *testing.T) {
		store := openStore(t)
		now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
		store.SetClock(func() time.Time { return now })

		ticket := createTicket(t, store, "k-failed-closed")
		if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 65, Shipping: `{}`}); err != nil {
			t.Fatal(err)
		}
		ready, err := store.Ticket(ctx, ticket.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.SetShipping(ctx, ready, Shipped{Failed: true, Message: "CI failed"}); err != nil {
			t.Fatal(err)
		}

		open, err := store.TicketsWithOpenPRs(ctx)
		if err != nil || len(open) != 1 {
			t.Fatalf("open = %+v, err = %v", open, err)
		}

		closed := now.Add(15 * time.Minute)
		updated, same, err := store.SetPRState(ctx, open[0], PRUpdate{Number: 65, State: PRClosed, At: closed})
		if err != nil || !same {
			t.Fatalf("SetPRState: same = %v, err = %v", same, err)
		}
		if updated.State != StateFailed || updated.PRState != PRClosed || updated.PRStateAt == nil || !updated.PRStateAt.Equal(closed) || updated.MergedAt != nil {
			t.Fatalf("updated = %+v", updated)
		}
		remaining, err := store.TicketsWithOpenPRs(ctx)
		if err != nil || len(remaining) != 0 {
			t.Fatalf("remaining = %+v, err = %v", remaining, err)
		}
		// A repeat call against the already-closed PR is ignored.
		if _, same, err := store.SetPRState(ctx, open[0], PRUpdate{Number: 65, State: PRClosed, At: closed}); err != nil || same {
			t.Fatalf("repeat SetPRState: same = %v, err = %v", same, err)
		}
	})

	for name, prState := range map[string]string{
		"cancelled merged": PRMerged,
		"cancelled closed": PRClosed,
	} {
		t.Run(name, func(t *testing.T) {
			store := openStore(t)
			now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
			store.SetClock(func() time.Time { return now })

			ticket := createTicket(t, store, "k-"+name)
			if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 77}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Cancel(ctx, ticket.ID, "alice"); err != nil {
				t.Fatal(err)
			}

			open, err := store.TicketsWithOpenPRs(ctx)
			if err != nil || len(open) != 1 || open[0].State != StateCancelled {
				t.Fatalf("open = %+v, err = %v", open, err)
			}

			at := now.Add(20 * time.Minute)
			updated, same, err := store.SetPRState(ctx, open[0], PRUpdate{Number: 77, State: prState, At: at})
			if err != nil || !same {
				t.Fatalf("SetPRState: same = %v, err = %v", same, err)
			}
			wantMerged := prState == PRMerged
			if updated.State != StateCancelled || updated.PRState != prState || updated.PRStateAt == nil || !updated.PRStateAt.Equal(at) || (updated.MergedAt != nil) != wantMerged {
				t.Fatalf("updated = %+v", updated)
			}
			if wantMerged && !updated.MergedAt.Equal(at) {
				t.Fatalf("merged_at = %v, want %v", updated.MergedAt, at)
			}
			remaining, err := store.TicketsWithOpenPRs(ctx)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("remaining = %+v, err = %v", remaining, err)
			}
		})
	}

	t.Run("cancelled while listed as pr_ready is treated as changed", func(t *testing.T) {
		store := openStore(t)
		now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
		store.SetClock(func() time.Time { return now })

		ticket := createTicket(t, store, "k-race-cancel")
		if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 88, Shipping: `{}`}); err != nil {
			t.Fatal(err)
		}
		open, err := store.TicketsWithOpenPRs(ctx)
		if err != nil || len(open) != 1 {
			t.Fatalf("open = %+v, err = %v", open, err)
		}
		if _, err := store.Cancel(ctx, ticket.ID, "alice"); err != nil {
			t.Fatal(err)
		}
		got, same, err := store.SetPRState(ctx, open[0], PRUpdate{Number: 88, State: PROpen, At: now.Add(time.Minute)})
		if err != nil || same || got.State != StateCancelled {
			t.Fatalf("got = %+v, same = %v, err = %v", got, same, err)
		}
	})
}
