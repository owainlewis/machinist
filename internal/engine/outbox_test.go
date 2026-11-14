package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
)

type post struct {
	repo   string
	number int
	body   string
}

func recordPosts(e *Engine) *[]post {
	posts := &[]post{}
	e.Post = func(_ context.Context, repo string, number int, body string) error {
		*posts = append(*posts, post{repo, number, body})
		return nil
	}
	return posts
}

func TestDeliverPostsOnceOnTheIssue(t *testing.T) {
	e := newEngine(t)
	posts := recordPosts(e)
	ticket := newIssueTicket(t, e)
	for range 2 {
		if err := e.Deliver(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(*posts) != 1 || (*posts)[0] != (post{"owner/repo", 3, "Working on this as ticket " + ticket.ID + "."}) {
		t.Fatalf("posts = %+v", *posts)
	}
	entries, _ := e.Store.Entries(t.Context(), ticket.ID)
	if entries[1].PostedAt == nil {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestDeliverUsesThePRWhenThereIsNoIssue(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	posts := recordPosts(e)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, succeed("needs_input", "Stuck", "Which?")); err != nil {
		t.Fatal(err)
	}
	if err := e.Deliver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(*posts) != 1 || (*posts)[0].number != 42 || (*posts)[0].body != "Which?" {
		t.Fatalf("posts = %+v", *posts)
	}
	if got, _ := e.Store.Ticket(t.Context(), ticket.ID); got.PRNumber != 42 {
		t.Fatalf("ticket = %+v", got)
	}
}

func TestDeliverKeepsMessagesWithNoDestinationInMachinist(t *testing.T) {
	e := newEngine(t)
	posts := recordPosts(e)
	ticket := newTicket(t, e, "api")
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, succeed("needs_input", "Stuck", "Which?")); err != nil {
		t.Fatal(err)
	}
	if message := lastMessage(t, e, ticket.ID); message != "Which?" {
		t.Fatalf("message = %q", message)
	}

	// The old question must not appear on a PR found later.
	if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "Use v2"}); err != nil {
		t.Fatal(err)
	}
	withPR(e)
	second := assignRun(t, e)
	if err := e.Complete(t.Context(), second.RunID, succeed("pr_ready", "Opened", "")); err != nil {
		t.Fatal(err)
	}
	if err := e.Deliver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(*posts) != 0 {
		t.Fatalf("posts = %+v", *posts)
	}
}

func TestDeliverContinuesPastAFailure(t *testing.T) {
	e := newEngine(t)
	first := newIssueTicket(t, e)
	second, _, err := e.Store.CreateTicket(t.Context(), tickets.NewTicket{Source: "github", Repo: "owner/repo", IssueNumber: 4, Title: "T", Body: "B", Agent: "default", CreatedBy: "alice", SourceKey: "github:comment:2"})
	if err != nil {
		t.Fatal(err)
	}
	posted := []int{}
	e.Post = func(_ context.Context, _ string, number int, _ string) error {
		if number == 3 {
			return errors.New("issue is locked")
		}
		posted = append(posted, number)
		return nil
	}
	if err := e.Deliver(t.Context()); err == nil || !strings.Contains(err.Error(), "issue is locked") {
		t.Fatalf("error = %v", err)
	}
	if len(posted) != 1 || posted[0] != 4 {
		t.Fatalf("posted = %v", posted)
	}
	failedEntries, _ := e.Store.Entries(t.Context(), first.ID)
	sentEntries, _ := e.Store.Entries(t.Context(), second.ID)
	if failedEntries[1].PostedAt != nil || sentEntries[1].PostedAt == nil {
		t.Fatalf("failed = %+v, sent = %+v", failedEntries[1], sentEntries[1])
	}
}

func TestDeliverBacksOffAfterAFailure(t *testing.T) {
	e := newEngine(t)
	newIssueTicket(t, e)
	attempts := 0
	e.Post = func(context.Context, string, int, string) error {
		attempts++
		return errors.New("down")
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e.now = func() time.Time { return now }
	e.deliverDue(t.Context())
	e.deliverDue(t.Context())
	if attempts != 1 {
		t.Fatalf("attempts = %d", attempts)
	}
	now = now.Add(deliverBackoff)
	e.deliverDue(t.Context())
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
}

func TestDeliverTruncatesLongMessages(t *testing.T) {
	e := newEngine(t)
	posts := recordPosts(e)
	ticket := newIssueTicket(t, e)
	if err := e.Store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateNeedsYou, Message: strings.Repeat("é", maxPostBytes)}); err != nil {
		t.Fatal(err)
	}
	if err := e.Deliver(t.Context()); err != nil {
		t.Fatal(err)
	}
	body := (*posts)[1].body
	if len(body) > maxPostBytes+20 || !strings.HasSuffix(body, "[truncated]") || !strings.HasPrefix(body, "é") {
		t.Fatalf("length = %d", len(body))
	}
}
