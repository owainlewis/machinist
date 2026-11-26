package tickets

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
)

// JobCounts counts jobs by where they are now.
type JobCounts struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Done      int `json:"done"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// RunCounts counts the runs that ended. Timed-out and lost runs are failed.
type RunCounts struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// Spread is the median and 90th percentile of a duration, in milliseconds.
// Both are nil when nothing was measured.
type Spread struct {
	Median *int64 `json:"median"`
	P90    *int64 `json:"p90"`
}

// StatsDay is one UTC day: the jobs created, the runs that ended, the pull
// requests opened and merged, spend, human touches, and how many times each
// step ran.
type StatsDay struct {
	Day       string `json:"day"`
	Jobs      int    `json:"jobs"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	Cancelled int    `json:"cancelled"`
	Opened    int    `json:"opened"`
	Merged    int    `json:"merged"`
	// Cost is reported USD spend for runs that ended this day.
	Cost float64 `json:"cost"`
	// HasCost is true when at least one run that ended this day reported a cost.
	HasCost bool `json:"has_cost"`
	// Tokens is the token total for runs that ended this day.
	Tokens int64 `json:"tokens"`
	// CostUnknown is how many runs that ended this day had at least one stage
	// whose harness reports no cost.
	CostUnknown int `json:"cost_unknown"`
	// Touches is the total human touches (replies, reruns and reviews) on pull
	// requests that merged this day.
	Touches int `json:"touches"`
	// Steps counts the steps of the runs that ended this day, by step name.
	// A step that was skipped did not run.
	Steps map[string]int `json:"steps"`
}

// PRCounts counts the pull requests opened and merged in the range.
type PRCounts struct {
	Opened int `json:"opened"`
	Merged int `json:"merged"`
	// MergeRate is the share of the pull requests opened in the range that
	// have merged, from 0 to 1. It is nil when none was opened.
	MergeRate *float64 `json:"merge_rate"`
}

// Cycle is how long the jobs whose pull request merged in the range took,
// from the job starting to its pull request merging, and where the time went.
// A job that merged without a review from a person has no review times.
type Cycle struct {
	Merged   int `json:"merged"`
	Reviewed int `json:"reviewed"`
	// ToMerge is started to merged, ToPR started to pull request opened,
	// ToReview pull request opened to its first review from a person, and
	// ReviewToMerge that review to merged.
	ToMerge       Spread `json:"to_merge"`
	ToPR          Spread `json:"to_pr"`
	ToReview      Spread `json:"to_review"`
	ReviewToMerge Spread `json:"review_to_merge"`
}

// Touches counts the times a person stepped in on the jobs whose pull request
// merged in the range, up to the merge: replies that ran the job again,
// reruns, and reviews from people. Fewer is better.
type Touches struct {
	Replies int `json:"replies"`
	Reruns  int `json:"reruns"`
	Reviews int `json:"reviews"`
	// PerPR is all touches over the merged pull requests. It is nil when none
	// merged.
	PerPR *float64 `json:"per_pr"`
}

// StatsGroup is the jobs and spend for one preset, repository or source.
type StatsGroup struct {
	Name      string `json:"name"`
	JobCounts `json:"jobs"`
	// RunMillis is the median length of this group's runs that ended.
	RunMillis   *int64  `json:"run_millis"`
	Runs        int     `json:"runs"`
	Tokens      int64   `json:"tokens"`
	Cost        float64 `json:"cost"`
	HasCost     bool    `json:"has_cost"`
	CostUnknown int     `json:"cost_unknown"`
	MergedPRs   int     `json:"merged_prs"`
}

// MonthSpend summarises calendar UTC month-to-date spend, tokens and unknown
// cost runs.
type MonthSpend struct {
	// Month is the UTC calendar month, YYYY-MM.
	Month           string   `json:"month"`
	Cost            float64  `json:"cost"`
	HasCost         bool     `json:"has_cost"`
	Tokens          int64    `json:"tokens"`
	Runs            int      `json:"runs"`
	CostUnknown     int      `json:"cost_unknown"`
	MergedPRs       int      `json:"merged_prs"`
	CostPerMergedPR *float64 `json:"cost_per_merged_pr"`
}

// ModelStats summarises one harness and model across the selected range.
// When Step is non-empty, the figures cover that step only.
type ModelStats struct {
	Step           string   `json:"step,omitempty"`
	Harness        string   `json:"harness"`
	Model          string   `json:"model"`
	Runs           int      `json:"runs"`
	Steps          int      `json:"steps"`
	Passed         int      `json:"passed"`
	Failed         int      `json:"failed"`
	Cancelled      int      `json:"cancelled"`
	PassRate       *float64 `json:"pass_rate"`
	OpenedPRs      int      `json:"opened_prs"`
	MergedPRs      int      `json:"merged_prs"`
	MergeRate      *float64 `json:"merge_rate"`
	DurationMillis Spread   `json:"duration_millis"`
	Tokens         int64    `json:"tokens"`
	Cost           float64  `json:"cost"`
	HasCost        bool     `json:"has_cost"`
	MedianCost     *float64 `json:"median_cost"`
	CostUnknown    int      `json:"cost_unknown"`
	TouchesPerPR   *float64 `json:"touches_per_pr"`
	ReworkRate     *float64 `json:"rework_rate"`
}

// Stats is what the dashboard shows for the last Days days, optionally filtered
// to one repository.
type Stats struct {
	Days int `json:"days"`
	// Repo is the repository filter when set.
	Repo string `json:"repo,omitempty"`
	// Repositories lists every repository seen in tickets, unfiltered by Repo,
	// so the dashboard filter can switch between them.
	Repositories []string  `json:"repositories"`
	Jobs         JobCounts `json:"jobs"`
	Runs         RunCounts `json:"runs"`
	// SuccessRate is succeeded over succeeded plus failed runs, from 0 to 1.
	// Cancelled runs do not count. It is nil when no run finished.
	SuccessRate     *float64     `json:"success_rate"`
	RunMillis       Spread       `json:"run_millis"`
	WaitMillis      Spread       `json:"wait_millis"`
	Tokens          int64        `json:"tokens"`
	Cost            float64      `json:"cost"`
	HasCost         bool         `json:"has_cost"`
	CostUnknown     int          `json:"cost_unknown"`
	CostPerMergedPR *float64     `json:"cost_per_merged_pr"`
	MonthSpend      MonthSpend   `json:"month_spend"`
	Daily           []StatsDay   `json:"daily"`
	Presets         []StatsGroup `json:"presets"`
	Repos           []StatsGroup `json:"repos"`
	// Sources groups the jobs by where they came from, such as web or linear.
	Sources    []StatsGroup `json:"sources"`
	Models     []ModelStats `json:"models"`
	ModelSteps []ModelStats `json:"model_steps"`
	PRs        PRCounts     `json:"prs"`
	Cycle      Cycle        `json:"cycle"`
	Touches    Touches      `json:"touches"`
	// Steps names every step that ran in the range, the most run first.
	Steps []string `json:"steps"`
}

func countJob(counts *JobCounts, state string) {
	counts.Total++
	switch state {
	case StateWorking:
		counts.Active++
	case StatePRReady, StateDone:
		counts.Done++
	case StateCancelled:
		counts.Cancelled++
	default:
		counts.Failed++
	}
}

func repoClause(column, repo string, args []any) (string, []any) {
	if repo == "" {
		return "", args
	}
	return " AND " + column + "=?", append(args, repo)
}

// Stats summarises the jobs created and runs ended in the last days days,
// today included. A non-empty repo restricts every figure (except Repositories)
// to that repository.
func (s *Store) Stats(ctx context.Context, days int, repo string) (Stats, error) {
	now := s.now().UTC()
	today := now.Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, 1-days)
	stats := Stats{
		Days:         days,
		Repo:         repo,
		Repositories: []string{},
		Daily:        make([]StatsDay, 0, days),
		Models:       []ModelStats{},
		ModelSteps:   []ModelStats{},
	}
	index := map[string]int{}
	for day := first; !day.After(today); day = day.AddDate(0, 0, 1) {
		index[day.Format(time.DateOnly)] = len(stats.Daily)
		stats.Daily = append(stats.Daily, StatsDay{Day: day.Format(time.DateOnly), Steps: map[string]int{}})
	}

	repoRows, err := s.q().QueryContext(ctx, `SELECT DISTINCT repo FROM tickets WHERE repo<>'' ORDER BY repo`)
	if err != nil {
		return Stats{}, err
	}
	defer repoRows.Close()
	for repoRows.Next() {
		var name string
		if err := repoRows.Scan(&name); err != nil {
			return Stats{}, err
		}
		stats.Repositories = append(stats.Repositories, name)
	}
	if err := repoRows.Err(); err != nil {
		return Stats{}, err
	}

	presets, repos, sources := map[string]*StatsGroup{}, map[string]*StatsGroup{}, map[string]*StatsGroup{}
	group := func(groups map[string]*StatsGroup, name string) *StatsGroup {
		if groups[name] == nil {
			groups[name] = &StatsGroup{Name: name}
		}
		return groups[name]
	}

	jobsWhere, jobsArgs := repoClause("repo", repo, []any{stamp(first)})
	jobs, err := s.q().QueryContext(ctx, `SELECT substr(created_at,1,10),state,agent,repo,origin FROM tickets WHERE created_at>=?`+jobsWhere, jobsArgs...)
	if err != nil {
		return Stats{}, err
	}
	defer jobs.Close()
	for jobs.Next() {
		var day, state, preset, repoName, source string
		if err := jobs.Scan(&day, &state, &preset, &repoName, &source); err != nil {
			return Stats{}, err
		}
		countJob(&stats.Jobs, state)
		countJob(&group(presets, preset).JobCounts, state)
		countJob(&group(repos, repoName).JobCounts, state)
		countJob(&group(sources, source).JobCounts, state)
		if i, ok := index[day]; ok {
			stats.Daily[i].Jobs++
		}
	}
	if err := jobs.Err(); err != nil {
		return Stats{}, err
	}

	runsWhere, runsArgs := repoClause("t.repo", repo, []any{stamp(first)})
	runs, err := s.q().QueryContext(ctx, `SELECT r.status,r.queued_at,r.started_at,r.ended_at,
  COALESCE(r.tokens,(SELECT CAST(SUM(s.tokens) AS BIGINT) FROM run_stages s WHERE s.run=r.id),0),
  COALESCE(r.cost,(SELECT SUM(s.cost) FROM run_stages s WHERE s.run=r.id)),
  EXISTS (SELECT 1 FROM run_stages s WHERE s.run=r.id AND s.cost_unknown<>0),
  t.agent,t.repo,t.origin
  FROM runs r JOIN tickets t ON t.id=r.ticket_id WHERE r.ended_at>=?`+runsWhere, runsArgs...)
	if err != nil {
		return Stats{}, err
	}
	defer runs.Close()
	var lengths, waits []int64
	presetLengths, repoLengths, sourceLengths := map[string][]int64{}, map[string][]int64{}, map[string][]int64{}
	for runs.Next() {
		var status, preset, repoName, source string
		var queued, started, ended sql.NullString
		var tokens int64
		var cost sql.NullFloat64
		var costUnknown bool
		if err := runs.Scan(&status, &queued, &started, &ended, &tokens, &cost, &costUnknown, &preset, &repoName, &source); err != nil {
			return Stats{}, err
		}
		stats.Tokens += tokens
		if cost.Valid {
			stats.Cost += cost.Float64
			stats.HasCost = true
		}
		if costUnknown {
			stats.CostUnknown++
		}
		stats.Runs.Total++

		addGroupRun := func(g *StatsGroup) {
			g.Runs++
			g.Tokens += tokens
			if cost.Valid {
				g.Cost += cost.Float64
				g.HasCost = true
			}
			if costUnknown {
				g.CostUnknown++
			}
		}
		addGroupRun(group(presets, preset))
		addGroupRun(group(repos, repoName))
		addGroupRun(group(sources, source))

		endedAt := parseTime(ended)
		day := -1
		if endedAt != nil {
			if i, ok := index[endedAt.Format(time.DateOnly)]; ok {
				day = i
				stats.Daily[day].Tokens += tokens
				if cost.Valid {
					stats.Daily[day].Cost += cost.Float64
					stats.Daily[day].HasCost = true
				}
				if costUnknown {
					stats.Daily[day].CostUnknown++
				}
			}
		}
		switch status {
		case RunSucceeded:
			stats.Runs.Succeeded++
			if day >= 0 {
				stats.Daily[day].Succeeded++
			}
		case RunCancelled:
			stats.Runs.Cancelled++
			if day >= 0 {
				stats.Daily[day].Cancelled++
			}
		default:
			stats.Runs.Failed++
			if day >= 0 {
				stats.Daily[day].Failed++
			}
		}
		startedAt := parseTime(started)
		if startedAt == nil || endedAt == nil {
			continue
		}
		length := endedAt.Sub(*startedAt).Milliseconds()
		lengths = append(lengths, length)
		presetLengths[preset] = append(presetLengths[preset], length)
		repoLengths[repoName] = append(repoLengths[repoName], length)
		sourceLengths[source] = append(sourceLengths[source], length)
		if queuedAt := parseTime(queued); queuedAt != nil {
			waits = append(waits, startedAt.Sub(*queuedAt).Milliseconds())
		}
	}
	if err := runs.Err(); err != nil {
		return Stats{}, err
	}
	if finished := stats.Runs.Succeeded + stats.Runs.Failed; finished > 0 {
		rate := float64(stats.Runs.Succeeded) / float64(finished)
		stats.SuccessRate = &rate
	}
	stats.RunMillis, stats.WaitMillis = spread(lengths), spread(waits)
	if err := s.pullRequestStats(ctx, &stats, first, repo, index, presets, repos, sources); err != nil {
		return Stats{}, err
	}
	if stats.PRs.Merged > 0 && stats.HasCost {
		perPR := stats.Cost / float64(stats.PRs.Merged)
		stats.CostPerMergedPR = &perPR
	}
	stats.Presets = groups(presets, presetLengths)
	stats.Repos = groups(repos, repoLengths)
	stats.Sources = groups(sources, sourceLengths)
	if err := s.monthSpendStats(ctx, &stats, now, repo); err != nil {
		return Stats{}, err
	}
	if err := s.stepStats(ctx, &stats, first, repo, index); err != nil {
		return Stats{}, err
	}
	if err := s.modelStats(ctx, &stats, first, repo); err != nil {
		return Stats{}, err
	}
	return stats, nil
}

// monthSpendStats computes calendar UTC month-to-date spend, tokens,
// unknown-cost runs, and merged PRs.
func (s *Store) monthSpendStats(ctx context.Context, stats *Stats, now time.Time, repo string) error {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	mtd := MonthSpend{Month: monthStart.Format("2006-01")}
	runsWhere, runsArgs := repoClause("t.repo", repo, []any{stamp(monthStart)})
	rows, err := s.q().QueryContext(ctx, `SELECT
  COALESCE(r.tokens,(SELECT CAST(SUM(s.tokens) AS BIGINT) FROM run_stages s WHERE s.run=r.id),0),
  COALESCE(r.cost,(SELECT SUM(s.cost) FROM run_stages s WHERE s.run=r.id)),
  EXISTS (SELECT 1 FROM run_stages s WHERE s.run=r.id AND s.cost_unknown<>0)
  FROM runs r JOIN tickets t ON t.id=r.ticket_id WHERE r.ended_at>=?`+runsWhere, runsArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var tokens int64
		var cost sql.NullFloat64
		var costUnknown bool
		if err := rows.Scan(&tokens, &cost, &costUnknown); err != nil {
			return err
		}
		mtd.Runs++
		mtd.Tokens += tokens
		if cost.Valid {
			mtd.Cost += cost.Float64
			mtd.HasCost = true
		}
		if costUnknown {
			mtd.CostUnknown++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	mergedWhere, mergedArgs := repoClause("repo", repo, []any{stamp(monthStart)})
	if err := s.q().QueryRowContext(ctx, `SELECT COUNT(*) FROM tickets WHERE merged_at>=?`+mergedWhere, mergedArgs...).Scan(&mtd.MergedPRs); err != nil {
		return err
	}
	if mtd.MergedPRs > 0 && mtd.HasCost {
		perPR := mtd.Cost / float64(mtd.MergedPRs)
		mtd.CostPerMergedPR = &perPR
	}
	stats.MonthSpend = mtd
	return nil
}

// pullRequestStats fills in the pull requests, cycle times and human touches.
// Every number comes from times Machinist stored: when the job was created,
// when GitHub says its pull request opened and merged, when the first person
// reviewed it, and when someone replied or ran it again.
func (s *Store) pullRequestStats(ctx context.Context, stats *Stats, first time.Time, repo string, index map[string]int, presets, repos, sources map[string]*StatsGroup) error {
	since := stamp(first)
	openedWhere, openedArgs := repoClause("repo", repo, []any{since})
	opened, err := s.q().QueryContext(ctx, `SELECT substr(pr_opened_at,1,10),merged_at IS NOT NULL FROM tickets WHERE pr_opened_at>=?`+openedWhere, openedArgs...)
	if err != nil {
		return err
	}
	defer opened.Close()
	openedMerged := 0
	for opened.Next() {
		var day string
		var merged bool
		if err := opened.Scan(&day, &merged); err != nil {
			return err
		}
		stats.PRs.Opened++
		if merged {
			openedMerged++
		}
		if i, ok := index[day]; ok {
			stats.Daily[i].Opened++
		}
	}
	if err := opened.Err(); err != nil {
		return err
	}
	if stats.PRs.Opened > 0 {
		rate := float64(openedMerged) / float64(stats.PRs.Opened)
		stats.PRs.MergeRate = &rate
	}

	group := func(groups map[string]*StatsGroup, name string) *StatsGroup {
		if groups[name] == nil {
			groups[name] = &StatsGroup{Name: name}
		}
		return groups[name]
	}

	mergedWhere, mergedArgs := repoClause("repo", repo, []any{since})
	merged, err := s.q().QueryContext(ctx, `SELECT created_at,pr_opened_at,first_review_at,merged_at,COALESCE(human_reviews,0),agent,repo,origin FROM tickets WHERE merged_at>=?`+mergedWhere, mergedArgs...)
	if err != nil {
		return err
	}
	defer merged.Close()
	var toMerge, toPR, toReview, reviewToMerge []int64
	between := func(from, to *time.Time) int64 { return max(to.Sub(*from).Milliseconds(), 0) }
	for merged.Next() {
		var created, prOpened, reviewed, mergedAt sql.NullString
		var reviews int
		var preset, repoName, source string
		if err := merged.Scan(&created, &prOpened, &reviewed, &mergedAt, &reviews, &preset, &repoName, &source); err != nil {
			return err
		}
		startedAt, openedAt, reviewedAt, mergedTime := parseTime(created), parseTime(prOpened), parseTime(reviewed), parseTime(mergedAt)
		stats.PRs.Merged++
		stats.Cycle.Merged++
		stats.Touches.Reviews += reviews
		group(presets, preset).MergedPRs++
		group(repos, repoName).MergedPRs++
		group(sources, source).MergedPRs++
		if i, ok := index[mergedTime.Format(time.DateOnly)]; ok {
			stats.Daily[i].Merged++
			stats.Daily[i].Touches += reviews
		}
		toMerge = append(toMerge, between(startedAt, mergedTime))
		if openedAt != nil {
			toPR = append(toPR, between(startedAt, openedAt))
		}
		if reviewedAt != nil {
			stats.Cycle.Reviewed++
			reviewToMerge = append(reviewToMerge, between(reviewedAt, mergedTime))
			if openedAt != nil {
				toReview = append(toReview, between(openedAt, reviewedAt))
			}
		}
	}
	if err := merged.Err(); err != nil {
		return err
	}
	stats.Cycle.ToMerge, stats.Cycle.ToPR = spread(toMerge), spread(toPR)
	stats.Cycle.ToReview, stats.Cycle.ReviewToMerge = spread(toReview), spread(reviewToMerge)

	touchesWhere, touchesArgs := repoClause("t.repo", repo, []any{since})
	touches, err := s.q().QueryContext(ctx, `SELECT substr(t.merged_at,1,10),h.kind,COUNT(*) FROM human_touches h JOIN tickets t ON t.id=h.ticket_id
  WHERE t.merged_at>=? AND h.at<=t.merged_at`+touchesWhere+` GROUP BY 1,2`, touchesArgs...)
	if err != nil {
		return err
	}
	defer touches.Close()
	for touches.Next() {
		var day, kind string
		var count int
		if err := touches.Scan(&day, &kind, &count); err != nil {
			return err
		}
		switch kind {
		case TouchReply:
			stats.Touches.Replies += count
			if i, ok := index[day]; ok {
				stats.Daily[i].Touches += count
			}
		case TouchRerun:
			stats.Touches.Reruns += count
			if i, ok := index[day]; ok {
				stats.Daily[i].Touches += count
			}
		}
	}
	if err := touches.Err(); err != nil {
		return err
	}
	if stats.Cycle.Merged > 0 {
		perPR := float64(stats.Touches.Replies+stats.Touches.Reruns+stats.Touches.Reviews) / float64(stats.Cycle.Merged)
		stats.Touches.PerPR = &perPR
	}
	return nil
}

// stepStats counts the agent steps that ran, by the day their run ended. A
// step that ran no agent, such as a shell checkout or a revise that had
// nothing to do, records no agents and is left out.
func (s *Store) stepStats(ctx context.Context, stats *Stats, first time.Time, repo string, index map[string]int) error {
	where, args := repoClause("t.repo", repo, []any{stamp(first), protocol.StageSkipped})
	rows, err := s.q().QueryContext(ctx, `SELECT substr(r.ended_at,1,10),s.name,COUNT(*) FROM run_stages s
  JOIN runs r ON r.id=s.run JOIN tickets t ON t.id=r.ticket_id
  WHERE r.ended_at>=? AND s.state<>? AND s.agents<>''`+where+` GROUP BY 1,2`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	totals := map[string]int{}
	for rows.Next() {
		var day, name string
		var count int
		if err := rows.Scan(&day, &name, &count); err != nil {
			return err
		}
		totals[name] += count
		if i, ok := index[day]; ok {
			stats.Daily[i].Steps[name] += count
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	stats.Steps = make([]string, 0, len(totals))
	for name := range totals {
		stats.Steps = append(stats.Steps, name)
	}
	sort.Slice(stats.Steps, func(a, b int) bool {
		if totals[stats.Steps[a]] != totals[stats.Steps[b]] {
			return totals[stats.Steps[a]] > totals[stats.Steps[b]]
		}
		return stats.Steps[a] < stats.Steps[b]
	})
	return nil
}

type modelKey struct {
	step    string
	harness string
	model   string
}

type ticketOutcome struct {
	opened   bool
	merged   bool
	touches  int
	reworked bool
}

type modelAccum struct {
	key         modelKey
	runs        map[string]struct{}
	tickets     map[string]struct{}
	steps       int
	passed      int
	failed      int
	cancelled   int
	durations   []int64
	costs       []float64
	tokens      int64
	cost        float64
	hasCost     bool
	costUnknown int
}

// modelStats aggregates agent stage performance by (harness, model) and by
// (step, harness, model). When a single stage ran multiple distinct
// (harness, model) pairs, its tokens and cost are split evenly across them so
// totals across models are never double-counted.
func (s *Store) modelStats(ctx context.Context, stats *Stats, first time.Time, repo string) error {
	where, args := repoClause("t.repo", repo, []any{stamp(first), protocol.StageSkipped})
	rows, err := s.q().QueryContext(ctx, `SELECT r.id,t.id,s.name,s.state,s.tokens,s.cost,s.cost_unknown,s.duration_millis,s.agents,
  t.pr_opened_at IS NOT NULL OR t.merged_at IS NOT NULL,
  t.merged_at IS NOT NULL,
  COALESCE(t.human_reviews,0),
  (SELECT COUNT(*) FROM human_touches h WHERE h.ticket_id=t.id AND h.kind IN (?,?) AND (t.merged_at IS NULL OR h.at<=t.merged_at))
  FROM run_stages s JOIN runs r ON r.id=s.run JOIN tickets t ON t.id=r.ticket_id
  WHERE r.ended_at>=? AND s.state<>? AND s.agents<>''`+where,
		append([]any{TouchReply, TouchRerun}, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	overall := map[modelKey]*modelAccum{}
	byStep := map[modelKey]*modelAccum{}
	outcomes := map[string]ticketOutcome{}

	get := func(m map[modelKey]*modelAccum, key modelKey) *modelAccum {
		acc := m[key]
		if acc == nil {
			acc = &modelAccum{
				key:     key,
				runs:    map[string]struct{}{},
				tickets: map[string]struct{}{},
			}
			m[key] = acc
		}
		return acc
	}

	for rows.Next() {
		var runID, ticketID, stepName, state, agentsRaw string
		var tokens sql.NullInt64
		var cost sql.NullFloat64
		var costUnknownInt int
		var durationMillis int64
		var opened, merged bool
		var humanReviews, replyReruns int
		if err := rows.Scan(&runID, &ticketID, &stepName, &state, &tokens, &cost, &costUnknownInt, &durationMillis, &agentsRaw, &opened, &merged, &humanReviews, &replyReruns); err != nil {
			return err
		}
		costUnknown := costUnknownInt != 0
		var agents []protocol.Agent
		if err := json.Unmarshal([]byte(agentsRaw), &agents); err != nil || len(agents) == 0 {
			continue
		}
		// Deduplicate (harness, model) pairs within one stage.
		seen := map[modelKey]struct{}{}
		unique := make([]modelKey, 0, len(agents))
		for _, a := range agents {
			if a.Harness == "" {
				continue
			}
			k := modelKey{harness: a.Harness, model: a.Model}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			unique = append(unique, k)
		}
		if len(unique) == 0 {
			continue
		}

		outcomes[ticketID] = ticketOutcome{
			opened:   opened,
			merged:   merged,
			touches:  humanReviews + replyReruns,
			reworked: replyReruns > 0,
		}

		share := len(unique)
		var tokenShare int64
		if tokens.Valid {
			tokenShare = tokens.Int64 / int64(share)
		}
		var costShare float64
		if cost.Valid {
			costShare = cost.Float64 / float64(share)
		}

		record := func(acc *modelAccum) {
			acc.runs[runID] = struct{}{}
			acc.tickets[ticketID] = struct{}{}
			acc.steps++
			switch state {
			case RunSucceeded:
				acc.passed++
			case RunCancelled:
				acc.cancelled++
			default:
				acc.failed++
			}
			if durationMillis > 0 {
				acc.durations = append(acc.durations, durationMillis)
			}
			if tokens.Valid {
				acc.tokens += tokenShare
			}
			if cost.Valid {
				acc.cost += costShare
				acc.hasCost = true
				acc.costs = append(acc.costs, costShare)
			}
			if costUnknown {
				acc.costUnknown++
			}
		}

		for _, pair := range unique {
			record(get(overall, pair))
			record(get(byStep, modelKey{step: stepName, harness: pair.harness, model: pair.model}))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	finalize := func(acc *modelAccum) ModelStats {
		ms := ModelStats{
			Step:           acc.key.step,
			Harness:        acc.key.harness,
			Model:          acc.key.model,
			Runs:           len(acc.runs),
			Steps:          acc.steps,
			Passed:         acc.passed,
			Failed:         acc.failed,
			Cancelled:      acc.cancelled,
			DurationMillis: spread(acc.durations),
			Tokens:         acc.tokens,
			Cost:           acc.cost,
			HasCost:        acc.hasCost,
			MedianCost:     medianFloat(acc.costs),
			CostUnknown:    acc.costUnknown,
		}
		if finished := acc.passed + acc.failed; finished > 0 {
			rate := float64(acc.passed) / float64(finished)
			ms.PassRate = &rate
		}
		var opened, merged, totalTouches, reworked int
		for ticketID := range acc.tickets {
			out := outcomes[ticketID]
			if out.opened {
				opened++
			}
			if out.merged {
				merged++
				totalTouches += out.touches
			}
			if out.reworked {
				reworked++
			}
		}
		ms.OpenedPRs = opened
		ms.MergedPRs = merged
		if opened > 0 {
			rate := float64(merged) / float64(opened)
			ms.MergeRate = &rate
		}
		if merged > 0 {
			perPR := float64(totalTouches) / float64(merged)
			ms.TouchesPerPR = &perPR
		}
		if len(acc.tickets) > 0 {
			rate := float64(reworked) / float64(len(acc.tickets))
			ms.ReworkRate = &rate
		}
		return ms
	}

	stats.Models = make([]ModelStats, 0, len(overall))
	for _, acc := range overall {
		stats.Models = append(stats.Models, finalize(acc))
	}
	sort.Slice(stats.Models, func(a, b int) bool {
		if stats.Models[a].Steps != stats.Models[b].Steps {
			return stats.Models[a].Steps > stats.Models[b].Steps
		}
		if stats.Models[a].Runs != stats.Models[b].Runs {
			return stats.Models[a].Runs > stats.Models[b].Runs
		}
		if stats.Models[a].Harness != stats.Models[b].Harness {
			return stats.Models[a].Harness < stats.Models[b].Harness
		}
		return stats.Models[a].Model < stats.Models[b].Model
	})

	stats.ModelSteps = make([]ModelStats, 0, len(byStep))
	for _, acc := range byStep {
		stats.ModelSteps = append(stats.ModelSteps, finalize(acc))
	}
	sort.Slice(stats.ModelSteps, func(a, b int) bool {
		oa, ob := stepOrder(stats.ModelSteps[a].Step), stepOrder(stats.ModelSteps[b].Step)
		if oa != ob {
			return oa < ob
		}
		if stats.ModelSteps[a].Step != stats.ModelSteps[b].Step {
			return stats.ModelSteps[a].Step < stats.ModelSteps[b].Step
		}
		if stats.ModelSteps[a].Steps != stats.ModelSteps[b].Steps {
			return stats.ModelSteps[a].Steps > stats.ModelSteps[b].Steps
		}
		if stats.ModelSteps[a].Harness != stats.ModelSteps[b].Harness {
			return stats.ModelSteps[a].Harness < stats.ModelSteps[b].Harness
		}
		return stats.ModelSteps[a].Model < stats.ModelSteps[b].Model
	})
	return nil
}

func stepOrder(name string) int {
	switch name {
	case "implement":
		return 0
	case "review":
		return 1
	case "revise":
		return 2
	case "publish":
		return 3
	default:
		return 4
	}
}

func spread(values []int64) Spread {
	if len(values) == 0 {
		return Spread{}
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	median, p90 := percentile(sorted, 50), percentile(sorted, 90)
	return Spread{Median: &median, P90: &p90}
}

func medianFloat(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	rank := (50*len(sorted) + 99) / 100
	median := sorted[max(rank-1, 0)]
	return &median
}

// percentile uses the nearest rank, so it is always a value that was seen.
func percentile(sorted []int64, p int) int64 {
	rank := (p*len(sorted) + 99) / 100
	return sorted[max(rank-1, 0)]
}

// groups lists the most used first, then by name.
func groups(found map[string]*StatsGroup, lengths map[string][]int64) []StatsGroup {
	list := make([]StatsGroup, 0, len(found))
	for name, group := range found {
		group.RunMillis = spread(lengths[name]).Median
		list = append(list, *group)
	}
	sort.Slice(list, func(a, b int) bool {
		if list[a].Total != list[b].Total {
			return list[a].Total > list[b].Total
		}
		return list[a].Name < list[b].Name
	})
	return list
}
