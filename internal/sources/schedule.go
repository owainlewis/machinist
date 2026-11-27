package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/owainlewis/machinist/internal/triggers"
)

const (
	scheduleInterval = 30 * time.Second
	scheduleCursor   = "schedule"
	schedulerName    = "schedule"
)

// RunOverrides holds optional per-run overrides for Schedule.RunNow.
// Empty fields fall back to the automation's saved configuration unless the
// corresponding Override* flag is set to explicitly clear the saved value.
type RunOverrides struct {
	Prompt          string
	Repo            string
	Preset          string
	Harness         string
	OverrideHarness bool
	Model           string
	OverrideModel   bool
}

// Schedule starts automation tickets when their cron schedules come due.
type Schedule struct {
	Store       *tickets.Store
	Automations []factory.Automation
	Log         io.Writer
	// Interval is the time between checks.
	Interval time.Duration
	now      func() time.Time
	mu       sync.RWMutex
}

// SetAutomations replaces the file-based automations atomically.
func (s *Schedule) SetAutomations(automations []factory.Automation) {
	s.mu.Lock()
	s.Automations = slices.Clone(automations)
	s.mu.Unlock()
}

func (s *Schedule) fileAutomations() []factory.Automation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.Automations)
}

// HasFileAutomation reports whether name belongs to a file-based automation in
// factory/automations/*.md.
func (s *Schedule) HasFileAutomation(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.Automations {
		if a.Name == name {
			return true
		}
	}
	return false
}

type resolvedAutomation struct {
	factory.Automation
	editable bool
}

func (s *Schedule) allAutomations(ctx context.Context) ([]resolvedAutomation, error) {
	fileList := s.fileAutomations()
	seen := make(map[string]bool, len(fileList))
	all := make([]resolvedAutomation, 0, len(fileList))
	for _, a := range fileList {
		seen[a.Name] = true
		all = append(all, resolvedAutomation{Automation: a, editable: false})
	}
	if s.Store != nil {
		stored, err := s.Store.Automations(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range stored {
			if seen[a.Name] {
				continue
			}
			var cron *triggers.Cron
			if strings.TrimSpace(a.Schedule) != "" {
				parsed, err := triggers.ParseCron(a.Schedule, a.Timezone)
				if err != nil {
					fmt.Fprintf(s.Log, "machinist: schedule: %s: %v\n", a.Name, err)
				} else {
					cron = parsed
				}
			}
			all = append(all, resolvedAutomation{
				Automation: factory.Automation{
					Name:    a.Name,
					Repo:    a.Repo,
					Preset:  a.Preset,
					Harness: a.Harness,
					Model:   a.Model,
					Prompt:  a.Prompt,
					Cron:    cron,
				},
				editable: true,
			})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

func NewSchedule(store *tickets.Store, automations []factory.Automation, log io.Writer) *Schedule {
	if log == nil {
		log = io.Discard
	}
	return &Schedule{Store: store, Automations: automations, Log: log, Interval: scheduleInterval, now: time.Now}
}

// Run checks at once, then every interval until ctx ends.
func (s *Schedule) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		if err := s.Poll(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(s.Log, "machinist: schedule: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func source(automation factory.Automation) string { return "schedule:" + automation.Name }

// base is the time after which the next occurrence falls: the newest ticket's
// start, or when Machinist first saw the file. Because a ticket moves it
// forward, missed occurrences collapse into one.
func (s *Schedule) base(ctx context.Context, automation factory.Automation) (time.Time, bool, error) {
	latest, err := s.Store.LatestTicketBySource(ctx, source(automation))
	if err == nil {
		return latest.CreatedAt, true, nil
	}
	if !errors.Is(err, tickets.ErrNotFound) {
		return time.Time{}, false, err
	}
	return s.Store.Cursor(ctx, scheduleCursor, automation.Name)
}

// Poll starts a ticket for each scheduled automation that is due. Manual-only
// automations (Cron == nil) are skipped. An automation whose last ticket is
// still active starts nothing, and stays due until it ends.
func (s *Schedule) Poll(ctx context.Context) error {
	all, err := s.allAutomations(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, automation := range all {
		if automation.Cron == nil {
			continue
		}
		if err := s.poll(ctx, automation.Automation); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", automation.Name, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Schedule) poll(ctx context.Context, automation factory.Automation) error {
	base, found, err := s.base(ctx, automation)
	if err != nil {
		return err
	}
	if !found {
		return s.Store.SetCursor(ctx, scheduleCursor, automation.Name, s.now())
	}
	occurrence := automation.Cron.Next(base)
	if occurrence.After(s.now()) {
		return nil
	}
	_, _, err = s.create(ctx, automation, schedulerName, occurrence.UTC().Format(time.RFC3339), RunOverrides{})
	return err
}

func (s *Schedule) create(ctx context.Context, automation factory.Automation, by, occurrence string, overrides RunOverrides) (tickets.Ticket, bool, error) {
	prompt := overrides.Prompt
	if prompt == "" {
		prompt = automation.Prompt
	}
	repo := overrides.Repo
	if repo == "" {
		repo = automation.Repo
	}
	preset := overrides.Preset
	if preset == "" {
		preset = automation.Preset
	}
	baseHarness, baseModel := automation.Harness, automation.Model
	if overrides.OverrideHarness {
		baseHarness = overrides.Harness
		if !overrides.OverrideModel && overrides.Harness != automation.Harness {
			baseModel = ""
		}
	}
	if overrides.OverrideModel {
		baseModel = overrides.Model
	}
	harnessName, model := harness.MergeOverride(baseHarness, baseModel, overrides.Harness, overrides.Model)
	return s.Store.CreateTicket(ctx, tickets.NewTicket{
		Source:    source(automation),
		Origin:    tickets.OriginSchedule,
		Repo:      repo,
		Title:     automation.Name,
		Body:      prompt,
		Agent:     preset,
		Harness:   harnessName,
		Model:     model,
		CreatedBy: by,
		SourceKey: "schedule:" + automation.Name + ":" + occurrence,
	})
}

// RunNow starts an automation by hand. Non-empty fields in overrides apply to
// this run only. If its last ticket is still active, it returns that ticket.
// The bool reports whether this call created one.
func (s *Schedule) RunNow(ctx context.Context, name, by string, overrides RunOverrides) (tickets.Ticket, bool, error) {
	all, err := s.allAutomations(ctx)
	if err != nil {
		return tickets.Ticket{}, false, err
	}
	for _, automation := range all {
		if automation.Name == name {
			return s.create(ctx, automation.Automation, by, fmt.Sprintf("manual:%d", s.now().UnixNano()), overrides)
		}
	}
	return tickets.Ticket{}, false, tickets.ErrNotFound
}

// LastTicket is an automation's newest ticket and what Machinist last said on it.
type LastTicket struct {
	tickets.Ticket
	Outcome string `json:"outcome,omitempty"`
}

// AutomationStatus is one automation as the UI shows it.
type AutomationStatus struct {
	Name       string      `json:"name"`
	Repo       string      `json:"repo"`
	Agent      string      `json:"agent"`
	Harness    string      `json:"harness,omitempty"`
	Model      string      `json:"model,omitempty"`
	Schedule   string      `json:"schedule"`
	Timezone   string      `json:"timezone"`
	NextDue    *time.Time  `json:"next_due,omitempty"`
	Prompt     string      `json:"prompt"`
	Editable   bool        `json:"editable"`
	LastTicket *LastTicket `json:"last_ticket,omitempty"`
}

// Status lists each automation with its next due time and last ticket.
func (s *Schedule) Status(ctx context.Context) ([]AutomationStatus, error) {
	all, err := s.allAutomations(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]AutomationStatus, 0, len(all))
	for _, item := range all {
		automation := item.Automation
		status := AutomationStatus{
			Name:     automation.Name,
			Repo:     automation.Repo,
			Agent:    automation.Preset,
			Harness:  automation.Harness,
			Model:    automation.Model,
			Prompt:   automation.Prompt,
			Editable: item.editable,
		}
		if automation.Cron != nil {
			status.Schedule = automation.Cron.Expression()
			status.Timezone = automation.Cron.Timezone()
			base, found, err := s.base(ctx, automation)
			if err != nil {
				return nil, err
			}
			if !found {
				base = s.now()
			}
			next := automation.Cron.Next(base)
			status.NextDue = &next
		}
		latest, err := s.Store.LatestTicketBySource(ctx, source(automation))
		if err == nil {
			outcome, err := s.Store.LatestMessage(ctx, latest.ID)
			if err != nil {
				return nil, err
			}
			status.LastTicket = &LastTicket{Ticket: latest, Outcome: outcome}
		} else if !errors.Is(err, tickets.ErrNotFound) {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}
