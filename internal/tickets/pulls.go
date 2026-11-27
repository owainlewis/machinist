package tickets

import (
	"context"
	"errors"
	"time"
)

// TicketsWithOpenPRs lists the jobs whose pull request was open when
// Machinist last looked, including failed and cancelled jobs whose pull
// request has not yet merged or closed. Once it merges or closes, the job
// leaves this list.
func (s *Store) TicketsWithOpenPRs(ctx context.Context) ([]Ticket, error) {
	rows, err := s.q().QueryContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE state IN (?,?,?) AND COALESCE(pr_number,0)>0 AND COALESCE(pr_state,'') IN ('',?) ORDER BY created_at`,
		StatePRReady, StateFailed, StateCancelled, PROpen)
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

func tracksOpenPR(state string) bool {
	return state == StatePRReady || state == StateFailed || state == StateCancelled
}

func openPRState(prState string) bool {
	return prState == "" || prState == PROpen
}

// PRUpdate is what GitHub says about a job's pull request.
type PRUpdate struct {
	Number int
	State  string
	// At is when the pull request merged or closed, or now while it is open.
	At time.Time
	// OpenedAt is when the pull request was opened. Zero means GitHub did not say.
	OpenedAt time.Time
	// Reviews, when set, are the people's reviews. Machinist reads them once,
	// when the pull request merges.
	Reviews *Reviews
}

// SetPRState records what GitHub says about a job's pull request. listed is
// the job as TicketsWithOpenPRs returned it. For a pr_ready job, a merged or
// closed pull request ends the job as done. For a failed job, a merged pull
// request transitions the job to done, while a closed pull request keeps it
// failed and marks pr_state closed. For a cancelled job, a merged or closed
// pull request updates pr_state (and merged_at/reviews if merged) while
// keeping the job cancelled. A job that has changed since it was listed, such
// as one cancelled or run again meanwhile, is left alone: every new pull
// request result sets pr_state_at, so it marks the generation.
//
// It returns the job as it now stands, and false when the job had changed,
// so the caller acts on nothing it listed. The job returned carries any new
// pr_state_at, so a later SetShipping matches it.
func (s *Store) SetPRState(ctx context.Context, listed Ticket, update PRUpdate) (Ticket, bool, error) {
	ticketID := listed.ID
	var current Ticket
	var same bool
	err := s.inTx(ctx, func(tx querier) error {
		ticket, err := s.loadTicketForUpdate(ctx, tx, ticketID)
		if err != nil {
			return err
		}
		current = ticket
		if !tracksOpenPR(ticket.State) || ticket.State != listed.State || !openPRState(ticket.PRState) || !sameTime(ticket.PRStateAt, listed.PRStateAt) {
			return nil
		}
		same = true
		if !update.OpenedAt.IsZero() {
			// GitHub's time is the true one, so it replaces the time Machinist
			// first saw the pull request.
			if _, err = tx.ExecContext(ctx, `UPDATE tickets SET pr_opened_at=? WHERE id=?`, stamp(update.OpenedAt), ticketID); err != nil {
				return err
			}
		}
		if update.State == PROpen {
			if update.Number != ticket.PRNumber || ticket.PRState != PROpen {
				if _, err = tx.ExecContext(ctx, `UPDATE tickets SET pr_number=?,pr_state=?,pr_state_at=?,updated_at=? WHERE id=?`, update.Number, PROpen, stamp(update.At), stamp(s.now()), ticketID); err != nil {
					return err
				}
			}
		} else {
			nextState := ticket.State
			switch {
			case ticket.State == StatePRReady:
				nextState = StateDone
			case ticket.State == StateFailed && update.State == PRMerged:
				nextState = StateDone
			}
			var merged any
			if update.State == PRMerged {
				merged = stamp(update.At)
			}
			if _, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,pr_number=?,pr_state=?,pr_state_at=?,merged_at=COALESCE(?,merged_at),shipping=NULL,updated_at=? WHERE id=?`,
				nextState, update.Number, update.State, stamp(update.At), merged, stamp(s.now()), ticketID); err != nil {
				return err
			}
			if err = setReviews(ctx, tx, ticketID, update.Reviews); err != nil {
				return err
			}
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
	// Reviews, when set, are the people's reviews of a pull request that merged.
	Reviews *Reviews
}

// SetShipping records a look at the pull request of a job that waits for its
// checks. listed is the job as TicketsWithOpenPRs returned it. A job that has
// changed since, such as one that ran again meanwhile, is left alone, as in
// SetPRState. It returns the updated ticket and true if the ticket changed.
func (s *Store) SetShipping(ctx context.Context, listed Ticket, shipped Shipped) (Ticket, bool, error) {
	var updated Ticket
	var changed bool
	err := s.inTx(ctx, func(tx querier) error {
		ticket, err := s.loadTicketForUpdate(ctx, tx, listed.ID)
		if err != nil || ticket.State != StatePRReady || ticket.Shipping == "" || !sameTime(ticket.PRStateAt, listed.PRStateAt) {
			return err
		}
		now := s.now()
		switch {
		case shipped.Merged:
			_, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,pr_state=?,pr_state_at=?,merged_at=COALESCE(merged_at,?),shipping=NULL,updated_at=? WHERE id=?`,
				StateDone, PRMerged, stamp(shipped.At), stamp(shipped.At), stamp(now), ticket.ID)
			if err == nil {
				err = setReviews(ctx, tx, ticket.ID, shipped.Reviews)
			}
		case shipped.Failed:
			_, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,shipping=NULL,updated_at=? WHERE id=?`, StateFailed, stamp(now), ticket.ID)
		default:
			if shipped.Shipping == ticket.Shipping {
				return nil
			}
			_, err = tx.ExecContext(ctx, `UPDATE tickets SET shipping=?,updated_at=? WHERE id=?`, nullableString(shipped.Shipping), stamp(now), ticket.ID)
		}
		if err != nil {
			return err
		}
		if shipped.Message != "" {
			if err = insertMessage(ctx, tx, ticket, shipped.Message, nil, now); err != nil {
				return err
			}
		}
		updated, err = loadTicket(ctx, tx, ticket.ID)
		if err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return Ticket{}, false, err
	}
	return updated, changed, nil
}

// StewardRequeueRequest describes an automatic PR Steward requeue after a
// failing CI check or merge conflict.
type StewardRequeueRequest struct {
	// MaxRetries is the configured automatic steward retry cap.
	MaxRetries int
	// Prompt is the system timeline and follow-up instructions entry added as
	// EntryCI so the next run sees what failed and how to fix it.
	Prompt string
}

// StewardRequeue clears a waiting pull request's shipping watch, increments
// its persisted steward_retries count, appends the steward timeline/prompt
// entry, and returns the ticket to working on the same branch without
// recording a human touch (TouchRerun). It returns false without modifying the
// ticket when the ticket has changed since listed, already has an active run,
// or has reached request.MaxRetries.
func (s *Store) StewardRequeue(ctx context.Context, listed Ticket, request StewardRequeueRequest) (Ticket, bool, error) {
	var updated Ticket
	var requeued bool
	err := s.inTx(ctx, func(tx querier) error {
		ticket, err := s.loadTicketForUpdate(ctx, tx, listed.ID)
		if err != nil || ticket.State != StatePRReady || ticket.Shipping == "" || !sameTime(ticket.PRStateAt, listed.PRStateAt) {
			return err
		}
		if request.MaxRetries <= 0 || ticket.StewardRetries >= request.MaxRetries {
			return nil
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE ticket_id=? AND status IN (?,?)`, ticket.ID, RunQueued, RunRunning).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return nil
		}
		now := s.now()
		if err = insertEntry(ctx, tx, ticket.ID, EntryCI, authorMachinist, request.Prompt, nil, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,pr_state=NULL,pr_state_at=NULL,shipping=NULL,merge_gate_decision='',merge_gate_risk='',merge_gate_reason='',resume_from_step='',resume_checkpoint_run='',steward_retries=steward_retries+1,updated_at=? WHERE id=?`, StateWorking, stamp(now), ticket.ID); err != nil {
			return err
		}
		updated, err = loadTicket(ctx, tx, ticket.ID)
		if err != nil {
			return err
		}
		requeued = true
		return nil
	})
	if err != nil {
		return Ticket{}, false, err
	}
	return updated, requeued, nil
}

// StillShipping reports whether listed is still waiting to ship on the same
// pull request generation. It lets the control plane re-check right before a
// GitHub merge or branch update, so a job cancelled or rerun while checks were
// fetched is not mutated on GitHub.
func (s *Store) StillShipping(ctx context.Context, listed Ticket) (bool, error) {
	var ok bool
	err := s.inTx(ctx, func(tx querier) error {
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

// setReviews records the people's reviews of a job's pull request. Nil
// leaves what is stored.
func setReviews(ctx context.Context, q querier, ticketID string, reviews *Reviews) error {
	if reviews == nil {
		return nil
	}
	var first any
	if reviews.First != nil {
		first = stamp(*reviews.First)
	}
	_, err := q.ExecContext(ctx, `UPDATE tickets SET human_reviews=?,first_review_at=? WHERE id=?`, reviews.Count, first, ticketID)
	return err
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
