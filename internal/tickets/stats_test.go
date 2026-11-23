package tickets

import (
	"slices"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

// timedRun queues a run at start, starts it waitMinutes later and ends it
// runMinutes after that, moving the ticket to state.
func timedRun(t *testing.T, store *Store, at *time.Time, ticket Ticket, status, state string, waitMinutes, runMinutes int) {
	t.Helper()
	if _, err := store.QueueRun(t.Context(), ticket.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	*at = at.Add(time.Duration(waitMinutes) * time.Minute)
	claim, err := store.ClaimRun(t.Context(), "runner", []string{"claude"}, at.Add(time.Hour), nil)
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
	coding.Source, coding.Origin = "linear", "linear"
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
	if len(stats.Sources) != 2 || stats.Sources[0].Name != OriginAPI || stats.Sources[0].Total != 2 || stats.Sources[1].Name != "linear" || stats.Sources[1].Done != 1 || *stats.Sources[1].RunMillis != 10*60_000 {
		t.Fatalf("sources = %+v", stats.Sources)
	}
}

func TestStatsWithNothingHasNoRates(t *testing.T) {
	stats, err := openStore(t).Stats(t.Context(), 14)
	if err != nil || stats.SuccessRate != nil || stats.RunMillis.Median != nil || len(stats.Daily) != 14 || stats.Presets == nil || stats.Sources == nil {
		t.Fatalf("stats = %+v, err = %v", stats, err)
	}
}

// runTo claims the ticket's next run and ends it with transition and stages.
func runTo(t *testing.T, store *Store, ticket Ticket, transition Transition, stages ...protocol.StageResult) {
	t.Helper()
	if _, err := store.QueueRun(t.Context(), ticket.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(t.Context(), "runner", []string{"claude"}, time.Now().Add(time.Hour), nil)
	if err != nil || claim == nil {
		t.Fatalf("claim = %v, err = %v", claim, err)
	}
	if err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: RunSucceeded, Stages: stages}, func(Ticket, string) Transition {
		return transition
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStatsMeasureTheWayToMergeAndThePeopleItNeeded(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	at := time.Date(2026, 5, 9, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return at })
	clock := func(hour, minute int) time.Time { return time.Date(2026, 5, 9, hour, minute, 0, 0, time.UTC) }

	// The merged job: started at 9:00, its pull request opened at 9:30, a
	// person asked for a change, someone reviewed it at 11:00 and it merged
	// at 12:00.
	// Steps that ran an agent record it; shell steps such as checkout and
	// merge, and a revise with nothing to revise, record none.
	agent := []protocol.Agent{{Name: "coder", Harness: "claude"}}
	merged := createTicket(t, store, "api:merged")
	opened := clock(9, 30)
	at = clock(9, 40)
	runTo(t, store, merged, Transition{State: StatePRReady, PRNumber: 7, PROpenedAt: &opened},
		protocol.StageResult{Name: "checkout", State: RunSucceeded},
		protocol.StageResult{Name: "implement", State: RunSucceeded, Agents: agent},
		protocol.StageResult{Name: "review", State: RunSucceeded, Agents: agent},
		protocol.StageResult{Name: "revise", State: RunSucceeded},
		protocol.StageResult{Name: "publish", State: protocol.StageSkipped, Agents: agent})
	at = clock(10, 0)
	if _, err := store.Reply(ctx, merged.ID, "linear", []Comment{{Body: "Use the other name."}}); err != nil {
		t.Fatal(err)
	}
	at = clock(10, 20)
	runTo(t, store, merged, Transition{State: StatePRReady, PRNumber: 7}, protocol.StageResult{Name: "revise", State: RunSucceeded, Agents: agent})
	listed, err := store.TicketsWithOpenPRs(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}
	reviewed := clock(11, 0)
	at = clock(12, 1)
	if err := store.SetPRState(ctx, listed[0], PRUpdate{Number: 7, State: PRMerged, At: clock(12, 0), OpenedAt: opened, Reviews: &Reviews{Count: 2, First: &reviewed}}); err != nil {
		t.Fatal(err)
	}

	// A job whose pull request is still open, after one rerun, and a job that
	// never opened one.
	at = clock(13, 0)
	open := createTicket(t, store, "api:open")
	runTo(t, store, open, Transition{State: StateFailed})
	if _, err := store.Rerun(ctx, open.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	at = clock(14, 0)
	runTo(t, store, open, Transition{State: StatePRReady, PRNumber: 8},
		protocol.StageResult{Name: "implement", State: RunSucceeded, Agents: agent},
		protocol.StageResult{Name: "merge", State: RunSucceeded})
	createTicket(t, store, "api:no-pr")

	stats, err := store.Stats(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	cycle := stats.Cycle
	if cycle.Merged != 1 || cycle.Reviewed != 1 || *cycle.ToMerge.Median != 3*hour || *cycle.ToPR.Median != hour/2 || *cycle.ToReview.Median != 3*hour/2 || *cycle.ReviewToMerge.Median != hour {
		t.Fatalf("cycle = %+v", cycle)
	}
	if stats.Touches.Replies != 1 || stats.Touches.Reruns != 0 || stats.Touches.Reviews != 2 || *stats.Touches.PerPR != 3 {
		t.Fatalf("touches = %+v, the open job's rerun does not count until it merges", stats.Touches)
	}
	if stats.PRs.Opened != 2 || stats.PRs.Merged != 1 || *stats.PRs.MergeRate != 0.5 {
		t.Fatalf("prs = %+v", stats.PRs)
	}
	day := stats.Daily[6]
	if day.Day != "2026-05-09" || day.Opened != 2 || day.Merged != 1 {
		t.Fatalf("day = %+v", day)
	}
	if day.Steps["implement"] != 2 || day.Steps["review"] != 1 || day.Steps["revise"] != 1 || day.Steps["publish"] != 0 ||
		day.Steps["checkout"] != 0 || day.Steps["merge"] != 0 {
		t.Fatalf("steps = %+v, only agent steps that ran count", day.Steps)
	}
	if want := []string{"implement", "review", "revise"}; !slices.Equal(stats.Steps, want) {
		t.Fatalf("steps = %v, want %v", stats.Steps, want)
	}

	// Running the merged job again starts a new pull request, so the old one's
	// times no longer count.
	at = clock(15, 0)
	if _, err := store.Rerun(ctx, merged.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if stats, err = store.Stats(ctx, 7); err != nil || stats.PRs.Opened != 1 || stats.Cycle.Merged != 0 || stats.Touches.PerPR != nil || stats.Cycle.ToMerge.Median != nil {
		t.Fatalf("stats = %+v, err = %v", stats, err)
	}
}

const hour = int64(time.Hour / time.Millisecond)
