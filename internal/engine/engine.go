// Package engine moves tickets through their states and hands runs to runners.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/ship"
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
	// Closed means closed without merging.
	Closed bool
	Merged bool
	// ChangedAt is when it merged or closed, if GitHub said.
	ChangedAt time.Time
	// OpenedAt is when it was opened, if GitHub said.
	OpenedAt time.Time
}

// BotEmail is the commit author email of every agent run.
func BotEmail(handle string) string { return handle + "@users.noreply.github.com" }

// PRFinder finds the pull request for a branch, or nil if there is none.
type PRFinder func(ctx context.Context, repository, branch string) (*PR, error)

// PullRequests returns what watching one pull request needs.
type PullRequests func(repository string, number int) ship.GitHub

// ReviewReader returns the reviews people submitted on a pull request up to
// until (when non-zero), leaving out bots' and post-merge reviews.
type ReviewReader func(ctx context.Context, repository string, number int, until time.Time) (tickets.Reviews, error)

// TokenMinter returns a short-lived GitHub token for one repository.
type TokenMinter func(ctx context.Context, repository string) (string, error)

// IdentityFinder returns the name and email that runs commit as.
type IdentityFinder func(ctx context.Context) (name, email string, err error)

type Options struct {
	Store      *tickets.Store
	Factory    *factory.Factory
	RunTimeout time.Duration
	Handle     string
	// AutoMerge reaches every step as MACHINIST_AUTO_MERGE, true or false.
	// A pull request merges only when it is true, whatever a step says.
	AutoMerge bool
	FindPR    PRFinder
	// PullRequests reaches one pull request on GitHub, to wait for its checks
	// and merge it. Without it, a pull request a run hands over stays open.
	PullRequests PullRequests
	// Reviews, when set, reads who reviewed a pull request once it merges,
	// for the dashboard.
	Reviews ReviewReader
	Post    Poster
	// Notify, when set, tells someone when a job opens a pull request that
	// waits for a person, or fails.
	Notify Notifier
	// GitHubToken, when set, gives each run its own token for its repository.
	// Without it, a runner uses its own GH_TOKEN.
	GitHubToken TokenMinter
	// Identity, when set, is the git author of every run, in place of Handle.
	// With a GitHub App it is the app's bot account, so commits show as the
	// app's and not a person's.
	Identity IdentityFinder
	// PRInterval is the time between checks of open pull requests.
	PRInterval time.Duration
	Log        io.Writer
}

// runConfig is an immutable snapshot of the reloadable engine settings a run
// uses. Runs already queued before a reload keep the runConfig they started
// with.
type runConfig struct {
	factory     *factory.Factory
	runTimeout  time.Duration
	handle      string
	autoMerge   bool
	githubToken TokenMinter
	identity    IdentityFinder
}

type pinnedConfig struct {
	config    *runConfig
	updatedAt time.Time
}

type Engine struct {
	Options
	mu          sync.RWMutex
	pinned      map[string]pinnedConfig
	now         func() time.Time
	sleep       func(context.Context, time.Duration) bool
	deliverNext time.Time
}

func New(options Options) *Engine {
	if options.Log == nil {
		options.Log = io.Discard
	}
	return &Engine{Options: options, now: time.Now}
}

func (e *Engine) snapshotLocked() *runConfig {
	return &runConfig{
		factory:     e.Factory,
		runTimeout:  e.RunTimeout,
		handle:      e.Handle,
		autoMerge:   e.AutoMerge,
		githubToken: e.GitHubToken,
		identity:    e.Identity,
	}
}

func (e *Engine) configForTicket(ticketID string, updatedAt time.Time) *runConfig {
	e.mu.RLock()
	pin, ok := e.pinned[ticketID]
	if !ok {
		cfg := e.snapshotLocked()
		e.mu.RUnlock()
		return cfg
	}
	if pin.updatedAt.Equal(updatedAt) {
		e.mu.RUnlock()
		return pin.config
	}
	e.mu.RUnlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if current, ok := e.pinned[ticketID]; ok && !current.updatedAt.Equal(updatedAt) {
		delete(e.pinned, ticketID)
	}
	return e.snapshotLocked()
}

func (e *Engine) takeConfigForTicket(ticketID string, updatedAt time.Time) *runConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	if pin, ok := e.pinned[ticketID]; ok {
		delete(e.pinned, ticketID)
		if pin.updatedAt.Equal(updatedAt) {
			return pin.config
		}
	}
	return e.snapshotLocked()
}

// PinnedFactory returns the factory a ticket was pinned to during a reload,
// or nil if the ticket uses the current factory or its generation changed.
func (e *Engine) PinnedFactory(ticketID string, updatedAt time.Time) *factory.Factory {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if pin, ok := e.pinned[ticketID]; ok && pin.updatedAt.Equal(updatedAt) {
		return pin.config.factory
	}
	return nil
}

// UnpinThrough drops any reload pin for ticketID recorded at or before at.
func (e *Engine) UnpinThrough(ticketID string, at time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if pin, ok := e.pinned[ticketID]; ok && !pin.updatedAt.After(at) {
		delete(e.pinned, ticketID)
	}
}

// UnpinBefore drops any reload pin for ticketID recorded strictly before at.
func (e *Engine) UnpinBefore(ticketID string, at time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if pin, ok := e.pinned[ticketID]; ok && pin.updatedAt.Before(at) {
		delete(e.pinned, ticketID)
	}
}

func (e *Engine) autoMerge() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.AutoMerge
}

func (e *Engine) githubToken() TokenMinter {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.GitHubToken
}

// Reload swaps the engine's factory and reloadable config atomically while
// pinning every currently queued or awaiting-run ticket to the configuration
// it started with, tied to that ticket's current UpdatedAt generation.
func (e *Engine) Reload(queuedTickets []tickets.Ticket, loaded *factory.Factory, runTimeout time.Duration, handle string, autoMerge bool, tokens TokenMinter, identity IdentityFinder) {
	e.mu.Lock()
	defer e.mu.Unlock()
	previous := e.snapshotLocked()
	nextPinned := make(map[string]pinnedConfig, len(queuedTickets))
	for _, ticket := range queuedTickets {
		if existing, ok := e.pinned[ticket.ID]; ok && existing.updatedAt.Equal(ticket.UpdatedAt) {
			nextPinned[ticket.ID] = existing
		} else {
			nextPinned[ticket.ID] = pinnedConfig{config: previous, updatedAt: ticket.UpdatedAt}
		}
	}
	e.pinned = nextPinned
	e.Factory = loaded
	e.RunTimeout = runTimeout
	e.Handle = handle
	e.AutoMerge = autoMerge
	e.GitHubToken = tokens
	e.Identity = identity
}

// Run advances tickets and posts messages until ctx ends. Posting has its own
// loop, so a slow GitHub cannot delay leases or new runs.
func (e *Engine) Run(ctx context.Context) {
	delivered := make(chan struct{})
	go func() { e.runDeliveries(ctx); close(delivered) }()
	defer func() { <-delivered }()
	checked := make(chan struct{})
	go func() { e.runPRChecks(ctx); close(checked) }()
	defer func() { <-checked }()
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
		return e.apply(ctx, ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed, Message: message})
	}
	cfg := e.configForTicket(ticket.ID, ticket.UpdatedAt)
	if _, ok := cfg.factory.Preset(ticket.Agent); !ok {
		return e.apply(ctx, ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed, Message: missingPresetMessage(ticket.Agent)})
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
//
// A probed runner, one that said which harnesses it has, gets only jobs whose
// preset's agents it can run. The others wait for a runner that can.
func (e *Engine) Assign(ctx context.Context, runnerName string, harnesses []string, probed bool) (*runner.RunRequest, error) {
	var accept func(tickets.Ticket) bool
	if probed {
		e.mu.RLock()
		current := e.snapshotLocked()
		pinned := maps.Clone(e.pinned)
		e.mu.RUnlock()
		accept = func(ticket tickets.Ticket) bool {
			cfg := current
			if pin, ok := pinned[ticket.ID]; ok && pin.updatedAt.Equal(ticket.UpdatedAt) {
				cfg = pin.config
			}
			preset, ok := cfg.factory.Preset(ticket.Agent)
			// A job whose preset is gone is claimed so that it fails.
			return !ok || HasAll(harnesses, cfg.factory.Needs(preset))
		}
	}
	claim, err := e.Store.ClaimRun(ctx, runnerName, harnesses, e.now().Add(LeaseDuration), accept)
	if err != nil || claim == nil {
		return nil, err
	}
	cfg := e.takeConfigForTicket(claim.Ticket.ID, claim.QueuedUpdatedAt)
	preset, ok := cfg.factory.Preset(claim.Ticket.Agent)
	if !ok {
		message := missingPresetMessage(claim.Ticket.Agent)
		return nil, e.completeRun(ctx, tickets.RunCompletion{RunID: claim.Run.ID, Status: tickets.RunFailed}, func(tickets.Ticket, string) tickets.Transition {
			return tickets.Transition{State: tickets.StateFailed, Message: message}
		})
	}
	timeout := cfg.runTimeout
	if preset.Timeout > 0 {
		timeout = preset.Timeout
	}
	token := ""
	if cfg.githubToken != nil {
		token, err = cfg.githubToken(ctx, claim.Ticket.Repo)
		if err != nil {
			message := sentence(fmt.Sprintf("Machinist could not get a GitHub token from the GitHub App: %v", err))
			return nil, e.completeRun(ctx, tickets.RunCompletion{RunID: claim.Run.ID, Status: tickets.RunFailed}, func(tickets.Ticket, string) tickets.Transition {
				return tickets.Transition{State: tickets.StateFailed, Message: message}
			})
		}
	}
	gitName, gitEmail := cfg.handle, BotEmail(cfg.handle)
	if cfg.identity != nil {
		gitName, gitEmail, err = cfg.identity(ctx)
		if err != nil {
			message := sentence(fmt.Sprintf("Machinist could not look up the GitHub App's bot account: %v", err))
			return nil, e.completeRun(ctx, tickets.RunCompletion{RunID: claim.Run.ID, Status: tickets.RunFailed}, func(tickets.Ticket, string) tickets.Transition {
				return tickets.Transition{State: tickets.StateFailed, Message: message}
			})
		}
	}
	steps := make([]runner.Step, len(preset.Steps))
	for index, step := range preset.Steps {
		steps[index] = preset.EffectiveStep(step)
	}
	return &runner.RunRequest{
		RunID:         claim.Run.ID,
		TicketID:      claim.Ticket.ID,
		Preset:        preset.Name,
		Repository:    claim.Ticket.Repo,
		Branch:        claim.Ticket.Branch(),
		Vars:          map[string]string{"repo": claim.Ticket.Repo, "auto_merge": strconv.FormatBool(cfg.autoMerge)},
		Prompt:        tickets.RenderLog(claim.Entries),
		Files:         cfg.factory.Files,
		Steps:         steps,
		TimeoutMillis: timeout.Milliseconds(),
		GitName:       gitName,
		GitEmail:      gitEmail,
		GitHubToken:   token,
	}, nil
}

// ErrNoGitHubApp means the control plane has no GitHub App to make tokens with.
var ErrNoGitHubApp = errors.New("the control plane has no GitHub App, so runs use the runner's GH_TOKEN")

// RunToken makes a new token for an active run's repository, and nothing
// else, so a run can last longer than one token.
func (e *Engine) RunToken(ctx context.Context, runID string) (string, error) {
	githubToken := e.githubToken()
	if githubToken == nil {
		return "", ErrNoGitHubApp
	}
	run, err := e.Store.Run(ctx, runID)
	if err != nil {
		return "", err
	}
	if run.Status != tickets.RunRunning {
		return "", tickets.ErrRunNotActive
	}
	ticket, err := e.Store.Ticket(ctx, run.TicketID)
	if err != nil {
		return "", err
	}
	return githubToken(ctx, ticket.Repo)
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
	if len(completion.Patch) > protocol.MaxPatchBytes {
		return fmt.Errorf("%w: the patch is larger than %d bytes", ErrInvalidCompletion, protocol.MaxPatchBytes)
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
	var reviews *tickets.Reviews
	if pr != nil && pr.Merged {
		mergedAt := pr.ChangedAt
		if mergedAt.IsZero() {
			mergedAt = e.now()
		}
		if reviews, err = e.readReviews(ctx, runID, pr.Number, mergedAt); err != nil {
			return err
		}
	}
	runReport := report
	if completion.State == tickets.RunSucceeded {
		runReport = reached(report, pr)
	}
	return e.completeRun(ctx, tickets.RunCompletion{RunID: runID, Status: completion.State, Report: runReport, Tokens: completion.Tokens, Cost: completion.Cost, Stages: completion.Stages, Patch: completion.Patch}, func(ticket tickets.Ticket, status string) tickets.Transition {
		transition := decide(status, report, reason, pr)
		if transition.State == tickets.StateFailed && len(completion.Patch) > 0 {
			transition.Message = sentence(transition.Message) + " " + PatchSaved
		}
		if pr != nil && ticket.PRNumber == 0 {
			transition.FirstPR = e.firstPR(ticket, pr.Number)
		}
		if transition.State == tickets.StatePRReady && completion.Ship != nil {
			transition.Shipping = e.startWatch(*completion.Ship)
		}
		if completion.Ship != nil && (transition.State == tickets.StatePRReady || transition.State == tickets.StateDone) {
			transition.MergeGate = mergeGateOf(*completion.Ship)
		}
		if transition.MergedAt != nil {
			transition.Reviews = reviews
		}
		return transition
	})
}

func mergeGateOf(request protocol.Ship) *tickets.MergeGate {
	decision := strings.TrimSpace(request.MergeGateDecision)
	risk := strings.TrimSpace(request.MergeGateRisk)
	reason := strings.TrimSpace(request.MergeGateReason)
	if decision == "" && risk == "" && reason == "" {
		return nil
	}
	return &tickets.MergeGate{
		Decision: decision,
		Risk:     risk,
		Reason:   reason,
	}
}

func (e *Engine) apply(ctx context.Context, ticketID, from string, transition tickets.Transition) error {
	ticket, applied, err := e.Store.Apply(ctx, ticketID, from, transition)
	if err == nil && applied {
		e.notifyIfNeeded(ticket)
	}
	return err
}

func (e *Engine) completeRun(ctx context.Context, completion tickets.RunCompletion, decide func(tickets.Ticket, string) tickets.Transition) error {
	ticket, transitioned, err := e.Store.CompleteRun(ctx, completion, decide)
	if err == nil && transitioned {
		e.notifyIfNeeded(ticket)
	}
	return err
}

func (e *Engine) setShipping(ctx context.Context, listed tickets.Ticket, shipped tickets.Shipped) error {
	ticket, changed, err := e.Store.SetShipping(ctx, listed, shipped)
	if err == nil && changed {
		e.notifyIfNeeded(ticket)
	}
	return err
}

func (e *Engine) notifyIfNeeded(ticket tickets.Ticket) {
	if e.Notify == nil || !needsPerson(ticket) {
		return
	}
	e.Notify.Notify(ticket)
}

// PatchSaved is what a failed job whose change was saved as a patch tells the
// person, so they can recover the work without paying for the agent again.
const PatchSaved = "The change is saved as a patch: download it from the job page and apply it with git am."

// sentence ends text with a full stop, unless it has one, so another can follow.
func sentence(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasSuffix(text, ".") || strings.HasSuffix(text, "…") {
		return text
	}
	return text + "."
}

// reached labels a run's report with what its pull request reached when the
// run ended. A step that opens or merges a pull request reports no change,
// because only the control plane can see what became of it.
func reached(report *protocol.Report, pr *PR) *protocol.Report {
	if report == nil || report.Outcome != protocol.OutcomeNoChange || pr == nil {
		return report
	}
	labelled := *report
	switch {
	case pr.Merged:
		labelled.Outcome = protocol.OutcomeMerged
	case !pr.Closed:
		labelled.Outcome = protocol.OutcomePRReady
	}
	return &labelled
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

// readReviews reads the reviews of a run's merged pull request up to mergedAt.
// Nil means they were not read.
func (e *Engine) readReviews(ctx context.Context, runID string, number int, mergedAt time.Time) (*tickets.Reviews, error) {
	if e.Reviews == nil {
		return nil, nil
	}
	run, err := e.Store.Run(ctx, runID)
	if err != nil {
		return nil, err
	}
	ticket, err := e.Store.Ticket(ctx, run.TicketID)
	if err != nil {
		return nil, err
	}
	reviews, err := e.Reviews(ctx, ticket.Repo, number, mergedAt)
	if err != nil {
		return nil, err
	}
	return &reviews, nil
}

// decide is the state table for the end of a run. A job ends done, with a pull
// request or without one, or failed. It never waits for a person.
func decide(status string, report *protocol.Report, reason string, pr *PR) tickets.Transition {
	transition := tickets.Transition{}
	if pr != nil {
		transition.PRNumber = pr.Number
		if !pr.OpenedAt.IsZero() {
			opened := pr.OpenedAt
			transition.PROpenedAt = &opened
		}
	}
	if status == tickets.RunInterrupted {
		transition.State = tickets.StateWorking
		return transition
	}
	if status != tickets.RunSucceeded {
		transition.State = tickets.StateFailed
		if report != nil {
			transition.Report = report.Outcome + ": " + report.Summary
			transition.Message = report.Summary
			return transition
		}
		if reason == "" {
			reason = status
		}
		transition.Message = "The run ended without a report: " + reason
		return transition
	}
	if report == nil {
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
		// A step that opened a pull request and left it for a person reports
		// nothing more: the open pull request is what makes the job pr_ready.
		// The runner cannot tell what became of the pull request, so the
		// report is labelled with what the job reached.
		transition.State, transition.Message = tickets.StateDone, report.Summary
		if pr != nil && !pr.Closed && !pr.Merged {
			transition.State = tickets.StatePRReady
			transition.Report = protocol.OutcomePRReady + ": " + report.Summary
		}
		if pr != nil && pr.Merged {
			transition.Report = "merged: " + report.Summary
			at := pr.ChangedAt
			if at.IsZero() {
				at = time.Now()
			}
			transition.MergedAt = &at
		}
	default:
		transition.State, transition.Message = tickets.StateFailed, report.Summary
	}
	return transition
}

// HasAll says whether have holds every name in want.
func HasAll(have, want []string) bool {
	for _, name := range want {
		if !slices.Contains(have, name) {
			return false
		}
	}
	return true
}
