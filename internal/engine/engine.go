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

// ErrInvalidCompletion means a runner sent a state Machinist does not know.
var ErrInvalidCompletion = errors.New("invalid run completion")

// PR is a pull request found for a ticket's branch.
type PR struct {
	Number int
}

// PRFinder finds the pull request for a branch, or nil if there is none.
type PRFinder func(ctx context.Context, repository, branch string) (*PR, error)

type Options struct {
	Store      *tickets.Store
	Factory    *factory.Factory
	MaxTurns   int
	RunTimeout time.Duration
	Handle     string
	FindPR     PRFinder
	Log        io.Writer
}

type Engine struct {
	Options
	now func() time.Time
}

func New(options Options) *Engine {
	if options.Log == nil {
		options.Log = io.Discard
	}
	return &Engine{Options: options, now: time.Now}
}

// Run advances tickets until ctx ends.
func (e *Engine) Run(ctx context.Context) {
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
	turns, err := e.Store.TurnsSinceHuman(ctx, ticket.ID)
	if err != nil {
		return err
	}
	if turns >= e.MaxTurns {
		message, err := e.Store.LastReport(ctx, ticket.ID)
		if err != nil {
			return err
		}
		if message == "" {
			message = fmt.Sprintf("Reached the limit of %d runs without human input.", e.MaxTurns)
		}
		return e.Store.Apply(ctx, ticket.ID, tickets.StateWorking, tickets.Transition{State: waitingState(ticket), Message: message})
	}
	agent, ok := e.Factory.Agents[ticket.Agent]
	if !ok {
		return e.Store.Apply(ctx, ticket.ID, tickets.StateWorking, tickets.Transition{State: waitingState(ticket), Message: missingAgentMessage(ticket.Agent)})
	}
	_, err = e.Store.QueueRun(ctx, ticket.ID, tickets.RunKindAgent, agent.Harness)
	return err
}

func missingAgentMessage(agent string) string {
	return fmt.Sprintf("Agent %q is not in the factory.", agent)
}

// waitingState is where a ticket goes when it needs a person. An automation
// never waits, so one bad run cannot stop its schedule.
func waitingState(ticket tickets.Ticket) string {
	if ticket.Automation() {
		return tickets.StateFailed
	}
	return tickets.StateNeedsYou
}

// Assign gives the oldest queued run for these harnesses to a runner. It
// returns nil when there is no work.
func (e *Engine) Assign(ctx context.Context, runnerName string, harnesses []string) (*runner.RunRequest, error) {
	claim, err := e.Store.ClaimRun(ctx, runnerName, harnesses, e.now().Add(LeaseDuration))
	if err != nil || claim == nil {
		return nil, err
	}
	agent, ok := e.Factory.Agents[claim.Ticket.Agent]
	if !ok {
		message := missingAgentMessage(claim.Ticket.Agent)
		return nil, e.Store.CompleteRun(ctx, tickets.RunCompletion{RunID: claim.Run.ID, Status: tickets.RunFailed}, e.MaxTurns, func(ticket tickets.Ticket, _ string) tickets.Transition {
			return tickets.Transition{State: waitingState(ticket), Message: message}
		})
	}
	branch := claim.Ticket.Branch()
	return &runner.RunRequest{
		RunID:         claim.Run.ID,
		TicketID:      claim.Ticket.ID,
		Repository:    claim.Ticket.Repo,
		Branch:        branch,
		Prompt:        renderPrompt(agent.Playbook, branch, tickets.RenderLog(claim.Entries)),
		AgentFiles:    agent.Files,
		Harness:       claim.Run.Harness,
		Model:         agent.Model,
		TimeoutMillis: e.RunTimeout.Milliseconds(),
		GitName:       e.Handle,
		GitEmail:      e.Handle + "@users.noreply.github.com",
	}, nil
}

const reportFormat = `End this run with one JSON object and nothing else:

{"outcome": "pr_ready|needs_input|no_change|failed", "summary": "...", "question": "..."}

- pr_ready: the work is pushed and a pull request is open for this branch.
- needs_input: you need a person to decide. Put what you need in question.
- no_change: the work needed no pull request. The summary is the answer.
- failed: you could not do the work. Say why in summary.`

func renderPrompt(playbook, branch, log string) string {
	return playbook + "\n\n# Branch\n\nYou are on branch `" + branch + "`. Push it often, because unpushed work is lost if this run stops.\n\n# Report\n\n" + reportFormat + "\n\n# Ticket log\n\n" + log
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
	return e.Store.CompleteRun(ctx, tickets.RunCompletion{RunID: runID, Status: completion.State, Report: report, Tokens: completion.Tokens, Cost: completion.Cost}, e.MaxTurns, func(ticket tickets.Ticket, status string) tickets.Transition {
		return decide(ticket, status, report, reason, pr)
	})
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

func decide(ticket tickets.Ticket, status string, report *protocol.Report, reason string, pr *PR) tickets.Transition {
	if status == tickets.RunInterrupted {
		return tickets.Transition{State: tickets.StateWorking}
	}
	transition := tickets.Transition{}
	if pr != nil {
		transition.PRNumber = pr.Number
	}
	if status != tickets.RunSucceeded || report == nil {
		if reason == "" {
			reason = status
		}
		transition.State = waitingState(ticket)
		transition.Message = "The run ended without a report: " + reason
		return transition
	}
	transition.Report = report.Outcome + ": " + report.Summary
	if report.Question != "" {
		transition.Report += "\nQuestion: " + report.Question
	}
	switch report.Outcome {
	case protocol.OutcomePRReady:
		if pr != nil {
			transition.State = tickets.StatePRReady
		} else {
			transition.State = waitingState(ticket)
			transition.Message = "Reported a PR but none exists for this branch."
		}
	case protocol.OutcomeNeedsInput:
		transition.State, transition.Message = waitingState(ticket), report.Question
	case protocol.OutcomeNoChange:
		transition.State, transition.Message = tickets.StateDone, report.Summary
	default:
		transition.State, transition.Message = waitingState(ticket), report.Summary
	}
	return transition
}
