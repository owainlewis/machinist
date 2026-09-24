package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

// processManagedTriggers admits due occurrences. One trigger's failure does not
// stop the others.
func (s *Server) processManagedTriggers(ctx context.Context) error {
	var failures []error
	for _, trigger := range s.triggers {
		if err := s.processManagedTrigger(ctx, trigger); err != nil {
			failures = append(failures, fmt.Errorf("trigger %q: %w", trigger.Identity, err))
		}
	}
	return errors.Join(failures...)
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
	return s.processFixedTrigger(ctx, trigger, status.ConfigGeneration, *status.NextDueAt, status.PendingOccurrenceAt, now)
}

func (s *Server) processFixedTrigger(ctx context.Context, trigger config.ResolvedTrigger, generation string, firstDue time.Time, pending *time.Time, now time.Time) error {
	if pending != nil {
		firstDue = *pending
	}
	occurrence, nextDue, coalesced, err := fixedOccurrenceWindow(trigger, firstDue, now, pending != nil)
	if err != nil {
		_ = s.store.RecordTriggerAttempt(ctx, trigger.Identity, generation, err)
		return err
	}
	if pending == nil {
		if err := s.store.SetTriggerPendingOccurrence(ctx, trigger.Identity, generation, occurrence); err != nil {
			_ = s.store.RecordTriggerAttempt(ctx, trigger.Identity, generation, err)
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
		return s.store.RecordTriggerAttempt(ctx, trigger.Identity, generation, nil)
	}
	if admissionErr == nil && coalesced > 0 {
		admissionErr = s.store.AddTriggerCoalesced(ctx, trigger.Identity, generation, coalesced)
	}
	recordErr := s.store.RecordTriggerAttempt(ctx, trigger.Identity, generation, admissionErr)
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
