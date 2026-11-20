package tickets

import (
	"context"
	"database/sql"
	"time"
)

// TicketsWithOpenPRs lists the jobs whose pull request was open when
// Machinist last looked. Once it merges or closes, the job leaves this list.
func (s *Store) TicketsWithOpenPRs(ctx context.Context) ([]Ticket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE state=? AND COALESCE(pr_number,0)>0 ORDER BY created_at`, StatePRReady)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []Ticket
	for rows.Next() {
		ticket, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, ticket)
	}
	return found, rows.Err()
}

// SetPRState records what GitHub says about a job's pull request. A merged or
// closed pull request ends the job as done. A job that has moved on since it
// was read, such as one run again, is left alone.
func (s *Store) SetPRState(ctx context.Context, ticketID string, number int, state string, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		ticket, err := loadTicket(ctx, tx, ticketID)
		if err != nil || ticket.State != StatePRReady {
			return err
		}
		if state == PROpen {
			if number == ticket.PRNumber && ticket.PRState == PROpen {
				return nil
			}
			_, err = tx.ExecContext(ctx, `UPDATE tickets SET pr_number=?,pr_state=?,pr_state_at=?,updated_at=? WHERE id=?`, number, PROpen, stamp(at), stamp(s.now()), ticketID)
			return err
		}
		var merged any
		if state == PRMerged {
			merged = stamp(at)
		}
		_, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,pr_number=?,pr_state=?,pr_state_at=?,merged_at=COALESCE(?,merged_at),updated_at=? WHERE id=?`,
			StateDone, number, state, stamp(at), merged, stamp(s.now()), ticketID)
		return err
	})
}
