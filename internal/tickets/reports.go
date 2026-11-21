package tickets

import (
	"context"
	"strings"
)

// What Machinist has told a tracker about a ticket. Each is told once.
const (
	ReportStarted = "started"
	ReportPR      = "pr"
	ReportFailed  = "failed"
	ReportMerged  = "merged"
	ReportClosed  = "closed"
)

// TrackerJob is a ticket from a tracker that is still going, or that has
// news the tracker has not been told.
type TrackerJob struct {
	TicketID string
	Repo     string
	State    string
	PRNumber int
	// PRState is open, merged or closed, or empty with no pull request.
	PRState   string
	SourceKey string
	// Reported holds what the tracker has been told already.
	Reported map[string]bool
}

// TrackerJobs lists a source's tickets that are working, or whose pull
// request, its merge or close, or a failure has not been reported to the
// source yet.
func (s *Store) TrackerJobs(ctx context.Context, source string) ([]TrackerJob, error) {
	unreported := `NOT EXISTS (SELECT 1 FROM tracker_reports r WHERE r.ticket_id=t.id AND r.kind=?)`
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.repo,t.state,COALESCE(t.pr_number,0),COALESCE(t.pr_state,''),e.source_key,
  COALESCE((SELECT group_concat(r.kind) FROM tracker_reports r WHERE r.ticket_id=t.id),'')
  FROM tickets t JOIN ticket_entries e ON e.ticket_id=t.id AND e.kind=? AND e.source_key IS NOT NULL
  WHERE t.source=? AND (t.state=?
    OR (COALESCE(t.pr_number,0)>0 AND `+unreported+`)
    OR (t.state=? AND `+unreported+`)
    OR (t.pr_state=? AND `+unreported+`)
    OR (t.pr_state=? AND `+unreported+`))
  ORDER BY t.created_at`, EntryRequest, source, StateWorking, ReportPR, StateFailed, ReportFailed, PRMerged, ReportMerged, PRClosed, ReportClosed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []TrackerJob{}
	for rows.Next() {
		var job TrackerJob
		var reported string
		if err := rows.Scan(&job.TicketID, &job.Repo, &job.State, &job.PRNumber, &job.PRState, &job.SourceKey, &reported); err != nil {
			return nil, err
		}
		job.Reported = map[string]bool{}
		for _, kind := range strings.Split(reported, ",") {
			if kind != "" {
				job.Reported[kind] = true
			}
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// MarkReported records that the tracker was told kind about a ticket.
func (s *Store) MarkReported(ctx context.Context, ticketID, kind string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tracker_reports(ticket_id,kind,reported_at) VALUES(?,?,?) ON CONFLICT(ticket_id,kind) DO NOTHING`, ticketID, kind, stamp(s.now()))
	return err
}
