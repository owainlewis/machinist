package tickets

import (
	"context"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

func finishRun(t *testing.T, store *Store, ticketID string, report *protocol.Report, tokens int64, cost *float64) string {
	t.Helper()
	if _, err := store.QueueRun(t.Context(), ticketID, "claude"); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(t.Context(), "runner", []string{"claude"}, time.Now().Add(time.Hour), nil)
	if err != nil || claim == nil {
		t.Fatalf("claim = %v, err = %v", claim, err)
	}
	_, _, err = store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded, Report: report, Tokens: &tokens, Cost: cost}, func(Ticket, string) Transition {
		return Transition{State: StatePRReady}
	})
	if err != nil {
		t.Fatal(err)
	}
	return claim.Run.ID
}

func TestLedgerAveragesMergedHumanTickets(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	cost := 2.0
	merged := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	first := createTicket(t, store, "api:first")
	finishRun(t, store, first.ID, &protocol.Report{Outcome: "pr_ready", Summary: "s"}, 100, &cost)
	if _, _, err := store.AddInput(ctx, first.ID, Input{Kind: EntryComment, Author: "alice", Body: "answer"}); err != nil {
		t.Fatal(err)
	}
	finishRun(t, store, first.ID, &protocol.Report{Outcome: "pr_ready", Summary: "s"}, 300, &cost)
	if err := markMerged(store, first.ID, 3, merged); err != nil {
		t.Fatal(err)
	}

	second := createTicket(t, store, "api:second")
	finishRun(t, store, second.ID, &protocol.Report{Outcome: "pr_ready", Summary: "s"}, 200, nil)
	if err := markMerged(store, second.ID, 1, merged.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	scheduled, _, err := store.CreateTicket(ctx, NewTicket{Source: "schedule:nightly", Repo: "owner/repo", Title: "T", Body: "B", Agent: "default", CreatedBy: "schedule", SourceKey: "schedule:nightly:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := markMerged(store, scheduled.ID, 9, merged); err != nil {
		t.Fatal(err)
	}
	createTicket(t, store, "api:unmerged")

	ledger, err := store.Ledger(ctx, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if ledger.MergedPRs != 2 || ledger.FeedbackRounds != 0.5 || ledger.HumanCommits != 2 || ledger.Tokens != 300 || ledger.Cost == nil || *ledger.Cost != 2 {
		t.Fatalf("ledger = %+v", ledger)
	}
	if later, _ := store.Ledger(ctx, "owner/repo", merged.Add(time.Minute)); later.MergedPRs != 1 || later.HumanCommits != 1 || later.Cost != nil {
		t.Fatalf("later = %+v", later)
	}
	if other, _ := store.Ledger(ctx, "other/repo", time.Time{}); other.MergedPRs != 0 {
		t.Fatalf("other = %+v", other)
	}

	figures, err := store.TicketFigures(ctx, first.ID)
	if err != nil || figures.FeedbackRounds != 1 || figures.Tokens != 400 || *figures.HumanCommits != 3 || !figures.Merged || *figures.Cost != 4 {
		t.Fatalf("figures = %+v, err = %v", figures, err)
	}
	if _, err := store.TicketFigures(ctx, "t_missing"); err != ErrNotFound {
		t.Fatalf("error = %v", err)
	}
}

// markMerged records a merged PR the way the GitHub follow-ups used to.
func markMerged(store *Store, ticketID string, humanCommits int, mergedAt time.Time) error {
	_, err := store.db.Exec(`UPDATE tickets SET state=?,human_commits=?,merged_at=? WHERE id=?`, StateDone, humanCommits, stamp(mergedAt), ticketID)
	return err
}

// The self-test saw 1004s for a job that queued for 508s and ran for 496s.
func TestTicketElapsedCountsOnlyTimeRunsSpentRunning(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	created := time.Date(2026, 11, 22, 11, 17, 6, 0, time.UTC)
	at := func(offset time.Duration) { store.SetClock(func() time.Time { return created.Add(offset) }) }
	at(0)
	ticket := createTicket(t, store, "api:elapsed")
	queue := func() {
		t.Helper()
		if _, err := store.QueueRun(ctx, ticket.ID, "claude"); err != nil {
			t.Fatal(err)
		}
	}
	answer := func() {
		t.Helper()
		if _, _, err := store.AddInput(ctx, ticket.ID, Input{Kind: EntryComment, Author: "alice", Body: "again"}); err != nil {
			t.Fatal(err)
		}
		queue()
	}
	complete := func(claimed, ended time.Duration) {
		t.Helper()
		at(claimed)
		claim, err := store.ClaimRun(ctx, "runner", []string{"claude"}, created.Add(claimed+time.Hour), nil)
		if err != nil || claim == nil {
			t.Fatalf("claim = %v, err = %v", claim, err)
		}
		at(ended)
		if _, _, err := store.CompleteRun(ctx, RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded, Report: &protocol.Report{Outcome: "pr_ready", Summary: "s"}}, func(Ticket, string) Transition {
			return Transition{State: StatePRReady}
		}); err != nil {
			t.Fatal(err)
		}
	}
	queue()
	complete(508*time.Second, 1004*time.Second)
	// The pull request changes hours later, which updates the ticket.
	if _, err := store.db.Exec(`UPDATE tickets SET updated_at=? WHERE id=?`, stamp(created.Add(3*time.Hour)), ticket.ID); err != nil {
		t.Fatal(err)
	}
	if figures, err := store.TicketFigures(ctx, ticket.ID); err != nil || figures.Elapsed != 496*time.Second {
		t.Fatalf("figures = %+v, err = %v", figures, err)
	}

	// A second run adds its own running time, and a run still going counts up to now.
	at(4 * time.Hour)
	answer()
	complete(4*time.Hour+10*time.Second, 4*time.Hour+110*time.Second)
	at(5 * time.Hour)
	answer()
	if claim, err := store.ClaimRun(ctx, "runner", []string{"claude"}, created.Add(6*time.Hour), nil); err != nil || claim == nil {
		t.Fatalf("claim = %v, err = %v", claim, err)
	}
	at(5*time.Hour + 30*time.Second)
	if figures, err := store.TicketFigures(ctx, ticket.ID); err != nil || figures.Elapsed != 626*time.Second {
		t.Fatalf("figures = %+v, err = %v", figures, err)
	}
}
