// Package engine moves tickets through their states and hands runs to runners.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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
	Closed bool
}

// BotEmail is the commit author email of every agent run.
func BotEmail(handle string) string { return handle + "@users.noreply.github.com" }

// PRFinder finds the pull request for a branch, or nil if there is none.
type PRFinder func(ctx context.Context, repository, branch string) (*PR, error)

type Options struct {
	Store      *tickets.Store
	Factory    *factory.Factory
	MaxTurns   int
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
	turns, err := e.Store.TurnsSinceHuman(ctx, ticket.ID)
	if err != nil {
		return err
	}
	if turns >= e.MaxTurns {
		message, err := e.turnLimitMessage(ctx, ticket.ID)
		if err != nil {
			return err
		}
		return e.Store.Apply(ctx, ticket.ID, tickets.StateWorking, tickets.Transition{State: waitingState(ticket), Message: message})
	}
	job, ok := e.Factory.Job(ticket.Agent)
	if !ok {
		return e.Store.Apply(ctx, ticket.ID, tickets.StateWorking, tickets.Transition{State: waitingState(ticket), Message: missingAgentMessage(ticket.Agent)})
	}
	_, err = e.Store.QueueRun(ctx, ticket.ID, job.Harness)
	return err
}

// turnLimitMessage says what the last run reported and what it left unfixed.
func (e *Engine) turnLimitMessage(ctx context.Context, ticketID string) (string, error) {
	report, err := e.Store.LastReport(ctx, ticketID)
	if err != nil {
		return "", err
	}
	waitingCI, err := e.Store.WaitingCI(ctx, ticketID)
	if err != nil {
		return "", err
	}
	parts := []string{fmt.Sprintf("Reached the limit of %d runs without human input.", e.MaxTurns)}
	for _, part := range []string{report, waitingCI} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n\n"), nil
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
	job, ok := e.Factory.Job(claim.Ticket.Agent)
	if !ok {
		message := missingAgentMessage(claim.Ticket.Agent)
		return nil, e.Store.CompleteRun(ctx, tickets.RunCompletion{RunID: claim.Run.ID, Status: tickets.RunFailed}, e.MaxTurns, func(ticket tickets.Ticket, _ string) tickets.Transition {
			return tickets.Transition{State: waitingState(ticket), Message: message}
		})
	}
	request := &runner.RunRequest{
		RunID:         claim.Run.ID,
		TicketID:      claim.Ticket.ID,
		Repository:    claim.Ticket.Repo,
		Branch:        claim.Ticket.Branch(),
		Harness:       claim.Run.Harness,
		TimeoutMillis: e.RunTimeout.Milliseconds(),
		GitName:       e.Handle,
		GitEmail:      BotEmail(e.Handle),
	}
	if claim.Run.Kind == tickets.RunKindReviewer {
		reviewer, ok := e.reviewer(claim.Run.Reviewer)
		if !ok {
			// Its failure changes no ticket state, so nothing here decides a transition.
			return nil, e.Store.CompleteRun(ctx, tickets.RunCompletion{RunID: claim.Run.ID, Status: tickets.RunFailed}, e.MaxTurns, nil)
		}
		request.Prompt = renderReviewerPrompt(reviewer.Prompt, claim.Ticket.Repo, claim.Ticket.PRNumber)
		request.Model = e.reviewerModel(job)
		return request, nil
	}
	request.Stages = e.stages(job, request.Branch, tickets.RenderLog(claim.Entries))
	return request, nil
}

// stages turns a job into what the runner starts. Every agent stage gets the
// same ticket log, and sees the work of earlier stages only in the clone.
func (e *Engine) stages(job factory.Job, branch, log string) []runner.Stage {
	stages := make([]runner.Stage, 0, len(job.Stages))
	for _, stage := range job.Stages {
		if stage.Agent == "" {
			stages = append(stages, runner.Stage{Name: stage.Name, Kind: runner.StageScript, Script: stage.Script})
			continue
		}
		agent := e.Factory.Agents[stage.Agent]
		stages = append(stages, runner.Stage{Name: stage.Name, Kind: runner.StageAgent, Prompt: renderPrompt(agent.Playbook, branch, log), Model: agent.Model, AgentFiles: agent.Files})
	}
	return stages
}

// reviewerModel is the model of the job's first agent stage.
func (e *Engine) reviewerModel(job factory.Job) string {
	for _, stage := range job.Stages {
		if stage.Agent != "" {
			return e.Factory.Agents[stage.Agent].Model
		}
	}
	return ""
}

func (e *Engine) reviewer(name string) (factory.Reviewer, bool) {
	for _, reviewer := range e.Factory.Reviewers {
		if reviewer.Name == name {
			return reviewer, true
		}
	}
	return factory.Reviewer{}, false
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

// A reviewer's report is ignored, but the harness still needs one to finish.
func renderReviewerPrompt(prompt, repository string, number int) string {
	return fmt.Sprintf("%s\n\n# Pull request\n\nRepository `%s`, PR #%d.\n\n# Report\n\n%s\n\nReport no_change once your review is posted.", prompt, repository, number, reportFormat)
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
	return e.Store.CompleteRun(ctx, tickets.RunCompletion{RunID: runID, Status: completion.State, Report: report, Tokens: completion.Tokens, Cost: completion.Cost, Stages: completion.Stages}, e.MaxTurns, func(ticket tickets.Ticket, status string) tickets.Transition {
		transition := decide(ticket, status, report, reason, pr)
		if pr != nil && ticket.PRNumber == 0 {
			transition.FirstPR = e.firstPR(ticket, pr.Number)
		}
		return transition
	})
}

func (e *Engine) firstPR(ticket tickets.Ticket, number int) *tickets.FirstPR {
	job, _ := e.Factory.Job(ticket.Agent)
	first := &tickets.FirstPR{Harness: job.Harness}
	if ticket.IssueNumber != 0 {
		first.Link = fmt.Sprintf("Opened a PR: https://github.com/%s/pull/%d", ticket.Repo, number)
	}
	for _, reviewer := range e.Factory.Reviewers {
		first.Reviewers = append(first.Reviewers, reviewer.Name)
	}
	return first
}

// PRMerged ends a ticket whose PR merged.
func (e *Engine) PRMerged(ctx context.Context, ticket tickets.Ticket, humanCommits int, mergedAt time.Time) error {
	return e.Store.PRMerged(ctx, ticket.ID, humanCommits, mergedAt)
}

// PRClosed stops work on a ticket whose PR closed without merging.
func (e *Engine) PRClosed(ctx context.Context, ticket tickets.Ticket, key string) error {
	return e.Store.PRClosed(ctx, ticket.ID, key, waitingState(ticket), "The PR was closed. Reply to continue or cancel.")
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
		if pr != nil && !pr.Closed {
			transition.State = tickets.StatePRReady
		} else {
			transition.State = waitingState(ticket)
			transition.Message = "Reported a PR but none is open for this branch."
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
