package tickets

import (
	"context"
	"time"
)

// UsageDay is the runs that ended on one UTC day and what they used.
type UsageDay struct {
	Day       string  `json:"day"`
	Succeeded int     `json:"succeeded"`
	Failed    int     `json:"failed"`
	Cancelled int     `json:"cancelled"`
	Tokens    int64   `json:"tokens"`
	Cost      float64 `json:"cost"`
}

// Usage is one entry per day, oldest first, ending today.
type Usage struct {
	Days []UsageDay `json:"days"`
}

// Usage counts the runs that ended on each of the last days days. A day with no runs is
// still listed. A run that timed out or lost its runner counts as failed.
func (s *Store) Usage(ctx context.Context, days int) (Usage, error) {
	today := s.now().UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, 1-days)
	rows, err := s.db.QueryContext(ctx, `SELECT substr(ended_at,1,10),
  SUM(status='succeeded'),
  SUM(status IN ('failed','timed_out','interrupted')),
  SUM(status='cancelled'),
  COALESCE(SUM(tokens),0),
  COALESCE(SUM(cost),0)
  FROM runs WHERE ended_at>=? GROUP BY 1`, stamp(first))
	if err != nil {
		return Usage{}, err
	}
	defer rows.Close()
	found := map[string]UsageDay{}
	for rows.Next() {
		var day UsageDay
		if err := rows.Scan(&day.Day, &day.Succeeded, &day.Failed, &day.Cancelled, &day.Tokens, &day.Cost); err != nil {
			return Usage{}, err
		}
		found[day.Day] = day
	}
	usage := Usage{Days: make([]UsageDay, 0, days)}
	for day := first; !day.After(today); day = day.AddDate(0, 0, 1) {
		entry := found[day.Format(time.DateOnly)]
		entry.Day = day.Format(time.DateOnly)
		usage.Days = append(usage.Days, entry)
	}
	return usage, rows.Err()
}
