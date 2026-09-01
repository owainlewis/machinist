package controlplane

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

const queuedGitHubLabel = "machinist:queued"

type githubTriggerClient interface {
	SearchRequestedIssues(context.Context, []string, string, int) ([]GitHubCandidate, error)
	IssueDetails(context.Context, string, int, string) (GitHubIssueDetails, error)
	Permission(context.Context, string, string) (string, error)
	AcknowledgeRequest(context.Context, string, int, string, string, bool) error
}

func (s *Server) processManagedTrigger(ctx context.Context, trigger config.ResolvedTrigger) error {
	statuses, err := s.store.TriggerSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("read managed trigger state: %w", err)
	}
	var status TriggerStatus
	found := false
	for _, candidate := range statuses {
		if candidate.Identity == trigger.Identity {
			status = candidate
			found = true
			break
		}
	}
	if !found {
		return errors.New("has no durable state")
	}
	if status.ConfigSignature != trigger.Signature {
		return ErrTriggerStale
	}
	now := s.now().UTC()
	if status.NextDueAt == nil || status.NextDueAt.After(now) {
		return nil
	}
	if trigger.Family == "github" {
		return s.processGitHubTrigger(ctx, trigger, status.ConfigGeneration)
	}
	return s.processFixedTrigger(ctx, trigger, status.ConfigGeneration, *status.NextDueAt, status.PendingOccurrenceAt, now)
}

func (s *Server) processFixedTrigger(ctx context.Context, trigger config.ResolvedTrigger, generation string, firstDue time.Time, pending *time.Time, now time.Time) error {
	if pending != nil {
		firstDue = *pending
	}
	occurrence, nextDue, coalesced, err := fixedOccurrenceWindow(trigger, firstDue, now, pending != nil)
	if err != nil {
		_ = s.store.RecordTriggerAttempt(ctx, trigger.Identity, generation, 0, err)
		return err
	}
	if pending == nil {
		if err := s.store.SetTriggerPendingOccurrence(ctx, trigger.Identity, generation, occurrence); err != nil {
			_ = s.store.RecordTriggerAttempt(ctx, trigger.Identity, generation, 0, err)
			return err
		}
	}
	admission := TriggerAdmission{
		Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, ConfigGeneration: generation,
		OccurrenceKey: occurrence.UTC().Format(time.RFC3339Nano), ScheduledAt: occurrence, NextDueAt: nextDue,
		Prompt: trigger.Prompt, Repository: trigger.Repository,
		SelectionName: trigger.SelectionName, Command: trigger.Command,
	}
	_, _, admissionErr := s.store.CreateTriggeredJob(ctx, admission)
	if errors.Is(admissionErr, ErrTriggerPreviousGenerationActive) {
		return s.store.RecordTriggerAttempt(ctx, trigger.Identity, generation, 0, nil)
	}
	if admissionErr == nil && coalesced > 0 {
		admissionErr = s.store.AddTriggerCoalesced(ctx, trigger.Identity, generation, coalesced)
	}
	recordErr := s.store.RecordTriggerAttempt(ctx, trigger.Identity, generation, 0, admissionErr)
	return errors.Join(admissionErr, recordErr)
}

// fixedOccurrenceWindow coalesces backlog into the latest due occurrence and advances
// to the first future time. Intervals use arithmetic; cron schedules retain calendar
// and daylight-saving behavior by walking their resolved occurrences.
func fixedOccurrenceWindow(trigger config.ResolvedTrigger, firstDue, now time.Time, preserveFirst bool) (time.Time, time.Time, int64, error) {
	firstDue = firstDue.UTC()
	now = now.UTC()
	if firstDue.After(now) {
		return time.Time{}, firstDue, 0, errors.New("trigger is not due")
	}
	if trigger.Family == "interval" {
		if trigger.Every <= 0 {
			return time.Time{}, time.Time{}, 0, errors.New("interval duration is not positive")
		}
		steps := int64(now.Sub(firstDue)/trigger.Every) + 1
		occurrence := firstDue
		if !preserveFirst {
			occurrence = firstDue.Add(time.Duration(steps-1) * trigger.Every)
		}
		return occurrence, firstDue.Add(time.Duration(steps) * trigger.Every), steps - 1, nil
	}
	if trigger.Family != "cron" {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("unsupported fixed trigger family %q", trigger.Family)
	}
	occurrence := firstDue
	cursor := firstDue
	var skipped int64
	for {
		next := trigger.NextDue(cursor)
		if next.IsZero() {
			return time.Time{}, time.Time{}, 0, errors.New("cron schedule has no future occurrence")
		}
		if next.After(now) {
			return occurrence, next, skipped, nil
		}
		cursor = next
		if !preserveFirst {
			occurrence = next
		}
		skipped++
	}
}

func (s *Server) processGitHubTrigger(ctx context.Context, trigger config.ResolvedTrigger, generation string) error {
	repositories := slices.SortedFunc(maps.Values(trigger.GitHubRepositories), func(left, right string) int {
		return strings.Compare(strings.ToLower(left), strings.ToLower(right))
	})
	var failures []error
	reconciliations, err := s.store.GitHubTriggerReconciliations(ctx, trigger.Identity)
	if err != nil {
		failures = append(failures, err)
	} else {
		for _, request := range reconciliations {
			if err := s.reconcileGitHubRequest(ctx, trigger, generation, repositories, request); err != nil {
				failures = append(failures, fmt.Errorf("%s#%d reconciliation: %w", request.Repository, request.IssueNumber, err))
			}
		}
	}

	candidates, searchErr := s.github.SearchRequestedIssues(ctx, repositories, trigger.Label, maxGitHubCandidates)
	if searchErr != nil {
		failures = append(failures, searchErr)
	} else {
		for _, candidate := range candidates {
			if err := s.processGitHubCandidate(ctx, trigger, generation, repositories, candidate); err != nil {
				failures = append(failures, fmt.Errorf("%s#%d: %w", candidate.Repository, candidate.Number, err))
			}
		}
	}
	triggerErr := errors.Join(failures...)
	recordErr := s.store.RecordTriggerAttempt(ctx, trigger.Identity, generation, len(candidates), triggerErr)
	// GitHub polling uses non-overlapping fixed delay, measured after the poll ends.
	nextErr := s.store.SetTriggerNextDue(ctx, trigger.Identity, generation, s.now().UTC().Add(trigger.Every))
	return errors.Join(triggerErr, recordErr, nextErr)
}

func (s *Server) processGitHubCandidate(ctx context.Context, trigger config.ResolvedTrigger, generation string, repositories []string, candidate GitHubCandidate) error {
	details, err := s.github.IssueDetails(ctx, candidate.Repository, candidate.Number, trigger.Label)
	if err != nil {
		return err
	}
	if !GitHubIssueIsEligible(details, repositories) || !hasGitHubLabel(details.Labels, trigger.Label) {
		return nil
	}
	return s.processGitHubDetails(ctx, trigger, generation, repositories, trigger.Label, details)
}

func (s *Server) processGitHubDetails(ctx context.Context, trigger config.ResolvedTrigger, generation string, repositories []string, requestLabel string, details GitHubIssueDetails) error {
	if !GitHubIssueIsEligible(details, repositories) {
		return nil
	}
	issueURL := fmt.Sprintf("https://github.com/%s/issues/%d", details.Repository, details.Number)
	request := GitHubTriggerRequest{
		TriggerIdentity: trigger.Identity, OccurrenceKey: details.RequestedEvent.OccurrenceKey, ConfigGeneration: generation,
		Repository: details.Repository, IssueNumber: details.Number, Subject: issueURL,
		Actor: details.RequestedEvent.Actor, RequestLabel: requestLabel, RequestedAt: details.RequestedEvent.CreatedAt,
	}
	permission, err := s.github.Permission(ctx, details.Repository, details.RequestedEvent.Actor)
	if err != nil {
		return err
	}
	if !GitHubPermissionCanWrite(permission) {
		request.State = "rejected"
		if err := s.store.RejectGitHubTriggerRequest(ctx, request); err != nil {
			return err
		}
		return s.reconcileGitHubRequest(ctx, trigger, generation, repositories, request)
	}
	logicalRepository, ok := logicalGitHubRepository(trigger.GitHubRepositories, details.Repository)
	if !ok {
		return nil
	}
	prompt := "Complete " + issueURL
	command, renderErr := config.RenderPrompt(trigger.Command, prompt)
	if renderErr != nil {
		return renderErr
	}
	command.Model = trigger.Model
	occurrenceKey := details.RequestedEvent.OccurrenceKey
	_, created, err := s.store.CreateTriggeredJob(ctx, TriggerAdmission{
		Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, ConfigGeneration: generation, OccurrenceKey: occurrenceKey,
		Subject: issueURL, ScheduledAt: details.RequestedEvent.CreatedAt,
		Prompt: prompt, Repository: logicalRepository,
		SelectionName: trigger.SelectionName, Command: command,
		GitHubRepository: details.Repository, GitHubIssueNumber: details.Number, GitHubIssueTitle: details.Title, RequestActor: details.RequestedEvent.Actor, RequestLabel: requestLabel,
	})
	if err != nil {
		return err
	}
	committed := created
	if !committed {
		committed, err = s.store.TriggerOccurrenceExists(ctx, trigger.Identity, occurrenceKey)
		if err != nil {
			return err
		}
	}
	if !committed {
		// An earlier request for this issue is still active. Leave the intake
		// label in place so the same occurrence is retried after it completes.
		return nil
	}
	request.State = "admitted"
	return s.reconcileGitHubRequest(ctx, trigger, generation, repositories, request)
}

func (s *Server) reconcileGitHubRequest(ctx context.Context, trigger config.ResolvedTrigger, generation string, repositories []string, request GitHubTriggerRequest) error {
	requestLabel := request.RequestLabel
	if requestLabel == "" {
		requestLabel = trigger.Label
	}
	details, err := s.github.IssueDetails(ctx, request.Repository, request.IssueNumber, requestLabel)
	if err != nil {
		return err
	}
	if !strings.EqualFold(details.State, "open") || details.IsPullRequest {
		return s.store.CompleteGitHubTriggerReconciliation(ctx, request.TriggerIdentity, request.OccurrenceKey, request.ConfigGeneration)
	}
	_, repositoryConfigured := logicalGitHubRepository(trigger.GitHubRepositories, details.Repository)
	if !repositoryConfigured && request.State == "pending" {
		return s.store.CompleteGitHubTriggerReconciliation(ctx, request.TriggerIdentity, request.OccurrenceKey, request.ConfigGeneration)
	}
	if details.RequestedEvent == nil {
		return errors.New("github issue timeline has no request-label event")
	}
	if details.RequestedEvent.OccurrenceKey != request.OccurrenceKey {
		if !repositoryConfigured {
			return s.store.CompleteGitHubTriggerReconciliation(ctx, request.TriggerIdentity, request.OccurrenceKey, request.ConfigGeneration)
		}
		if err := s.processGitHubDetails(ctx, trigger, generation, repositories, requestLabel, details); err != nil {
			return err
		}
		return s.store.CompleteGitHubTriggerReconciliation(ctx, request.TriggerIdentity, request.OccurrenceKey, request.ConfigGeneration)
	}
	if request.State == "pending" {
		details.RequestedEvent.Actor = request.Actor
		details.RequestedEvent.CreatedAt = request.RequestedAt
		return s.processGitHubDetails(ctx, trigger, generation, repositories, requestLabel, details)
	}
	if !hasGitHubLabel(details.Labels, requestLabel) {
		return s.store.CompleteGitHubTriggerReconciliation(ctx, request.TriggerIdentity, request.OccurrenceKey, request.ConfigGeneration)
	}
	if err := s.github.AcknowledgeRequest(ctx, request.Repository, request.IssueNumber, requestLabel, queuedGitHubLabel, request.State == "admitted"); err != nil {
		return err
	}
	after, err := s.github.IssueDetails(ctx, request.Repository, request.IssueNumber, requestLabel)
	if err != nil {
		return err
	}
	if after.RequestedEvent != nil && after.RequestedEvent.OccurrenceKey != request.OccurrenceKey {
		if err := s.processGitHubDetails(ctx, trigger, generation, repositories, requestLabel, after); err != nil {
			return err
		}
	}
	if hasGitHubLabel(after.Labels, requestLabel) && (after.RequestedEvent == nil || after.RequestedEvent.OccurrenceKey == request.OccurrenceKey) {
		return errors.New("github request label remained after acknowledgement")
	}
	return s.store.CompleteGitHubTriggerReconciliation(ctx, request.TriggerIdentity, request.OccurrenceKey, request.ConfigGeneration)
}

func logicalGitHubRepository(repositories map[string]string, slug string) (string, bool) {
	for logical, configured := range repositories {
		if strings.EqualFold(configured, slug) {
			return logical, true
		}
	}
	return "", false
}

func hasGitHubLabel(labels []string, want string) bool {
	for _, label := range labels {
		if strings.EqualFold(label, want) {
			return true
		}
	}
	return false
}
