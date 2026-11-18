// Package engine moves tickets through their states and hands runs to runners.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/tickets"
)

const tickInterval = time.Second

// maxAttempts caps the runs of one job. A run only repeats when its runner was
// lost, so a runner that keeps dying cannot retry a job forever.
const maxAttempts = 3

// ErrInvalidCompletion means a runner sent a state Machinist does not know.
var ErrInvalidCompletion = errors.New("invalid run completion")

// PR is a pull request found for a ticket's branch.
type PR struct {
	Number int
	Closed bool
}

// BotEmail is the commit author email of every agent run.
func BotEmail(handle string) string { return handle + "@users.noreply.github.com" }

// PRFinder finds the pull request for a branch, or nil if there is none.
type PRFinder func(ctx context.Context, repository, branch string) (*PR, error)

type Options struct {
	Store      *tickets.Store
	Factory    *factory.Factory
	RunTimeout time.Duration
	Handle     string
	FindPR     PRFinder
	Post       Poster
	Log        io.Writer
}

type Engine struct {
	Options
	now         func() time.Time
	deliverNext time.Time
}

func New(options Options) *Engine {
	if options.Log == nil {
		options.Log = io.Discard
	}
	return &Engine{Options: options, now: time.Now}
}

// Run advances tickets and posts messages until ctx ends. Posting has its own
// loop, so a slow GitHub cannot delay leases or new runs.
func (e *Engine) Run(ctx context.Context) {
	delivered := make(chan struct{})
	go func() { e.runDeliveries(ctx); close(delivered) }()
	defer func() { <-delivered }()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.Tick(ctx); err != nil && ctx.Err() == nil {
				fmt.Fprintf(e.Log, "machinist: engine: %v\n", err)
			}
		}
	}
}

// Tick expires lost leases and queues a run for each ticket that needs one.
func (e *Engine) Tick(ctx context.Context) error {
	if err := e.expireLeases(ctx); err != nil {
		return err
	}
	waiting, err := e.Store.TicketsAwaitingRun(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, ticket := range waiting {
		if err := e.advance(ctx, ticket); err != nil {
			failures = append(failures, fmt.Errorf("ticket %s: %w", ticket.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (e *Engine) advance(ctx context.Context, ticket tickets.Ticket) error {
	attempts, err := e.Store.TurnsSinceHuman(ctx, ticket.ID)
	if err != nil {
		return err
	}
	if attempts >= maxAttempts {
		message := fmt.Sprintf("Stopped after %d attempts, because the runner was lost each time.", maxAttempts)
		return e.Store.Apply(ctx, ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed, Message: message})
	}
	if _, ok := e.Factory.Preset(ticket.Agent); !ok {
		return e.Store.Apply(ctx, ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed, Message: missingPresetMessage(ticket.Agent)})
	}
	// The ticket can leave working between the listing and here. Then there is nothing to run.
	if _, err = e.Store.QueueRun(ctx, ticket.ID, protocol.StepsHarness); errors.Is(err, tickets.ErrNotWorking) {
		return nil
	}
	return err
}

func missingPresetMessage(preset string) string {
	return fmt.Sprintf("Preset %q is not in the factory.", preset)
}

// Assign gives the oldest queued run for these harnesses to a runner. It
// returns nil when there is no work.
func (e *Engine) Assign(ctx context.Context, runnerName string, harnesses []string) (*runner.RunRequest, error) {
	claim, err := e.Store.ClaimRun(ctx, runnerName, harnesses, e.now().Add(LeaseDuration))
	if err != nil || claim == nil {
		return nil, err
	}
	preset, ok := e.Factory.Preset(claim.Ticket.Agent)
	if !ok {
		message := missingPresetMessage(claim.Ticket.Agent)
		return nil, e.Store.CompleteRun(ctx, tickets.RunCompletion{RunID: claim.Run.ID, Status: tickets.RunFailed}, func(tickets.Ticket, string) tickets.Transition {
			return tickets.Transition{State: tickets.StateFailed, Message: message}
		})
	}
	timeout := e.RunTimeout
	if preset.Timeout > 0 {
		timeout = preset.Timeout
	}
	return &runner.RunRequest{
		RunID:         claim.Run.ID,
		TicketID:      claim.Ticket.ID,
		Preset:        preset.Name,
		Repository:    claim.Ticket.Repo,
		Branch:        claim.Ticket.Branch(),
		Vars:          map[string]string{"repo": claim.Ticket.Repo},
		Prompt:        tickets.RenderLog(claim.Entries),
		Files:         e.Factory.Files,
		Steps:         preset.Steps,
		TimeoutMillis: timeout.Milliseconds(),
		GitName:       e.Handle,
		GitEmail:      BotEmail(e.Handle),
	}, nil
}

// Heartbeat renews a run's lease and reports whether the runner should stop it.
func (e *Engine) Heartbeat(ctx context.Context, runID string) (bool, error) {
	return e.Store.RenewLease(ctx, runID, e.now().Add(LeaseDuration))
}

// Complete records the end of a run and applies its row of the state table.
func (e *Engine) Complete(ctx context.Context, runID string, completion protocol.Completion) error {
	switch completion.State {
	case tickets.RunSucceeded, tickets.RunFailed, tickets.RunCancelled, tickets.RunTimedOut:
	default:
		return fmt.Errorf("%w: unknown state %q", ErrInvalidCompletion, completion.State)
	}
	reason := completion.Error
	report := completion.Report
	if report != nil {
		if err := report.Validate(); err != nil {
			reason, report = err.Error(), nil
		}
	}
	pr, err := e.findPR(ctx, runID)
	if err != nil {
		return err
	}
	return e.Store.CompleteRun(ctx, tickets.RunCompletion{RunID: runID, Status: completion.State, Report: report, Tokens: completion.Tokens, Cost: completion.Cost, Stages: completion.Stages}, func(ticket tickets.Ticket, status string) tickets.Transition {
		transition := decide(status, report, reason, pr)
		if pr != nil && ticket.PRNumber == 0 {
			transition.FirstPR = e.firstPR(ticket, pr.Number)
		}
		return transition
	})
}

func (e *Engine) firstPR(ticket tickets.Ticket, number int) *tickets.FirstPR {
	first := &tickets.FirstPR{}
	if ticket.IssueNumber != 0 {
		first.Link = fmt.Sprintf("Opened a PR: https://github.com/%s/pull/%d", ticket.Repo, number)
	}
	return first
}

// findPR looks up the ticket's PR after an agent run. An error is returned to
// the runner, which retries, so a passing GitHub failure cannot lose a report.
func (e *Engine) findPR(ctx context.Context, runID string) (*PR, error) {
	if e.FindPR == nil {
		return nil, nil
	}
	run, err := e.Store.Run(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Kind != tickets.RunKindAgent || run.Status != tickets.RunRunning {
		return nil, nil
	}
	ticket, err := e.Store.Ticket(ctx, run.TicketID)
	if err != nil {
		return nil, err
	}
	return e.FindPR(ctx, ticket.Repo, ticket.Branch())
}

// decide is the state table for the end of a run. A job ends done, with a pull
// request or without one, or failed. It never waits for a person.
func decide(status string, report *protocol.Report, reason string, pr *PR) tickets.Transition {
	transition := tickets.Transition{}
	if pr != nil {
		transition.PRNumber = pr.Number
	}
	if status == tickets.RunInterrupted {
		transition.State = tickets.StateWorking
		return transition
	}
	if status != tickets.RunSucceeded || report == nil {
		if reason == "" {
			reason = status
		}
		transition.State = tickets.StateFailed
		transition.Message = "The run ended without a report: " + reason
		return transition
	}
	transition.Report = report.Outcome + ": " + report.Summary
	switch report.Outcome {
	case protocol.OutcomePRReady:
		if pr != nil && !pr.Closed {
			transition.State = tickets.StatePRReady
		} else {
			transition.State = tickets.StateFailed
			transition.Message = "Reported a PR but none is open for this branch."
		}
	case protocol.OutcomeNoChange:
		transition.State, transition.Message = tickets.StateDone, report.Summary
	default:
		transition.State, transition.Message = tickets.StateFailed, report.Summary
	}
	return transition
}
