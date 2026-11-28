package tickets

import (
	"context"
	"strings"
)

// What Machinist has told a tracker about a ticket. Each is told once.
const (
	ReportStarted = "started"
	ReportGate    = "gate"
	ReportPR      = "pr"
	ReportFailed  = "failed"
	ReportMerged  = "merged"
	ReportClosed  = "closed"
	// ReportFinished is a job that ended done with no pull request.
	ReportFinished = "finished"
)

// TrackerJob is a ticket from a tracker that is still going, or that has
// news the tracker has not been told.
type TrackerJob struct {
	TicketID string
	Repo     string
	State    string
	PRNumber int
	// PRState is open, merged or closed, or empty with no pull request.
	PRState string
	// GateMessage is the static prompt configured on the active gate step.
	// Gate details are never exposed to external trackers.
	GateMessage string
	SourceKey   string
	// Reported holds what the tracker has been told already.
	Reported map[string]bool
}

// TrackerJobs lists a source's tickets that are working or waiting at a gate,
// or whose pull request, its merge or close, or a failure has not been reported
// to the source yet.
func (s *Store) TrackerJobs(ctx context.Context, source string) ([]TrackerJob, error) {
	unreported := `NOT EXISTS (SELECT 1 FROM tracker_reports r WHERE r.ticket_id=t.id AND r.kind=?)`
	var jobs []TrackerJob
	err := s.inTxReadOnly(ctx, func(tx querier) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.id,t.repo,t.state,COALESCE(t.pr_number,0),COALESCE(t.pr_state,''),COALESCE(t.gate_message,''),e.source_key
  FROM tickets t JOIN ticket_entries e ON e.ticket_id=t.id AND e.kind=? AND e.source_key IS NOT NULL
  WHERE t.source=? AND (t.state IN (?,?)
    OR (COALESCE(t.pr_number,0)>0 AND `+unreported+`)
    OR (t.state=? AND `+unreported+`)
    OR (t.pr_state=? AND `+unreported+`)
    OR (t.pr_state=? AND `+unreported+`)
    OR (t.state=? AND COALESCE(t.pr_number,0)=0 AND `+unreported+`))
  ORDER BY t.created_at`, EntryRequest, source, StateWorking, StateWaiting, ReportPR, StateFailed, ReportFailed, PRMerged, ReportMerged, PRClosed, ReportClosed, StateDone, ReportFinished)
		if err != nil {
			return err
		}
		jobs = []TrackerJob{}
		for rows.Next() {
			var job TrackerJob
			if err := rows.Scan(&job.TicketID, &job.Repo, &job.State, &job.PRNumber, &job.PRState, &job.GateMessage, &job.SourceKey); err != nil {
				rows.Close()
				return err
			}
			job.Reported = map[string]bool{}
			jobs = append(jobs, job)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(jobs) == 0 {
			return nil
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(jobs)), ",")
		args := make([]any, len(jobs))
		indexByID := make(map[string]int, len(jobs))
		for i, job := range jobs {
			args[i] = job.TicketID
			indexByID[job.TicketID] = i
		}
		repRows, err := tx.QueryContext(ctx, `SELECT ticket_id,kind FROM tracker_reports WHERE ticket_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		defer repRows.Close()
		for repRows.Next() {
			var ticketID, kind string
			if err := repRows.Scan(&ticketID, &kind); err != nil {
				return err
			}
			if idx, ok := indexByID[ticketID]; ok && kind != "" {
				jobs[idx].Reported[kind] = true
			}
		}
		return repRows.Err()
	})
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

// MarkReported records that the tracker was told kind about a ticket.
func (s *Store) MarkReported(ctx context.Context, ticketID, kind string) error {
	_, err := s.q().ExecContext(ctx, `INSERT INTO tracker_reports(ticket_id,kind,reported_at) VALUES(?,?,?) ON CONFLICT(ticket_id,kind) DO NOTHING`, ticketID, kind, stamp(s.now()))
	return err
}
