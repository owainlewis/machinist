package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

const queuedGitHubLabel = "machinist:queued"

type githubTriggerClient interface {
	SearchRequestedIssues(context.Context, []string, string, int) ([]GitHubCandidate, error)
	IssueDetails(context.Context, string, int, string) (GitHubIssueDetails, error)
	Permission(context.Context, string, string) (string, error)
	ReplaceRequestLabel(context.Context, string, int, string, string) error
}

func (s *Server) processManagedTriggers(ctx context.Context) error {
	if len(s.triggers) == 0 {
		return nil
	}
	statuses, err := s.store.TriggerSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("read managed trigger state: %w", err)
	}
	byIdentity := make(map[string]TriggerStatus, len(statuses))
	for _, status := range statuses {
		byIdentity[status.Identity] = status
	}
	now := s.now().UTC()
	var failures []error
	for _, trigger := range s.triggers {
		status, ok := byIdentity[trigger.Identity]
		if !ok {
			failures = append(failures, fmt.Errorf("trigger %q has no durable state", trigger.Identity))
			continue
		}
		if status.NextDueAt == nil || status.NextDueAt.After(now) {
			continue
		}
		var triggerErr error
		if trigger.Family == "github" {
			triggerErr = s.processGitHubTrigger(ctx, trigger)
		} else {
			triggerErr = s.processFixedTrigger(ctx, trigger, *status.NextDueAt, status.PendingOccurrenceAt, now)
		}
		if triggerErr != nil {
			failures = append(failures, fmt.Errorf("trigger %q: %w", trigger.Identity, triggerErr))
		}
	}
	return errors.Join(failures...)
}

func (s *Server) processFixedTrigger(ctx context.Context, trigger config.ResolvedTrigger, firstDue time.Time, pending *time.Time, now time.Time) error {
	if pending != nil {
		firstDue = *pending
	}
	occurrence, nextDue, coalesced, err := fixedOccurrenceWindow(trigger, firstDue, now, pending != nil)
	if err != nil {
		_ = s.store.RecordTriggerAttempt(ctx, trigger.Identity, 0, err)
		return err
	}
	if pending == nil {
		if err := s.store.SetTriggerPendingOccurrence(ctx, trigger.Identity, occurrence); err != nil {
			_ = s.store.RecordTriggerAttempt(ctx, trigger.Identity, 0, err)
			return err
		}
	}
	admission := TriggerAdmission{
		Identity: trigger.Identity, Family: trigger.Family,
		OccurrenceKey: occurrence.UTC().Format(time.RFC3339Nano), ScheduledAt: occurrence, NextDueAt: nextDue,
		Prompt: trigger.Prompt, Repository: trigger.Repository,
		SelectionKind: trigger.SelectionKind, SelectionName: trigger.SelectionName, Agents: trigger.Agents,
	}
	_, _, admissionErr := s.store.CreateTriggeredJob(ctx, admission)
	if admissionErr == nil && coalesced > 0 {
		admissionErr = s.store.AddTriggerCoalesced(ctx, trigger.Identity, coalesced)
	}
	recordErr := s.store.RecordTriggerAttempt(ctx, trigger.Identity, 0, admissionErr)
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

func (s *Server) processGitHubTrigger(ctx context.Context, trigger config.ResolvedTrigger) error {
	repositories := make([]string, 0, len(trigger.GitHubRepositories))
	for _, slug := range trigger.GitHubRepositories {
		repositories = append(repositories, slug)
	}
	sort.Slice(repositories, func(i, j int) bool { return strings.ToLower(repositories[i]) < strings.ToLower(repositories[j]) })
	candidates, searchErr := s.github.SearchRequestedIssues(ctx, repositories, trigger.Label, maxGitHubCandidates)
	if searchErr != nil {
		recordErr := s.store.RecordTriggerAttempt(ctx, trigger.Identity, 0, searchErr)
		nextErr := s.store.SetTriggerNextDue(ctx, trigger.Identity, s.now().UTC().Add(trigger.Every))
		return errors.Join(searchErr, recordErr, nextErr)
	}

	var failures []error
	for _, candidate := range candidates {
		if err := s.processGitHubCandidate(ctx, trigger, repositories, candidate); err != nil {
			failures = append(failures, fmt.Errorf("%s#%d: %w", candidate.Repository, candidate.Number, err))
		}
	}
	triggerErr := errors.Join(failures...)
	recordErr := s.store.RecordTriggerAttempt(ctx, trigger.Identity, len(candidates), triggerErr)
	// GitHub polling uses non-overlapping fixed delay, measured after the poll ends.
	nextErr := s.store.SetTriggerNextDue(ctx, trigger.Identity, s.now().UTC().Add(trigger.Every))
	return errors.Join(triggerErr, recordErr, nextErr)
}

func (s *Server) processGitHubCandidate(ctx context.Context, trigger config.ResolvedTrigger, repositories []string, candidate GitHubCandidate) error {
	details, err := s.github.IssueDetails(ctx, candidate.Repository, candidate.Number, trigger.Label)
	if err != nil {
		return err
	}
	if !GitHubIssueIsEligible(details, repositories) || !hasGitHubLabel(details.Labels, trigger.Label) {
		return nil
	}
	permission, err := s.github.Permission(ctx, details.Repository, details.RequestedEvent.Actor)
	if err != nil {
		return err
	}
	if !GitHubPermissionCanWrite(permission) {
		return nil
	}
	logicalRepository, ok := logicalGitHubRepository(trigger.GitHubRepositories, details.Repository)
	if !ok {
		return nil
	}
	issueURL := fmt.Sprintf("https://github.com/%s/issues/%d", details.Repository, details.Number)
	prompt := "Complete " + issueURL
	agents := make([]config.ResolvedAgent, len(trigger.Agents))
	for index, agent := range trigger.Agents {
		rendered, renderErr := config.RenderPrompt(agent, prompt)
		if renderErr != nil {
			return renderErr
		}
		rendered.Model = trigger.Model
		agents[index] = rendered
	}
	occurrenceKey := details.RequestedEvent.OccurrenceKey
	_, created, err := s.store.CreateTriggeredJob(ctx, TriggerAdmission{
		Identity: trigger.Identity, Family: trigger.Family, OccurrenceKey: occurrenceKey,
		Subject: issueURL, ScheduledAt: details.RequestedEvent.CreatedAt,
		Prompt: prompt, Repository: logicalRepository,
		SelectionKind: trigger.SelectionKind, SelectionName: trigger.SelectionName, Agents: agents,
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
	return s.github.ReplaceRequestLabel(ctx, details.Repository, details.Number, trigger.Label, queuedGitHubLabel)
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
