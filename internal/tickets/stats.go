package tickets

import (
	"context"
	"database/sql"
	"slices"
	"sort"
	"time"
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

// StatsDay is one UTC day: the jobs created and the runs that ended.
type StatsDay struct {
	Day       string `json:"day"`
	Jobs      int    `json:"jobs"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	Cancelled int    `json:"cancelled"`
}

// StatsGroup is the jobs created for one preset or one repository.
type StatsGroup struct {
	Name      string `json:"name"`
	JobCounts `json:"jobs"`
	// RunMillis is the median length of this group's runs that ended.
	RunMillis *int64 `json:"run_millis"`
}

// Stats is what the dashboard shows for the last Days days.
type Stats struct {
	Days int       `json:"days"`
	Jobs JobCounts `json:"jobs"`
	Runs RunCounts `json:"runs"`
	// SuccessRate is succeeded over succeeded plus failed runs, from 0 to 1.
	// Cancelled runs do not count. It is nil when no run finished.
	SuccessRate *float64     `json:"success_rate"`
	RunMillis   Spread       `json:"run_millis"`
	WaitMillis  Spread       `json:"wait_millis"`
	Tokens      int64        `json:"tokens"`
	Cost        float64      `json:"cost"`
	Daily       []StatsDay   `json:"daily"`
	Presets     []StatsGroup `json:"presets"`
	Repos       []StatsGroup `json:"repos"`
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

// Stats summarises the jobs created and runs ended in the last days days,
// today included.
func (s *Store) Stats(ctx context.Context, days int) (Stats, error) {
	today := s.now().UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, 1-days)
	stats := Stats{Days: days, Daily: make([]StatsDay, 0, days)}
	index := map[string]int{}
	for day := first; !day.After(today); day = day.AddDate(0, 0, 1) {
		index[day.Format(time.DateOnly)] = len(stats.Daily)
		stats.Daily = append(stats.Daily, StatsDay{Day: day.Format(time.DateOnly)})
	}
	presets, repos := map[string]*StatsGroup{}, map[string]*StatsGroup{}
	group := func(groups map[string]*StatsGroup, name string) *StatsGroup {
		if groups[name] == nil {
			groups[name] = &StatsGroup{Name: name}
		}
		return groups[name]
	}

	jobs, err := s.db.QueryContext(ctx, `SELECT substr(created_at,1,10),state,agent,repo FROM tickets WHERE created_at>=?`, stamp(first))
	if err != nil {
		return Stats{}, err
	}
	defer jobs.Close()
	for jobs.Next() {
		var day, state, preset, repo string
		if err := jobs.Scan(&day, &state, &preset, &repo); err != nil {
			return Stats{}, err
		}
		countJob(&stats.Jobs, state)
		countJob(&group(presets, preset).JobCounts, state)
		countJob(&group(repos, repo).JobCounts, state)
		if i, ok := index[day]; ok {
			stats.Daily[i].Jobs++
		}
	}
	if err := jobs.Err(); err != nil {
		return Stats{}, err
	}

	runs, err := s.db.QueryContext(ctx, `SELECT r.status,r.queued_at,r.started_at,r.ended_at,COALESCE(r.tokens,0),COALESCE(r.cost,0),t.agent,t.repo
  FROM runs r JOIN tickets t ON t.id=r.ticket_id WHERE r.ended_at>=?`, stamp(first))
	if err != nil {
		return Stats{}, err
	}
	defer runs.Close()
	var lengths, waits []int64
	presetLengths, repoLengths := map[string][]int64{}, map[string][]int64{}
	for runs.Next() {
		var status, preset, repo string
		var queued, started, ended sql.NullString
		var tokens int64
		var cost float64
		if err := runs.Scan(&status, &queued, &started, &ended, &tokens, &cost, &preset, &repo); err != nil {
			return Stats{}, err
		}
		stats.Tokens += tokens
		stats.Cost += cost
		stats.Runs.Total++
		endedAt := parseTime(ended)
		day := -1
		if endedAt != nil {
			if i, ok := index[endedAt.Format(time.DateOnly)]; ok {
				day = i
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
		repoLengths[repo] = append(repoLengths[repo], length)
		if queuedAt := parseTime(queued); queuedAt != nil {
			waits = append(waits, startedAt.Sub(*queuedAt).Milliseconds())
		}
		group(presets, preset)
		group(repos, repo)
	}
	if err := runs.Err(); err != nil {
		return Stats{}, err
	}
	if finished := stats.Runs.Succeeded + stats.Runs.Failed; finished > 0 {
		rate := float64(stats.Runs.Succeeded) / float64(finished)
		stats.SuccessRate = &rate
	}
	stats.RunMillis, stats.WaitMillis = spread(lengths), spread(waits)
	stats.Presets = groups(presets, presetLengths)
	stats.Repos = groups(repos, repoLengths)
	return stats, nil
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
