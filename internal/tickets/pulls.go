package tickets

import (
	"context"
	"database/sql"
	"errors"
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

// SetPRState records what GitHub says about a job's pull request. listed is
// the job as TicketsWithOpenPRs returned it. A merged or closed pull request
// ends the job as done. A job that has changed since it was listed, such as
// one run again that opened a new pull request meanwhile, is left alone:
// every new pull request result sets pr_state_at, so it marks the generation.
//
// It returns the job as it now stands, and false when the job had changed,
// so the caller acts on nothing it listed. The job returned carries any new
// pr_state_at, so a later SetShipping matches it.
func (s *Store) SetPRState(ctx context.Context, listed Ticket, number int, state string, at time.Time) (Ticket, bool, error) {
	ticketID := listed.ID
	var current Ticket
	var same bool
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		ticket, err := loadTicket(ctx, tx, ticketID)
		if err != nil {
			return err
		}
		current = ticket
		if ticket.State != StatePRReady || !sameTime(ticket.PRStateAt, listed.PRStateAt) {
			return nil
		}
		same = true
		if state == PROpen && number == ticket.PRNumber && ticket.PRState == PROpen {
			return nil
		}
		if state == PROpen {
			_, err = tx.ExecContext(ctx, `UPDATE tickets SET pr_number=?,pr_state=?,pr_state_at=?,updated_at=? WHERE id=?`, number, PROpen, stamp(at), stamp(s.now()), ticketID)
		} else {
			var merged any
			if state == PRMerged {
				merged = stamp(at)
			}
			_, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,pr_number=?,pr_state=?,pr_state_at=?,merged_at=COALESCE(?,merged_at),shipping=NULL,updated_at=? WHERE id=?`,
				StateDone, number, state, stamp(at), merged, stamp(s.now()), ticketID)
		}
		if err != nil {
			return err
		}
		current, err = loadTicket(ctx, tx, ticketID)
		return err
	})
	if err != nil {
		return Ticket{}, false, err
	}
	return current, same, nil
}

// Shipped is what one look at a job's waiting pull request changed.
type Shipped struct {
	// Shipping is what to keep for the next look, or empty when the wait is
	// over.
	Shipping string
	// Merged ends the job done, with its pull request merged at At.
	Merged bool
	// Failed ends the job failed, because a check failed.
	Failed bool
	At     time.Time
	// Message, when set, tells the person what happened.
	Message string
}

// SetShipping records a look at the pull request of a job that waits for its
// checks. listed is the job as TicketsWithOpenPRs returned it. A job that has
// changed since, such as one that ran again meanwhile, is left alone, as in
// SetPRState.
func (s *Store) SetShipping(ctx context.Context, listed Ticket, shipped Shipped) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		ticket, err := loadTicket(ctx, tx, listed.ID)
		if err != nil || ticket.State != StatePRReady || ticket.Shipping == "" || !sameTime(ticket.PRStateAt, listed.PRStateAt) {
			return err
		}
		now := s.now()
		switch {
		case shipped.Merged:
			_, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,pr_state=?,pr_state_at=?,merged_at=COALESCE(merged_at,?),shipping=NULL,updated_at=? WHERE id=?`,
				StateDone, PRMerged, stamp(shipped.At), stamp(shipped.At), stamp(now), ticket.ID)
		case shipped.Failed:
			_, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,shipping=NULL,updated_at=? WHERE id=?`, StateFailed, stamp(now), ticket.ID)
		default:
			if shipped.Shipping == ticket.Shipping {
				return nil
			}
			_, err = tx.ExecContext(ctx, `UPDATE tickets SET shipping=?,updated_at=? WHERE id=?`, nullableString(shipped.Shipping), stamp(now), ticket.ID)
		}
		if err != nil || shipped.Message == "" {
			return err
		}
		return insertMessage(ctx, tx, ticket, ticket.PRNumber, shipped.Message, nil, now)
	})
}

// StillShipping reports whether listed is still waiting to ship on the same
// pull request generation. It lets the control plane re-check right before a
// GitHub merge or branch update, so a job cancelled or rerun while checks were
// fetched is not mutated on GitHub.
func (s *Store) StillShipping(ctx context.Context, listed Ticket) (bool, error) {
	var ok bool
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		ticket, err := loadTicket(ctx, tx, listed.ID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		ok = ticket.State == StatePRReady && ticket.Shipping != "" && sameTime(ticket.PRStateAt, listed.PRStateAt)
		return nil
	})
	return ok, err
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
