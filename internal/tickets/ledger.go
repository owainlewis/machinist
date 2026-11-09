package tickets

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Figures is what one ticket cost and how much help it needed.
type Figures struct {
	TicketID       string        `json:"ticket_id"`
	Questions      int           `json:"questions"`
	FeedbackRounds int           `json:"feedback_rounds"`
	HumanCommits   *int          `json:"human_commits,omitempty"`
	Tokens         int64         `json:"tokens"`
	Cost           *float64      `json:"cost,omitempty"`
	Elapsed        time.Duration `json:"elapsed"`
	Merged         bool          `json:"merged"`
}

// Ledger is the average per merged PR, for tickets not started by a schedule.
type Ledger struct {
	MergedPRs      int      `json:"merged_prs"`
	Questions      float64  `json:"questions"`
	FeedbackRounds float64  `json:"feedback_rounds"`
	HumanCommits   float64  `json:"human_commits"`
	Tokens         float64  `json:"tokens"`
	Cost           *float64 `json:"cost,omitempty"`
}

const figuresQuery = `SELECT t.id,t.human_commits,t.merged_at,t.created_at,t.updated_at,
  (SELECT COUNT(*) FROM runs r WHERE r.ticket_id=t.id AND r.kind='agent' AND json_extract(r.report,'$.outcome')='needs_input'),
  (SELECT COUNT(DISTINCT e.consumed_by_run) FROM ticket_entries e WHERE e.ticket_id=t.id AND e.kind IN ('comment','review') AND e.consumed_by_run IS NOT NULL),
  (SELECT COALESCE(SUM(r.tokens),0) FROM runs r WHERE r.ticket_id=t.id),
  (SELECT SUM(r.cost) FROM runs r WHERE r.ticket_id=t.id)
  FROM tickets t WHERE `

func (s *Store) figures(ctx context.Context, where string, arguments ...any) ([]Figures, error) {
	rows, err := s.db.QueryContext(ctx, figuresQuery+where, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Figures{}
	for rows.Next() {
		var figures Figures
		var commits sql.NullInt64
		var merged sql.NullString
		var created, updated string
		var cost sql.NullFloat64
		if err := rows.Scan(&figures.TicketID, &commits, &merged, &created, &updated, &figures.Questions, &figures.FeedbackRounds, &figures.Tokens, &cost); err != nil {
			return nil, err
		}
		if commits.Valid {
			count := int(commits.Int64)
			figures.HumanCommits = &count
		}
		if cost.Valid {
			figures.Cost = &cost.Float64
		}
		figures.Merged = merged.Valid
		figures.Elapsed = parseTime(sql.NullString{String: updated, Valid: true}).Sub(*parseTime(sql.NullString{String: created, Valid: true}))
		result = append(result, figures)
	}
	return result, rows.Err()
}

// TicketFigures returns the figures for one ticket.
func (s *Store) TicketFigures(ctx context.Context, ticketID string) (Figures, error) {
	figures, err := s.figures(ctx, `t.id=?`, ticketID)
	if err != nil {
		return Figures{}, err
	}
	if len(figures) == 0 {
		return Figures{}, ErrNotFound
	}
	return figures[0], nil
}

// Ledger averages the figures of PRs merged since a time. An empty repo means
// every repository, and a zero time means all history.
func (s *Store) Ledger(ctx context.Context, repo string, since time.Time) (Ledger, error) {
	conditions := []string{`t.merged_at IS NOT NULL`, `t.source NOT LIKE 'schedule:%'`}
	arguments := []any{}
	if repo != "" {
		conditions = append(conditions, `t.repo=?`)
		arguments = append(arguments, repo)
	}
	if !since.IsZero() {
		conditions = append(conditions, `t.merged_at>=?`)
		arguments = append(arguments, stamp(since))
	}
	merged, err := s.figures(ctx, strings.Join(conditions, ` AND `), arguments...)
	if err != nil {
		return Ledger{}, err
	}
	ledger := Ledger{MergedPRs: len(merged)}
	if ledger.MergedPRs == 0 {
		return ledger, nil
	}
	count := float64(ledger.MergedPRs)
	var cost float64
	costed := false
	for _, figures := range merged {
		ledger.Questions += float64(figures.Questions) / count
		ledger.FeedbackRounds += float64(figures.FeedbackRounds) / count
		ledger.Tokens += float64(figures.Tokens) / count
		if figures.HumanCommits != nil {
			ledger.HumanCommits += float64(*figures.HumanCommits) / count
		}
		if figures.Cost != nil {
			cost += *figures.Cost / count
			costed = true
		}
	}
	if costed {
		ledger.Cost = &cost
	}
	return ledger, nil
}
