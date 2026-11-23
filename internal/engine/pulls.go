package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/ship"
	"github.com/owainlewis/machinist/internal/tickets"
)

var errShippingStopped = errors.New("job is no longer shipping")

// guardedPull wraps ship.GitHub so Merge and UpdateBranch re-verify that the
// job is still waiting to ship on the same generation before mutating GitHub.
type guardedPull struct {
	inner   ship.GitHub
	store   *tickets.Store
	ticket  tickets.Ticket
	stopped bool
}

func (g *guardedPull) PullRequest(ctx context.Context) (ship.PullRequest, error) {
	if g.stopped {
		return ship.PullRequest{}, errShippingStopped
	}
	return g.inner.PullRequest(ctx)
}

func (g *guardedPull) Checks(ctx context.Context) ([]ship.Check, error) {
	if g.stopped {
		return nil, errShippingStopped
	}
	return g.inner.Checks(ctx)
}

func (g *guardedPull) checkStillShipping(ctx context.Context) error {
	ok, err := g.store.StillShipping(ctx, g.ticket)
	if err != nil {
		return err
	}
	if !ok {
		g.stopped = true
		return errShippingStopped
	}
	return nil
}

func (g *guardedPull) Merge(ctx context.Context, head string) error {
	if err := g.checkStillShipping(ctx); err != nil {
		return err
	}
	return g.inner.Merge(ctx, head)
}

func (g *guardedPull) UpdateBranch(ctx context.Context) error {
	if err := g.checkStillShipping(ctx); err != nil {
		return err
	}
	return g.inner.UpdateBranch(ctx)
}

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
// open, one call per job, and records when it merges or closes. When one
// merges, it also reads who reviewed it. Machinist
// finds the pull request by the branch it named, never by asking the agent.
func (e *Engine) CheckPRs(ctx context.Context) error {
	if e.FindPR == nil {
		return nil
	}
	open, err := e.Store.TicketsWithOpenPRs(ctx)
	if err != nil {
		return err
	}
	for _, listed := range open {
		pr, err := e.FindPR(ctx, listed.Repo, listed.Branch())
		if err != nil {
			// Stop at the first failure, so a rate limit is not made worse.
			return fmt.Errorf("job %s: %w", listed.ID, err)
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
		update := tickets.PRUpdate{Number: pr.Number, State: state, At: at, OpenedAt: pr.OpenedAt}
		if state == tickets.PRMerged && e.Reviews != nil {
			reviews, err := e.Reviews(ctx, listed.Repo, pr.Number)
			if err != nil {
				return fmt.Errorf("job %s: reviews: %w", listed.ID, err)
			}
			update.Reviews = &reviews
		}
		ticket, same, err := e.Store.SetPRState(ctx, listed, update)
		if err != nil {
			return fmt.Errorf("job %s: %w", listed.ID, err)
		}
		if !same {
			// The job was cancelled or run again while GitHub was asked, so
			// what was asked about it is out of date.
			continue
		}
		if state != tickets.PROpen {
			fmt.Fprintf(e.Log, "machinist: job %s: pull request #%d %s\n", ticket.ID, pr.Number, state)
			continue
		}
		// The job as it now stands, so a cancel or rerun is seen and a look
		// is recorded against its newest pull request result.
		if ticket.Shipping != "" {
			if err := e.lookAt(ctx, ticket, pr.Number); err != nil {
				return fmt.Errorf("job %s: pull request #%d: %w", ticket.ID, pr.Number, err)
			}
		}
	}
	return nil
}

// startWatch is what a job keeps while the control plane waits for the
// checks of the pull request its run handed over. The run is over, so no
// runner waits on CI.
func (e *Engine) startWatch(request protocol.Ship) string {
	// A Watch is strings, a time and numbers, so it always encodes.
	body, _ := json.Marshal(ship.NewWatch(e.AutoMerge, request, e.now()))
	return string(body)
}

// lookAt takes one look at the checks of a job's open pull request. It merges
// the pull request once every check passed, when the watch allows it, and
// fails the job when a check failed. A check still running waits for the
// next look.
func (e *Engine) lookAt(ctx context.Context, ticket tickets.Ticket, number int) error {
	if e.PullRequests == nil {
		return nil
	}
	var watch ship.Watch
	if err := json.Unmarshal([]byte(ticket.Shipping), &watch); err != nil {
		// What an older Machinist kept cannot be read, so leave the pull
		// request for a person rather than guess.
		fmt.Fprintf(e.Log, "machinist: job %s: pull request #%d: stopped waiting for checks: %v\n", ticket.ID, number, err)
		return e.Store.SetShipping(ctx, ticket, tickets.Shipped{})
	}
	watchForLook := watch
	autoMergeTurnedOff := !e.AutoMerge && watchForLook.Hold == ""
	if autoMergeTurnedOff {
		// Re-check github.auto_merge on every look so turning it off and
		// restarting the control plane stops in-flight pull requests from
		// merging, while still failing the job if a check fails.
		watchForLook.Hold = "auto-merge is off"
	}
	guarded := &guardedPull{inner: e.PullRequests(ticket.Repo, number), store: e.Store, ticket: ticket}
	look, err := ship.LookOnce(ctx, guarded, watchForLook, e.now())
	if guarded.stopped || errors.Is(err, errShippingStopped) {
		return nil
	}
	if err != nil {
		return err
	}
	if look.Log != "" {
		fmt.Fprintf(e.Log, "machinist: job %s: pull request #%d: %s\n", ticket.ID, number, look.Log)
	}
	shipped := tickets.Shipped{Message: look.Message, At: e.now()}
	switch look.Outcome {
	case ship.Settled:
		// A person merged or closed it, which the next look records.
		return nil
	case ship.Waiting:
		if autoMergeTurnedOff {
			look.Watch.Hold = watch.Hold
		}
		body, err := json.Marshal(look.Watch)
		if err != nil {
			return err
		}
		shipped.Shipping = string(body)
	case ship.LeftOpen:
		if autoMergeTurnedOff && shipped.Message == "" {
			shipped.Message = fmt.Sprintf("Left open: auto-merge is off. https://github.com/%s/pull/%d", ticket.Repo, number)
		}
	case ship.Merged:
		shipped.Merged = true
	case ship.CheckFailed:
		shipped.Failed = true
	}
	return e.Store.SetShipping(ctx, ticket, shipped)
}
