package tickets

import (
	"testing"
	"time"
)

func endRun(t *testing.T, store *Store, sourceKey, status string, tokens int64, cost *float64) {
	t.Helper()
	ticket := createTicket(t, store, sourceKey)
	if _, err := store.QueueRun(t.Context(), ticket.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(t.Context(), "runner", []string{"claude"}, time.Now().Add(time.Hour))
	if err != nil || claim == nil {
		t.Fatalf("claim = %v, err = %v", claim, err)
	}
	err = store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: status, Tokens: &tokens, Cost: cost}, 10, func(Ticket, string) Transition {
		return Transition{State: StateFailed}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUsageCountsRunsByTheDayTheyEnded(t *testing.T) {
	store := openStore(t)
	cost := 1.5
	at := time.Date(2026, 5, 8, 23, 30, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return at })
	endRun(t, store, "api:early", RunSucceeded, 100, nil)

	at = time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC)
	endRun(t, store, "api:passed", RunSucceeded, 200, &cost)
	endRun(t, store, "api:failed", RunFailed, 50, &cost)
	endRun(t, store, "api:timed-out", RunTimedOut, 0, nil)
	cancelled := createTicket(t, store, "api:cancelled")
	if _, err := store.QueueRun(t.Context(), cancelled.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(t.Context(), cancelled.ID, "alice"); err != nil {
		t.Fatal(err)
	}

	usage, err := store.Usage(t.Context(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Days) != 14 || usage.Days[0].Day != "2026-04-27" || usage.Days[13].Day != "2026-05-10" {
		t.Fatalf("days = %+v", usage.Days)
	}
	if got, want := usage.Days[13], (UsageDay{Day: "2026-05-10", Succeeded: 1, Failed: 2, Cancelled: 1, Tokens: 250, Cost: 3}); got != want {
		t.Fatalf("today = %+v, want %+v", got, want)
	}
	if got, want := usage.Days[11], (UsageDay{Day: "2026-05-08", Succeeded: 1, Tokens: 100}); got != want {
		t.Fatalf("two days ago = %+v, want %+v", got, want)
	}
	if got, want := usage.Days[12], (UsageDay{Day: "2026-05-09"}); got != want {
		t.Fatalf("yesterday = %+v, want %+v", got, want)
	}

	at = time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	usage, err = store.Usage(t.Context(), 14)
	if err != nil {
		t.Fatal(err)
	}
	if first := usage.Days[0]; first.Day != "2026-05-09" || first.Succeeded != 0 {
		t.Fatalf("first day = %+v", first)
	}
	if last := usage.Days[1]; last.Failed != 2 {
		t.Fatalf("second day = %+v", last)
	}
}
