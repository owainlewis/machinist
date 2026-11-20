package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	defaultPRInterval = time.Minute
	maxPRBackoff      = 5 * time.Minute
)

// runPRChecks checks open pull requests every PRInterval until ctx ends. After
// a failure it waits twice as long as before, up to five minutes.
func (e *Engine) runPRChecks(ctx context.Context) {
	interval := e.PRInterval
	if interval <= 0 {
		interval = defaultPRInterval
	}
	wait := interval
	for {
		if !e.wait(ctx, wait) {
			return
		}
		err := e.CheckPRs(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			wait = min(max(wait*2, interval), maxPRBackoff)
			fmt.Fprintf(e.Log, "machinist: pull requests: %v; next check in %s\n", err, wait)
		default:
			wait = interval
		}
	}
}

func (e *Engine) wait(ctx context.Context, d time.Duration) bool {
	if e.sleep != nil {
		return e.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// CheckPRs asks GitHub about the pull request on each job's branch while it is
// open, one call per job, and records when it merges or closes. Machinist
// finds the pull request by the branch it named, never by asking the agent.
func (e *Engine) CheckPRs(ctx context.Context) error {
	if e.FindPR == nil {
		return nil
	}
	open, err := e.Store.TicketsWithOpenPRs(ctx)
	if err != nil {
		return err
	}
	for _, ticket := range open {
		pr, err := e.FindPR(ctx, ticket.Repo, ticket.Branch())
		if err != nil {
			// Stop at the first failure, so a rate limit is not made worse.
			return fmt.Errorf("job %s: %w", ticket.ID, err)
		}
		if pr == nil {
			continue
		}
		state := tickets.PROpen
		switch {
		case pr.Merged:
			state = tickets.PRMerged
		case pr.Closed:
			state = tickets.PRClosed
		}
		at := pr.ChangedAt
		if at.IsZero() {
			at = e.now()
		}
		if err := e.Store.SetPRState(ctx, ticket.ID, pr.Number, state, at); err != nil {
			return fmt.Errorf("job %s: %w", ticket.ID, err)
		}
		if state != tickets.PROpen {
			fmt.Fprintf(e.Log, "machinist: job %s: pull request #%d %s\n", ticket.ID, pr.Number, state)
		}
	}
	return nil
}
