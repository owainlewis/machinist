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
	claim, err := store.ClaimRun(t.Context(), "runner", []string{"claude"}, time.Now().Add(time.Hour))
	if err != nil || claim == nil {
		t.Fatalf("claim = %v, err = %v", claim, err)
	}
	err = store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded, Report: report, Tokens: &tokens, Cost: cost}, 10, func(Ticket, string) Transition {
		return Transition{State: StateNeedsYou}
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
	finishRun(t, store, first.ID, &protocol.Report{Outcome: "needs_input", Summary: "s", Question: "q"}, 100, &cost)
	if _, _, err := store.AddInput(ctx, first.ID, Input{Kind: EntryComment, Author: "alice", Body: "answer"}); err != nil {
		t.Fatal(err)
	}
	finishRun(t, store, first.ID, &protocol.Report{Outcome: "pr_ready", Summary: "s"}, 300, &cost)
	if err := store.PRMerged(ctx, first.ID, 3, merged); err != nil {
		t.Fatal(err)
	}

	second := createTicket(t, store, "api:second")
	finishRun(t, store, second.ID, &protocol.Report{Outcome: "pr_ready", Summary: "s"}, 200, nil)
	if err := store.PRMerged(ctx, second.ID, 1, merged.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	scheduled, _, err := store.CreateTicket(ctx, NewTicket{Source: "schedule:nightly", Repo: "owner/repo", Title: "T", Body: "B", Agent: "default", CreatedBy: "schedule", SourceKey: "schedule:nightly:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PRMerged(ctx, scheduled.ID, 9, merged); err != nil {
		t.Fatal(err)
	}
	createTicket(t, store, "api:unmerged")

	ledger, err := store.Ledger(ctx, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if ledger.MergedPRs != 2 || ledger.Questions != 0.5 || ledger.FeedbackRounds != 0.5 || ledger.HumanCommits != 2 || ledger.Tokens != 300 || ledger.Cost == nil || *ledger.Cost != 2 {
		t.Fatalf("ledger = %+v", ledger)
	}
	if later, _ := store.Ledger(ctx, "owner/repo", merged.Add(time.Minute)); later.MergedPRs != 1 || later.HumanCommits != 1 || later.Cost != nil {
		t.Fatalf("later = %+v", later)
	}
	if other, _ := store.Ledger(ctx, "other/repo", time.Time{}); other.MergedPRs != 0 || other.Questions != 0 {
		t.Fatalf("other = %+v", other)
	}

	figures, err := store.TicketFigures(ctx, first.ID)
	if err != nil || figures.Questions != 1 || figures.FeedbackRounds != 1 || figures.Tokens != 400 || *figures.HumanCommits != 3 || !figures.Merged || *figures.Cost != 4 {
		t.Fatalf("figures = %+v, err = %v", figures, err)
	}
	if _, err := store.TicketFigures(ctx, "t_missing"); err != ErrNotFound {
		t.Fatalf("error = %v", err)
	}
}
