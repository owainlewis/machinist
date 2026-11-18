package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/tickets"
)

func newIssueTicket(t *testing.T, e *Engine) tickets.Ticket {
	t.Helper()
	ticket, _, err := e.Store.CreateTicket(t.Context(), tickets.NewTicket{Source: "github", Repo: "owner/repo", IssueNumber: 3, Title: "Title", Body: "THE REQUEST", Agent: "default", CreatedBy: "alice", SourceKey: "github:comment:1"})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func withPR(e *Engine) {
	e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: 42}, nil }
}

// prReady runs a ticket to pr_ready.
func prReady(t *testing.T, e *Engine, ticket tickets.Ticket) {
	t.Helper()
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, succeed("pr_ready", "Opened")); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StatePRReady {
		t.Fatalf("state = %s", got)
	}
}

func messages(t *testing.T, e *Engine, id string) []string {
	t.Helper()
	entries, err := e.Store.Entries(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	bodies := []string{}
	for _, entry := range entries {
		if entry.Kind == tickets.EntryMessage {
			bodies = append(bodies, entry.Body)
		}
	}
	return bodies
}

func TestFirstPRPostsTheLinkOnce(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	ticket := newIssueTicket(t, e)
	prReady(t, e, ticket)
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Change it", SourceKey: "c2"}); err != nil {
		t.Fatal(err)
	}
	prReady(t, e, ticket)
	all := messages(t, e, ticket.ID)
	if len(all) != 2 || all[1] != "Opened a PR: https://github.com/owner/repo/pull/42" {
		t.Fatalf("messages = %q", all)
	}
}

func TestFirstPRWithNoIssuePostsNoLink(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	ticket := newTicket(t, e, "api")
	prReady(t, e, ticket)
	if all := messages(t, e, ticket.ID); len(all) != 0 {
		t.Fatalf("messages = %q", all)
	}
}

func TestInputDuringRunStartsTheNextRunAtOnce(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Also this", SourceKey: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Complete(t.Context(), request.RunID, succeed("pr_ready", "Opened")); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, ticket.ID); got != tickets.StateWorking {
		t.Fatalf("state = %s", got)
	}
	if next := assignRun(t, e); !strings.Contains(prompt(t, next), "Also this") {
		t.Fatalf("prompt = %s", prompt(t, next))
	}
}

func TestCancelFromEveryActiveStateStopsWorkAndKeepsThePR(t *testing.T) {
	for name, setup := range map[string]func(*testing.T, *Engine, tickets.Ticket) string{
		"working": func(t *testing.T, e *Engine, _ tickets.Ticket) string { return assignRun(t, e).RunID },
		"pr ready": func(t *testing.T, e *Engine, ticket tickets.Ticket) string {
			prReady(t, e, ticket)
			return ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEngine(t)
			withPR(e)
			ticket := newTicket(t, e, "api")
			running := setup(t, e, ticket)
			if _, err := e.Store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
				t.Fatal(err)
			}
			got, _ := e.Store.Ticket(t.Context(), ticket.ID)
			if got.State != tickets.StateCancelled || lastMessage(t, e, ticket.ID) != "Cancelled by alice" {
				t.Fatalf("ticket = %+v, message = %q", got, lastMessage(t, e, ticket.ID))
			}
			if running != "" {
				if cancel, err := e.Heartbeat(t.Context(), running); err != nil || !cancel {
					t.Fatalf("cancel = %v, err = %v", cancel, err)
				}
			}
			if _, err := e.Store.Cancel(t.Context(), ticket.ID, "alice"); err != tickets.ErrTicketEnded {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEndedTicketIgnoresEveryEvent(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	ticket := newTicket(t, e, "api")
	prReady(t, e, ticket)
	if _, err := e.Store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	before := len(messages(t, e, ticket.ID))
	if _, added, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Hello"}); err != nil || added {
		t.Fatalf("added = %v, err = %v", added, err)
	}
	got, _ := e.Store.Ticket(t.Context(), ticket.ID)
	if got.State != tickets.StateCancelled || got.MergedAt != nil || len(messages(t, e, ticket.ID)) != before {
		t.Fatalf("ticket = %+v", got)
	}
}
