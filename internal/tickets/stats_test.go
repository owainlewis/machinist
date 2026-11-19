package tickets

import (
	"testing"
	"time"
)

// timedRun queues a run at start, starts it waitMinutes later and ends it
// runMinutes after that, moving the ticket to state.
func timedRun(t *testing.T, store *Store, at *time.Time, ticket Ticket, status, state string, waitMinutes, runMinutes int) {
	t.Helper()
	if _, err := store.QueueRun(t.Context(), ticket.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	*at = at.Add(time.Duration(waitMinutes) * time.Minute)
	claim, err := store.ClaimRun(t.Context(), "runner", []string{"claude"}, at.Add(time.Hour))
	if err != nil || claim == nil {
		t.Fatalf("claim = %v, err = %v", claim, err)
	}
	*at = at.Add(time.Duration(runMinutes) * time.Minute)
	tokens := int64(10)
	if err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: status, Tokens: &tokens}, func(Ticket, string) Transition {
		return Transition{State: state}
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStatsSummarisesJobsAndRuns(t *testing.T) {
	store := openStore(t)
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return at })
	createTicket(t, store, "api:too-old")

	at = time.Date(2026, 5, 9, 9, 0, 0, 0, time.UTC)
	coding := request("api:coding")
	coding.Agent, coding.Repo = "coding", "owner/app"
	first, _, err := store.CreateTicket(t.Context(), coding)
	if err != nil {
		t.Fatal(err)
	}
	timedRun(t, store, &at, first, RunSucceeded, StatePRReady, 1, 10)

	at = time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC)
	second := createTicket(t, store, "api:second")
	timedRun(t, store, &at, second, RunFailed, StateFailed, 3, 2)
	createTicket(t, store, "api:waiting")

	stats, err := store.Stats(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Jobs != (JobCounts{Total: 3, Active: 1, Done: 1, Failed: 1}) {
		t.Fatalf("jobs = %+v", stats.Jobs)
	}
	if stats.Runs != (RunCounts{Total: 2, Succeeded: 1, Failed: 1}) || stats.SuccessRate == nil || *stats.SuccessRate != 0.5 || stats.Tokens != 20 {
		t.Fatalf("runs = %+v, rate = %v, tokens = %d", stats.Runs, stats.SuccessRate, stats.Tokens)
	}
	if *stats.RunMillis.Median != 2*60_000 || *stats.RunMillis.P90 != 10*60_000 || *stats.WaitMillis.Median != 60_000 {
		t.Fatalf("run = %v/%v, wait = %v", *stats.RunMillis.Median, *stats.RunMillis.P90, *stats.WaitMillis.Median)
	}
	if len(stats.Daily) != 7 || stats.Daily[6].Day != "2026-05-10" || stats.Daily[6].Jobs != 2 || stats.Daily[6].Failed != 1 || stats.Daily[5].Succeeded != 1 {
		t.Fatalf("daily = %+v", stats.Daily)
	}
	if len(stats.Presets) != 2 || stats.Presets[0].Name != "default" || stats.Presets[0].Total != 2 || stats.Presets[1].Name != "coding" || *stats.Presets[1].RunMillis != 10*60_000 {
		t.Fatalf("presets = %+v", stats.Presets)
	}
	if len(stats.Repos) != 2 || stats.Repos[0].Name != "owner/repo" {
		t.Fatalf("repos = %+v", stats.Repos)
	}
}

func TestStatsWithNothingHasNoRates(t *testing.T) {
	stats, err := openStore(t).Stats(t.Context(), 14)
	if err != nil || stats.SuccessRate != nil || stats.RunMillis.Median != nil || len(stats.Daily) != 14 || stats.Presets == nil {
		t.Fatalf("stats = %+v, err = %v", stats, err)
	}
}
