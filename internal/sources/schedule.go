package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/tickets"
)

const (
	scheduleInterval = 30 * time.Second
	scheduleCursor   = "schedule"
	schedulerName    = "schedule"
)

// Schedule starts automation tickets when their cron schedules come due.
type Schedule struct {
	Store       *tickets.Store
	Automations []factory.Automation
	Log         io.Writer
	// Interval is the time between checks.
	Interval time.Duration
	now      func() time.Time
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

// Poll starts a ticket for each automation that is due. An automation whose
// last ticket is still active starts nothing, and stays due until it ends.
func (s *Schedule) Poll(ctx context.Context) error {
	var failures []error
	for _, automation := range s.Automations {
		if err := s.poll(ctx, automation); err != nil {
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
	_, _, err = s.create(ctx, automation, schedulerName, occurrence.UTC().Format(time.RFC3339))
	return err
}

func (s *Schedule) create(ctx context.Context, automation factory.Automation, by, occurrence string) (tickets.Ticket, bool, error) {
	return s.Store.CreateTicket(ctx, tickets.NewTicket{
		Source:    source(automation),
		Repo:      automation.Repo,
		Title:     automation.Name,
		Body:      automation.Prompt,
		Agent:     automation.Agent,
		CreatedBy: by,
		SourceKey: "schedule:" + automation.Name + ":" + occurrence,
	})
}

// RunNow starts an automation by hand. If its last ticket is still active, it
// returns that ticket. The bool reports whether this call created one.
func (s *Schedule) RunNow(ctx context.Context, name, by string) (tickets.Ticket, bool, error) {
	for _, automation := range s.Automations {
		if automation.Name == name {
			return s.create(ctx, automation, by, fmt.Sprintf("manual:%d", s.now().UnixNano()))
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
	Schedule   string      `json:"schedule"`
	Timezone   string      `json:"timezone"`
	NextDue    time.Time   `json:"next_due"`
	LastTicket *LastTicket `json:"last_ticket,omitempty"`
}

// Status lists each automation with its next due time and last ticket.
func (s *Schedule) Status(ctx context.Context) ([]AutomationStatus, error) {
	statuses := make([]AutomationStatus, 0, len(s.Automations))
	for _, automation := range s.Automations {
		status := AutomationStatus{Name: automation.Name, Repo: automation.Repo, Agent: automation.Agent, Schedule: automation.Cron.Expression(), Timezone: automation.Cron.Timezone()}
		base, found, err := s.base(ctx, automation)
		if err != nil {
			return nil, err
		}
		if !found {
			base = s.now()
		}
		status.NextDue = automation.Cron.Next(base)
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
