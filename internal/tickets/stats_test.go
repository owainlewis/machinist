package tickets

import (
	"math"
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

	stats, err := store.Stats(t.Context(), 7, "")
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
	stats, err := openStore(t).Stats(t.Context(), 14, "")
	if err != nil || stats.SuccessRate != nil || stats.RunMillis.Median != nil || len(stats.Daily) != 14 || stats.Presets == nil || stats.Sources == nil || stats.Models == nil || stats.ModelSteps == nil || stats.Repositories == nil || stats.CostPerMergedPR != nil || stats.MonthSpend.CostPerMergedPR != nil {
		t.Fatalf("stats = %+v, err = %v", stats, err)
	}
}

// runTo claims the ticket's next run and ends it with transition and stages.
func runTo(t *testing.T, store *Store, ticket Ticket, transition Transition, stages ...protocol.StageResult) {
	t.Helper()
	runWithStatus(t, store, ticket, RunSucceeded, transition, stages...)
}

func runWithStatus(t *testing.T, store *Store, ticket Ticket, status string, transition Transition, stages ...protocol.StageResult) {
	t.Helper()
	if _, err := store.QueueRun(t.Context(), ticket.ID, "claude"); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(t.Context(), "runner", []string{"claude"}, time.Now().Add(time.Hour), nil)
	if err != nil || claim == nil {
		t.Fatalf("claim = %v, err = %v", claim, err)
	}
	tokens, cost := protocol.Totals(stages)
	if err := store.CompleteRun(t.Context(), RunCompletion{RunID: claim.Run.ID, Status: status, Tokens: tokens, Cost: cost, Stages: stages}, func(Ticket, string) Transition {
		return transition
	}); err != nil {
		t.Fatal(err)
	}
}

func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

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
	if _, _, err := store.SetPRState(ctx, listed[0], PRUpdate{Number: 7, State: PRMerged, At: clock(12, 0), OpenedAt: opened, Reviews: &Reviews{Count: 2, First: &reviewed}}); err != nil {
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

	stats, err := store.Stats(ctx, 7, "")
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
	if day.Day != "2026-05-09" || day.Opened != 2 || day.Merged != 1 || day.Touches != 3 {
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
	// times and touches no longer count.
	at = clock(15, 0)
	if _, err := store.Rerun(ctx, merged.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if stats, err = store.Stats(ctx, 7, ""); err != nil || stats.PRs.Opened != 1 || stats.Cycle.Merged != 0 || stats.Touches.PerPR != nil || stats.Cycle.ToMerge.Median != nil {
		t.Fatalf("stats = %+v, err = %v", stats, err)
	}
	// When the rerun's new pull request merges, only the new generation's
	// touches count — the previous PR's reply is not inherited.
	opened2 := clock(15, 10)
	at = clock(15, 15)
	runTo(t, store, merged, Transition{State: StatePRReady, PRNumber: 9, PROpenedAt: &opened2})
	listed, err = store.TicketsWithOpenPRs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rerunTicket Ticket
	for _, item := range listed {
		if item.ID == merged.ID {
			rerunTicket = item
		}
	}
	at = clock(16, 0)
	if _, _, err := store.SetPRState(ctx, rerunTicket, PRUpdate{Number: 9, State: PRMerged, At: clock(16, 0), OpenedAt: opened2, Reviews: &Reviews{}}); err != nil {
		t.Fatal(err)
	}
	if stats, err = store.Stats(ctx, 7, ""); err != nil || stats.Touches.Replies != 0 || stats.Touches.Reruns != 1 || stats.Touches.Reviews != 0 || stats.Touches.PerPR == nil || *stats.Touches.PerPR != 1 {
		t.Fatalf("touches after rerun merge = %+v, err = %v", stats.Touches, err)
	}
}

func TestStatsMonthToDateSpendCostPerMergedPRAndCostUnknown(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	// Previous month run: inside 30-day rolling window, outside May MTD.
	at := time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return at })
	april := createTicket(t, store, "api:april")
	runTo(t, store, april, Transition{State: StateDone},
		protocol.StageResult{Name: "implement", State: RunSucceeded, Tokens: int64Ptr(5000), Cost: float64Ptr(1.00), Agents: []protocol.Agent{{Name: "coder", Harness: "claude", Model: "claude-opus-5-5"}}})

	// Early May run: inside May MTD, outside a 7-day rolling window on May 20.
	at = time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	earlyMay := createTicket(t, store, "api:early-may")
	earlyMerged := at.Add(30 * time.Minute)
	runTo(t, store, earlyMay, Transition{State: StateDone, PRNumber: 1, PROpenedAt: &at, MergedAt: &earlyMerged},
		protocol.StageResult{Name: "implement", State: RunSucceeded, Tokens: int64Ptr(10_000), Cost: float64Ptr(2.50), Agents: []protocol.Agent{{Name: "coder", Harness: "claude", Model: "claude-opus-5-5"}}})

	// Recent May run: metered Claude + unmetered Antigravity in the same run.
	at = time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	recent := createTicket(t, store, "api:recent")
	recentMerged := at.Add(time.Hour)
	runTo(t, store, recent, Transition{State: StateDone, PRNumber: 2, PROpenedAt: &at, MergedAt: &recentMerged},
		protocol.StageResult{Name: "implement", State: RunSucceeded, Tokens: int64Ptr(20_000), Cost: float64Ptr(1.50), Agents: []protocol.Agent{{Name: "coder", Harness: "claude", Model: "claude-opus-5-5"}}},
		protocol.StageResult{Name: "review", State: RunSucceeded, Tokens: int64Ptr(30_000), CostUnknown: true, Agents: []protocol.Agent{{Name: "review", Harness: "agy", Model: "gemini-4-neon-high"}}})

	// Purely unmetered run on May 20.
	at = time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	agyOnly := request("api:agy-only")
	agyOnly.Repo = "owner/unmetered"
	unmeteredTicket, _, err := store.CreateTicket(ctx, agyOnly)
	if err != nil {
		t.Fatal(err)
	}
	unmeteredMerged := at.Add(20 * time.Minute)
	runTo(t, store, unmeteredTicket, Transition{State: StateDone, PRNumber: 3, PROpenedAt: &at, MergedAt: &unmeteredMerged},
		protocol.StageResult{Name: "implement", State: RunSucceeded, Tokens: int64Ptr(40_000), CostUnknown: true, Agents: []protocol.Agent{{Name: "coder", Harness: "agy", Model: "gemini-4-neon-high"}}})

	// 7-day stats on May 20: rolling window is May 14..20, MTD is May 1..20.
	stats, err := store.Stats(ctx, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	// Rolling 7d: recent ($1.50, 50k tokens, unknown) + agyOnly ($0/unreported, 40k tokens, unknown).
	// Merged PRs in 7d: 2 (recent + agyOnly). Reported cost per merged PR: 1.50 / 2 = 0.75.
	if !stats.HasCost || !approx(stats.Cost, 1.50) || stats.Tokens != 90_000 || stats.CostUnknown != 2 {
		t.Fatalf("7d spend = cost:%v hasCost:%v tokens:%d unknown:%d", stats.Cost, stats.HasCost, stats.Tokens, stats.CostUnknown)
	}
	if stats.CostPerMergedPR == nil || !approx(*stats.CostPerMergedPR, 0.75) {
		t.Fatalf("7d cost_per_merged_pr = %v, want 0.75", stats.CostPerMergedPR)
	}
	// MTD (2026-05): earlyMay ($2.50, 10k) + recent ($1.50, 50k, unknown) + agyOnly (40k, unknown); excludes April!
	mtd := stats.MonthSpend
	if mtd.Month != "2026-05" || !mtd.HasCost || !approx(mtd.Cost, 4.00) || mtd.Tokens != 100_000 || mtd.Runs != 3 || mtd.CostUnknown != 2 || mtd.MergedPRs != 3 {
		t.Fatalf("month_spend = %+v", mtd)
	}
	if mtd.CostPerMergedPR == nil || !approx(*mtd.CostPerMergedPR, 4.00/3.0) {
		t.Fatalf("mtd cost_per_merged_pr = %v", mtd.CostPerMergedPR)
	}
	// Daily spend on May 19 (index 5) and May 20 (index 6).
	if !stats.Daily[5].HasCost || !approx(stats.Daily[5].Cost, 1.50) || stats.Daily[5].CostUnknown != 1 || stats.Daily[5].Tokens != 50_000 {
		t.Fatalf("May 19 daily = %+v", stats.Daily[5])
	}
	if stats.Daily[6].HasCost || stats.Daily[6].Cost != 0 || stats.Daily[6].CostUnknown != 1 || stats.Daily[6].Tokens != 40_000 {
		t.Fatalf("May 20 daily = %+v", stats.Daily[6])
	}

	// Filtering to the purely unmetered repo must NEVER report $0.00 as CostPerMergedPR.
	unmeteredStats, err := store.Stats(ctx, 7, "owner/unmetered")
	if err != nil {
		t.Fatal(err)
	}
	if unmeteredStats.HasCost || unmeteredStats.Cost != 0 || unmeteredStats.CostUnknown != 1 || unmeteredStats.CostPerMergedPR != nil || unmeteredStats.MonthSpend.HasCost || unmeteredStats.MonthSpend.CostPerMergedPR != nil {
		t.Fatalf("unmetered stats must not treat unknown cost as $0: %+v", unmeteredStats)
	}
	if len(unmeteredStats.Models) != 1 || unmeteredStats.Models[0].HasCost || unmeteredStats.Models[0].MedianCost != nil || unmeteredStats.Models[0].CostUnknown != 1 || unmeteredStats.Models[0].Tokens != 40_000 {
		t.Fatalf("unmetered model stats = %+v", unmeteredStats.Models)
	}
}

func TestStatsComparesModelsAndStepsAndFiltersByRepo(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	at := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return at })

	claude := []protocol.Agent{{Name: "coder", Harness: "claude", Model: "claude-opus-5-5"}}
	gemini := []protocol.Agent{{Name: "coder", Harness: "agy", Model: "gemini-4-neon-high"}}

	// Repo A, Job 1: Claude implement (succeeded, 60s, $0.40) + Gemini review (succeeded, 30s, cost_unknown).
	// Needed 1 reply and 1 human review before merging.
	reqA1 := request("api:a1")
	reqA1.Repo, reqA1.Agent = "owner/app", "coding"
	tA1, _, err := store.CreateTicket(ctx, reqA1)
	if err != nil {
		t.Fatal(err)
	}
	openedA1 := at.Add(10 * time.Minute)
	runTo(t, store, tA1, Transition{State: StatePRReady, PRNumber: 10, PROpenedAt: &openedA1},
		protocol.StageResult{Name: "implement", State: RunSucceeded, DurationMillis: 60_000, Tokens: int64Ptr(15_000), Cost: float64Ptr(0.40), Agents: claude},
		protocol.StageResult{Name: "review", State: RunSucceeded, DurationMillis: 30_000, Tokens: int64Ptr(12_000), CostUnknown: true, Agents: gemini},
		protocol.StageResult{Name: "publish", State: protocol.StageSkipped, Agents: claude})
	at = at.Add(20 * time.Minute)
	if _, err := store.Reply(ctx, tA1.ID, "alice", []Comment{{Body: "Rename helper"}}); err != nil {
		t.Fatal(err)
	}
	at = at.Add(10 * time.Minute)
	runTo(t, store, tA1, Transition{State: StatePRReady, PRNumber: 10},
		protocol.StageResult{Name: "revise", State: RunSucceeded, DurationMillis: 20_000, Tokens: int64Ptr(5_000), Cost: float64Ptr(0.10), Agents: claude})
	listed, err := store.TicketsWithOpenPRs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(30 * time.Minute)
	reviewedAt := at.Add(-10 * time.Minute)
	if _, _, err := store.SetPRState(ctx, listed[0], PRUpdate{Number: 10, State: PRMerged, At: at, OpenedAt: openedA1, Reviews: &Reviews{Count: 1, First: &reviewedAt}}); err != nil {
		t.Fatal(err)
	}

	// Repo A, Job 2: Claude implement failed, then Gemini implement succeeded and opened PR (still open).
	reqA2 := request("api:a2")
	reqA2.Repo, reqA2.Agent = "owner/app", "coding"
	tA2, _, err := store.CreateTicket(ctx, reqA2)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(10 * time.Minute)
	runWithStatus(t, store, tA2, RunFailed, Transition{State: StateFailed},
		protocol.StageResult{Name: "implement", State: RunFailed, DurationMillis: 90_000, Tokens: int64Ptr(10_000), Cost: float64Ptr(0.20), Agents: claude})
	if _, err := store.Rerun(ctx, tA2.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	at = at.Add(10 * time.Minute)
	openedA2 := at
	runTo(t, store, tA2, Transition{State: StatePRReady, PRNumber: 11, PROpenedAt: &openedA2},
		protocol.StageResult{Name: "implement", State: RunSucceeded, DurationMillis: 45_000, Tokens: int64Ptr(25_000), CostUnknown: true, Agents: gemini})

	// Repo B, Job 1: separate repo & preset to verify repo filtering and preset/repo spend.
	reqB1 := request("api:b1")
	reqB1.Repo, reqB1.Agent = "owner/api", "fast"
	tB1, _, err := store.CreateTicket(ctx, reqB1)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(10 * time.Minute)
	mergedB1 := at.Add(5 * time.Minute)
	runTo(t, store, tB1, Transition{State: StateDone, PRNumber: 1, PROpenedAt: &at, MergedAt: &mergedB1},
		protocol.StageResult{Name: "implement", State: RunSucceeded, DurationMillis: 15_000, Tokens: int64Ptr(8_000), Cost: float64Ptr(0.80), Agents: claude})

	// Unfiltered stats: sees both repos.
	all, err := store.Stats(ctx, 7, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(all.Repositories, []string{"owner/api", "owner/app"}) {
		t.Fatalf("repositories = %v", all.Repositories)
	}
	if len(all.Models) != 2 {
		t.Fatalf("models = %+v", all.Models)
	}
	// Claude ran 4 steps (implement x3, revise x1) across 4 runs; Gemini ran 2 steps across 2 runs.
	gotClaude := all.Models[0]
	gotGemini := all.Models[1]
	if gotClaude.Harness != "claude" || gotClaude.Model != "claude-opus-5-5" || gotClaude.Steps != 4 || gotClaude.Runs != 4 || gotClaude.Passed != 3 || gotClaude.Failed != 1 {
		t.Fatalf("claude = %+v", gotClaude)
	}
	if gotClaude.PassRate == nil || !approx(*gotClaude.PassRate, 0.75) {
		t.Fatalf("claude pass_rate = %v", gotClaude.PassRate)
	}
	// Tickets touching Claude: tA1 (opened+merged, 2 touches, reworked), tA2 (opened, not merged, reworked), tB1 (opened+merged, 0 touches, not reworked).
	if gotClaude.OpenedPRs != 3 || gotClaude.MergedPRs != 2 || gotClaude.MergeRate == nil || !approx(*gotClaude.MergeRate, 2.0/3.0) {
		t.Fatalf("claude prs = %+v", gotClaude)
	}
	if gotClaude.TouchesPerPR == nil || !approx(*gotClaude.TouchesPerPR, 1.0) || gotClaude.ReworkRate == nil || !approx(*gotClaude.ReworkRate, 2.0/3.0) {
		t.Fatalf("claude touches/rework = %v / %v", gotClaude.TouchesPerPR, gotClaude.ReworkRate)
	}
	// Claude costs: [0.10, 0.20, 0.40, 0.80] -> total 1.50, nearest-rank median = 0.20.
	if !gotClaude.HasCost || !approx(gotClaude.Cost, 1.50) || gotClaude.MedianCost == nil || !approx(*gotClaude.MedianCost, 0.20) || gotClaude.Tokens != 38_000 || gotClaude.CostUnknown != 0 {
		t.Fatalf("claude spend = %+v", gotClaude)
	}

	if gotGemini.Harness != "agy" || gotGemini.Model != "gemini-4-neon-high" || gotGemini.Steps != 2 || gotGemini.Runs != 2 || gotGemini.Passed != 2 || gotGemini.Failed != 0 {
		t.Fatalf("gemini = %+v", gotGemini)
	}
	if gotGemini.HasCost || gotGemini.Cost != 0 || gotGemini.MedianCost != nil || gotGemini.CostUnknown != 2 || gotGemini.Tokens != 37_000 {
		t.Fatalf("gemini spend = %+v", gotGemini)
	}
	// Gemini durations: [30000, 45000] -> nearest-rank median = 30000.
	if gotGemini.DurationMillis.Median == nil || *gotGemini.DurationMillis.Median != 30_000 {
		t.Fatalf("gemini duration = %+v", gotGemini.DurationMillis)
	}

	// ModelSteps ordered by pipeline order: implement (claude, gemini), review (gemini), revise (claude).
	if len(all.ModelSteps) != 4 ||
		all.ModelSteps[0].Step != "implement" || all.ModelSteps[0].Harness != "claude" || all.ModelSteps[0].Steps != 3 ||
		all.ModelSteps[1].Step != "implement" || all.ModelSteps[1].Harness != "agy" || all.ModelSteps[1].Steps != 1 ||
		all.ModelSteps[2].Step != "review" || all.ModelSteps[2].Harness != "agy" ||
		all.ModelSteps[3].Step != "revise" || all.ModelSteps[3].Harness != "claude" {
		t.Fatalf("model_steps = %+v", all.ModelSteps)
	}

	// Preset & repo spend breakdowns.
	var appRepo, apiRepo StatsGroup
	for _, r := range all.Repos {
		switch r.Name {
		case "owner/app":
			appRepo = r
		case "owner/api":
			apiRepo = r
		}
	}
	if !approx(appRepo.Cost, 0.70) || appRepo.CostUnknown != 2 || appRepo.Tokens != 67_000 || appRepo.MergedPRs != 1 || appRepo.Runs != 4 {
		t.Fatalf("app repo = %+v", appRepo)
	}
	if !approx(apiRepo.Cost, 0.80) || apiRepo.CostUnknown != 0 || apiRepo.Tokens != 8_000 || apiRepo.MergedPRs != 1 || apiRepo.Runs != 1 {
		t.Fatalf("api repo = %+v", apiRepo)
	}

	// Filtered to owner/app: excludes owner/api everywhere, while keeping Repositories unfiltered.
	filtered, err := store.Stats(ctx, 7, "owner/app")
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Repo != "owner/app" || !slices.Equal(filtered.Repositories, []string{"owner/api", "owner/app"}) {
		t.Fatalf("filtered header = repo:%q repos:%v", filtered.Repo, filtered.Repositories)
	}
	if filtered.Jobs.Total != 2 || filtered.Runs.Total != 4 || !approx(filtered.Cost, 0.70) || filtered.CostUnknown != 2 || filtered.PRs.Merged != 1 || filtered.CostPerMergedPR == nil || !approx(*filtered.CostPerMergedPR, 0.70) {
		t.Fatalf("filtered stats = %+v", filtered)
	}
	if len(filtered.Repos) != 1 || filtered.Repos[0].Name != "owner/app" || len(filtered.Presets) != 1 || filtered.Presets[0].Name != "coding" {
		t.Fatalf("filtered groups = repos:%+v presets:%+v", filtered.Repos, filtered.Presets)
	}
	if len(filtered.Models) != 2 || filtered.Models[0].Steps != 3 || !approx(filtered.Models[0].Cost, 0.70) {
		t.Fatalf("filtered models = %+v", filtered.Models)
	}
}

const hour = int64(time.Hour / time.Millisecond)
