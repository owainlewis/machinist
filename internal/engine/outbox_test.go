package engine

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/ghclient"
	"github.com/owainlewis/machinist/internal/protocol"
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

func TestDeliverDoesNotPostToPRWhenThereIsNoIssue(t *testing.T) {
	e := newEngine(t)
	withPR(e)
	posts := recordPosts(e)
	ticket := newTicket(t, e, "run")
	request := assignRun(t, e)
	if err := e.Complete(t.Context(), request.RunID, protocol.Completion{State: tickets.RunFailed, Error: "exit 1"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Deliver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(*posts) != 0 {
		t.Fatalf("posts = %+v, want none for non-issue job", *posts)
	}
	if message := lastMessage(t, e, ticket.ID); message != "The run ended without a report: exit 1" {
		t.Fatalf("message = %q", message)
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
	if err := e.Complete(t.Context(), request.RunID, succeed("failed", "Stuck")); err != nil {
		t.Fatal(err)
	}
	if message := lastMessage(t, e, ticket.ID); message != "Stuck" {
		t.Fatalf("message = %q", message)
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
	if err := e.Store.Apply(t.Context(), ticket.ID, tickets.StateWorking, tickets.Transition{State: tickets.StateFailed, Message: strings.Repeat("é", maxPostBytes)}); err != nil {
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

func TestDeliverDropsPermanent4xxErrorsAfterLoggingOnce(t *testing.T) {
	for name, postErr := range map[string]error{
		"400 bad request": &ghclient.Error{Kind: ghclient.ErrorCommand, Operation: "post comment", Detail: "gh: Bad Request (HTTP 400)"},
		"403 forbidden":   &ghclient.Error{Kind: ghclient.ErrorCommand, Operation: "post comment", Detail: "gh: Issue is locked (HTTP 403)"},
		"404 not found":   &ghclient.Error{Kind: ghclient.ErrorCommand, Operation: "post comment", Detail: "gh: Not Found (HTTP 404)"},
		"410 gone":        &ghclient.Error{Kind: ghclient.ErrorCommand, Operation: "post comment", Detail: "gh: Gone (HTTP 410)"},
		"422 validation":  &ghclient.Error{Kind: ghclient.ErrorCommand, Operation: "post comment", Detail: "gh: Validation Failed (HTTP 422)"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEngine(t)
			var log bytes.Buffer
			e.Log = &log
			ticket := newIssueTicket(t, e)
			attempts := 0
			e.Post = func(context.Context, string, int, string) error {
				attempts++
				return postErr
			}
			if err := e.Deliver(t.Context()); err != nil {
				t.Fatalf("first Deliver error = %v, want nil for dropped permanent 4xx", err)
			}
			if err := e.Deliver(t.Context()); err != nil {
				t.Fatalf("second Deliver error = %v", err)
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d, want 1 (must not retry permanent 4xx)", attempts)
			}
			entries, _ := e.Store.Entries(t.Context(), ticket.ID)
			if entries[1].PostedAt == nil {
				t.Fatalf("entry was not marked handled: %+v", entries[1])
			}
			if !strings.Contains(log.String(), "owner/repo#3") || strings.Count(log.String(), "owner/repo#3") != 1 {
				t.Fatalf("log = %q, want one log line for owner/repo#3", log.String())
			}
		})
	}
}

func TestDeliverStillRetriesTransientAndRateLimitErrors(t *testing.T) {
	for name, postErr := range map[string]error{
		"429 too many requests": &ghclient.Error{Kind: ghclient.ErrorRateLimit, Operation: "post comment", Detail: "gh: Too Many Requests (HTTP 429)"},
		"403 rate limit":        &ghclient.Error{Kind: ghclient.ErrorRateLimit, Operation: "post comment", Detail: "gh: API rate limit exceeded (HTTP 403)"},
		"408 request timeout":   &ghclient.Error{Kind: ghclient.ErrorCommand, Operation: "post comment", Detail: "gh: Request Timeout (HTTP 408)"},
		"401 auth error":        &ghclient.Error{Kind: ghclient.ErrorAuth, Operation: "post comment", Detail: "gh: Bad credentials (HTTP 401)"},
		"502 bad gateway":       &ghclient.Error{Kind: ghclient.ErrorCommand, Operation: "post comment", Detail: "gh: Bad Gateway (HTTP 502)"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEngine(t)
			ticket := newIssueTicket(t, e)
			attempts := 0
			e.Post = func(context.Context, string, int, string) error {
				attempts++
				return postErr
			}
			if err := e.Deliver(t.Context()); err == nil {
				t.Fatalf("expected transient error to be returned for retry")
			}
			if err := e.Deliver(t.Context()); err == nil {
				t.Fatalf("expected second Deliver to retry")
			}
			if attempts != 2 {
				t.Fatalf("attempts = %d, want 2", attempts)
			}
			entries, _ := e.Store.Entries(t.Context(), ticket.ID)
			if entries[1].PostedAt != nil {
				t.Fatalf("transient failure was wrongly marked handled: %+v", entries[1])
			}
		})
	}
}
