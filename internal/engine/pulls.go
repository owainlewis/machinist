package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/ghclient"
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
// merges, it also reads who reviewed it. Machinist finds the pull request by
// the branch it named, never by asking the agent.
//
// A non-rate-limit failure on one job or repository is collected so other
// repositories keep being tracked; a rate-limit error stops the round early so
// the rate limit is not made worse.
func (e *Engine) CheckPRs(ctx context.Context) error {
	if e.FindPR == nil {
		return nil
	}
	open, err := e.Store.TicketsWithOpenPRs(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, listed := range open {
		if err := e.checkPR(ctx, listed); err != nil {
			failures = append(failures, err)
			if ghclient.IsRateLimit(err) || ctx.Err() != nil {
				break
			}
		}
	}
	return errors.Join(failures...)
}

func (e *Engine) checkPR(ctx context.Context, listed tickets.Ticket) error {
	pr, err := e.FindPR(ctx, listed.Repo, listed.Branch())
	if err != nil {
		return fmt.Errorf("job %s (%s): %w", listed.ID, listed.Repo, err)
	}
	if pr == nil {
		return nil
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
		reviews, err := e.Reviews(ctx, listed.Repo, pr.Number, at)
		if err != nil {
			return fmt.Errorf("job %s (%s): reviews: %w", listed.ID, listed.Repo, err)
		}
		update.Reviews = &reviews
	}
	ticket, same, err := e.Store.SetPRState(ctx, listed, update)
	if err != nil {
		return fmt.Errorf("job %s (%s): %w", listed.ID, listed.Repo, err)
	}
	if !same {
		// The job was cancelled or run again while GitHub was asked, so
		// what was asked about it is out of date.
		return nil
	}
	if state != tickets.PROpen {
		fmt.Fprintf(e.Log, "machinist: job %s: pull request #%d %s\n", ticket.ID, pr.Number, state)
		return nil
	}
	// The job as it now stands, so a cancel or rerun is seen and a look
	// is recorded against its newest pull request result. Terminal jobs
	// (failed or cancelled) only reconcile PR state and never run shipping
	// checks, auto-merge, or PR Steward.
	if ticket.State == tickets.StatePRReady && ticket.Shipping != "" {
		if err := e.lookAt(ctx, ticket, pr.Number); err != nil {
			return fmt.Errorf("job %s (%s): pull request #%d: %w", ticket.ID, listed.Repo, pr.Number, err)
		}
	}
	return nil
}

// startWatch is what a job keeps while the control plane waits for the
// checks of the pull request its run handed over. The run is over, so no
// runner waits on CI.
func (e *Engine) startWatch(request protocol.Ship) string {
	// A Watch is strings, a time and numbers, so it always encodes.
	body, _ := json.Marshal(ship.NewWatch(e.autoMerge(), request, e.now()))
	return string(body)
}

// lookAt takes one look at the checks of a job's open pull request. It merges
// the pull request once every check passed, when the watch allows it, and
// fails the job when a check failed. A check still running waits for the
// next look.
func (e *Engine) lookAt(ctx context.Context, ticket tickets.Ticket, number int) error {
	if e.PullRequests == nil || ticket.State != tickets.StatePRReady {
		return nil
	}
	var watch ship.Watch
	if err := json.Unmarshal([]byte(ticket.Shipping), &watch); err != nil {
		// What an older Machinist kept cannot be read, so leave the pull
		// request for a person rather than guess.
		fmt.Fprintf(e.Log, "machinist: job %s: pull request #%d: stopped waiting for checks: %v\n", ticket.ID, number, err)
		return e.setShipping(ctx, ticket, tickets.Shipped{})
	}
	watchForLook := watch
	autoMergeTurnedOff := !e.autoMerge() && watchForLook.Hold == ""
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
		// Persist any normalized watch state (for example, a branch update's
		// new head) before retrying so a later push cannot be mistaken for
		// Machinist's own branch update.
		if look.Outcome == ship.Waiting {
			if autoMergeTurnedOff {
				look.Watch.Hold = watch.Hold
			}
			if body, marshalErr := json.Marshal(look.Watch); marshalErr == nil {
				_ = e.setShipping(ctx, ticket, tickets.Shipped{Shipping: string(body), At: e.now()})
			}
		}
		return err
	}
	if look.Steward != nil {
		maxRetries := e.stewardRetries()
		if maxRetries > 0 && ticket.StewardRetries < maxRetries {
			attempt := ticket.StewardRetries + 1
			prompt := stewardPrompt(ticket.Repo, number, attempt, maxRetries, look.Steward)
			_, requeued, err := e.Store.StewardRequeue(ctx, ticket, tickets.StewardRequeueRequest{
				MaxRetries: maxRetries,
				Prompt:     prompt,
			})
			if err != nil {
				return err
			}
			if requeued {
				fmt.Fprintf(e.Log, "machinist: job %s: pull request #%d: steward attempt %d/%d: %s; requeueing\n",
					ticket.ID, number, attempt, maxRetries, stewardLogReason(look.Steward))
				return nil
			}
			current, loadErr := e.Store.Ticket(ctx, ticket.ID)
			if loadErr != nil ||
				current.State != tickets.StatePRReady ||
				current.Shipping == "" ||
				!sameTime(current.PRStateAt, ticket.PRStateAt) ||
				current.StewardRetries < maxRetries {
				return nil
			}
			ticket = current
		}
		if maxRetries > 0 && ticket.StewardRetries >= maxRetries {
			fmt.Fprintf(e.Log, "machinist: job %s: pull request #%d: steward retries exhausted (%d/%d): %s\n",
				ticket.ID, number, ticket.StewardRetries, maxRetries, look.Log)
			look.Message = stewardExhaustedMessage(look.Message, ticket.StewardRetries, maxRetries)
			look.Log = ""
		}
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
		if e.Reviews != nil {
			if reviews, err := e.Reviews(ctx, ticket.Repo, number, shipped.At); err != nil {
				fmt.Fprintf(e.Log, "machinist: job %s: pull request #%d: reviews: %v\n", ticket.ID, number, err)
			} else {
				shipped.Reviews = &reviews
			}
		}
	case ship.CheckFailed:
		shipped.Failed = true
	}
	return e.setShipping(ctx, ticket, shipped)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func stewardLogReason(s *ship.Steward) string {
	switch s.Reason {
	case ship.StewardMergeConflict:
		if s.Detail != "" {
			return "merge conflict (" + s.Detail + ")"
		}
		return "merge conflict"
	default:
		return s.Detail
	}
}

func stewardExhaustedMessage(message string, used, maxRetries int) string {
	note := fmt.Sprintf("PR Steward exhausted %d/%d retries", used, maxRetries)
	if message == "" {
		return note
	}
	return fmt.Sprintf("%s (%s)", message, note)
}

func stewardSummary(number, attempt, maxRetries int, s *ship.Steward) string {
	switch s.Reason {
	case ship.StewardCheckFailed:
		if s.Link != "" {
			return fmt.Sprintf("PR Steward (attempt %d/%d): check %q failed (%s). Requeueing to rebase onto main and fix.", attempt, maxRetries, s.Check, s.Link)
		}
		return fmt.Sprintf("PR Steward (attempt %d/%d): check %q failed. Requeueing to rebase onto main and fix.", attempt, maxRetries, s.Check)
	case ship.StewardMergeConflict:
		if s.Detail != "" {
			return fmt.Sprintf("PR Steward (attempt %d/%d): merge conflict on pull request #%d (%s). Requeueing to rebase onto main and resolve conflicts.", attempt, maxRetries, number, s.Detail)
		}
		return fmt.Sprintf("PR Steward (attempt %d/%d): merge conflict on pull request #%d. Requeueing to rebase onto main and resolve conflicts.", attempt, maxRetries, number)
	default:
		return fmt.Sprintf("PR Steward (attempt %d/%d): %s. Requeueing to fix.", attempt, maxRetries, s.Detail)
	}
}

func stewardPrompt(repo string, number, attempt, maxRetries int, s *ship.Steward) string {
	prURL := s.PRURL
	if prURL == "" {
		prURL = fmt.Sprintf("https://github.com/%s/pull/%d", repo, number)
	}
	var b strings.Builder
	b.WriteString(stewardSummary(number, attempt, maxRetries, s))
	b.WriteString("\n\n")
	switch s.Reason {
	case ship.StewardCheckFailed:
		if s.Link != "" {
			fmt.Fprintf(&b, "Pull request #%d (%s) failed CI check %q (%s).\n\n", number, prURL, s.Check, s.Link)
		} else {
			fmt.Fprintf(&b, "Pull request #%d (%s) failed CI check %q.\n\n", number, prURL, s.Check)
		}
		b.WriteString("Follow these steps on this pull request branch to bring it back to green:\n")
		b.WriteString("1. Fetch the latest default branch (`git fetch origin`) and merge `origin/HEAD` into this branch (prefer merging over rebasing published commits so the pipeline's non-force push stays fast-forward).\n")
		b.WriteString("2. Resolve any merge conflicts cleanly without dropping either branch's intent.\n")
		fmt.Fprintf(&b, "3. Inspect and fix the failing check (%q), reproducing the failure locally.\n", s.Check)
		b.WriteString("4. Run the repository's tests, linter, and type checker locally and confirm they pass.\n")
		b.WriteString("5. Commit your fixes on this branch. Do not push, open a new pull request, or merge — the pipeline will push the updated branch and re-run review and merge-gate.")
	case ship.StewardMergeConflict:
		fmt.Fprintf(&b, "Pull request #%d (%s) has merge conflicts with the base branch (%s).\n\n", number, prURL, s.Detail)
		b.WriteString("Follow these steps on this pull request branch to resolve the conflicts and bring it back to green:\n")
		b.WriteString("1. Fetch the latest default branch (`git fetch origin`) and merge `origin/HEAD` into this branch (prefer merging over rebasing published commits so the pipeline's non-force push stays fast-forward).\n")
		b.WriteString("2. Resolve all merge conflicts cleanly, preserving both the base branch changes and this pull request's intent.\n")
		b.WriteString("3. Run the repository's tests, linter, and type checker locally to catch any semantic conflicts or regressions.\n")
		b.WriteString("4. Commit the resolution on this branch. Do not push, open a new pull request, or merge — the pipeline will push the updated branch and re-run review and merge-gate.")
	default:
		fmt.Fprintf(&b, "Pull request #%d (%s) needs attention: %s.\n\n", number, prURL, s.Detail)
		b.WriteString("Fetch `origin`, merge `origin/HEAD`, fix the issue, run the repository's checks locally, and commit your fixes on this branch.")
	}
	return b.String()
}
