package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/ship"
	"github.com/owainlewis/machinist/internal/tickets"
)

type recordedPost struct {
	ContentType string
	Raw         string
	Payload     Notification
	Fields      map[string]any
}

type webhookSpy struct {
	server *httptest.Server
	mu     sync.Mutex
	posts  []recordedPost
	status int
}

func newWebhookSpy(t *testing.T, status int) *webhookSpy {
	t.Helper()
	spy := &webhookSpy{status: status}
	spy.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload Notification
		var fields map[string]any
		_ = json.Unmarshal(body, &payload)
		_ = json.Unmarshal(body, &fields)
		spy.mu.Lock()
		spy.posts = append(spy.posts, recordedPost{
			ContentType: r.Header.Get("Content-Type"),
			Raw:         string(body),
			Payload:     payload,
			Fields:      fields,
		})
		code := spy.status
		spy.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(spy.server.Close)
	return spy
}

func (s *webhookSpy) snapshot() []recordedPost {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedPost, len(s.posts))
	copy(out, s.posts)
	return out
}

func newPrivateTicket(t *testing.T, e *Engine, title string) tickets.Ticket {
	t.Helper()
	ticket, _, err := e.Store.CreateTicket(t.Context(), tickets.NewTicket{
		Source:    "api",
		Repo:      "owner/repo",
		Title:     title,
		Body:      "SECRET_PROMPT_QUESTION_TEXT private_code_snippet()",
		Agent:     "default",
		CreatedBy: "alice",
		SourceKey: "key:" + t.Name() + ":" + title,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestWebhookNotifiesOnPRReadyAndFailedWithoutPrivateOutput(t *testing.T) {
	tests := []struct {
		name       string
		pr         *PR
		completion protocol.Completion
		wantState  string
		wantAction string
	}{
		{
			name:       "pr_ready waiting for a person",
			pr:         &PR{Number: 42},
			completion: succeed(protocol.OutcomePRReady, "PRIVATE_AGENT_OUTPUT_SECRET"),
			wantState:  tickets.StatePRReady,
			wantAction: "PR ready",
		},
		{
			name:       "no_change with open PR waiting for a person",
			pr:         &PR{Number: 42},
			completion: succeed(protocol.OutcomeNoChange, "PRIVATE_AGENT_OUTPUT_SECRET"),
			wantState:  tickets.StatePRReady,
			wantAction: "PR ready",
		},
		{
			name:       "failed run",
			pr:         nil,
			completion: protocol.Completion{State: tickets.RunFailed, Error: "PRIVATE_STDERR_SECRET", Report: &protocol.Report{Outcome: protocol.OutcomeFailed, Summary: "PRIVATE_AGENT_OUTPUT_SECRET"}},
			wantState:  tickets.StateFailed,
			wantAction: "failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spy := newWebhookSpy(t, http.StatusOK)
			notifier := NewWebhookNotifier(spy.server.URL, "https://machinist.example.com/", io.Discard)
			e := newEngine(t)
			e.Notify = notifier
			e.FindPR = func(context.Context, string, string) (*PR, error) { return tc.pr, nil }

			ticket := newPrivateTicket(t, e, "Fix flaky test")
			run := assignRun(t, e)
			if err := e.Complete(t.Context(), run.RunID, tc.completion); err != nil {
				t.Fatal(err)
			}
			notifier.Wait()

			posts := spy.snapshot()
			if len(posts) != 1 {
				t.Fatalf("posts = %d, want 1", len(posts))
			}
			post := posts[0]
			if !strings.HasPrefix(post.ContentType, "application/json") {
				t.Fatalf("Content-Type = %q", post.ContentType)
			}
			wantURL := "https://machinist.example.com/#/jobs/" + ticket.ID
			got := post.Payload
			if got.TicketID != ticket.ID || got.Title != "Fix flaky test" || got.Repo != "owner/repo" || got.State != tc.wantState || got.CreatedBy != "alice" || got.URL != wantURL {
				t.Fatalf("payload = %+v", got)
			}
			for _, want := range []string{"owner/repo", tc.wantAction, "Fix flaky test", ticket.ID, "alice", wantURL} {
				if !strings.Contains(got.Text, want) {
					t.Fatalf("text = %q, missing %q", got.Text, want)
				}
			}
			for _, secret := range []string{"SECRET_PROMPT_QUESTION_TEXT", "private_code_snippet", "PRIVATE_AGENT_OUTPUT_SECRET", "PRIVATE_STDERR_SECRET"} {
				if strings.Contains(post.Raw, secret) {
					t.Fatalf("webhook payload leaked private text %q: %s", secret, post.Raw)
				}
			}
		})
	}
}

func TestWebhookOmitsLinkWithoutPublicURL(t *testing.T) {
	spy := newWebhookSpy(t, http.StatusOK)
	notifier := NewWebhookNotifier(spy.server.URL, "", io.Discard)
	e := newEngine(t)
	e.Notify = notifier
	e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: 42}, nil }

	ticket := newPrivateTicket(t, e, "Add webhook")
	run := assignRun(t, e)
	if err := e.Complete(t.Context(), run.RunID, succeed(protocol.OutcomePRReady, "Opened")); err != nil {
		t.Fatal(err)
	}
	notifier.Wait()

	posts := spy.snapshot()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	if _, hasURL := posts[0].Fields["url"]; hasURL {
		t.Fatalf("expected url field to be omitted without public_url, got %s", posts[0].Raw)
	}
	if strings.Contains(posts[0].Payload.Text, "http://") || strings.Contains(posts[0].Payload.Text, "https://") || strings.Contains(posts[0].Payload.Text, "#/jobs/") {
		t.Fatalf("expected no link in text without public_url, got %q", posts[0].Payload.Text)
	}
	if posts[0].Payload.TicketID != ticket.ID || posts[0].Payload.State != tickets.StatePRReady {
		t.Fatalf("payload = %+v", posts[0].Payload)
	}
}

func TestWebhookDoesNotNotifyForNonWaitingStates(t *testing.T) {
	t.Run("done without PR", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "https://machinist.example.com", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		newPrivateTicket(t, e, "Nothing to change")
		run := assignRun(t, e)
		if err := e.Complete(t.Context(), run.RunID, succeed(protocol.OutcomeNoChange, "Nothing to do")); err != nil {
			t.Fatal(err)
		}
		notifier.Wait()
		if posts := spy.snapshot(); len(posts) != 0 {
			t.Fatalf("expected no notification for done ticket, got %+v", posts)
		}
	})

	t.Run("done with already-merged PR", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "https://machinist.example.com", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: 42, Merged: true}, nil }
		newPrivateTicket(t, e, "Merged directly")
		run := assignRun(t, e)
		if err := e.Complete(t.Context(), run.RunID, succeed(protocol.OutcomeNoChange, "Merged")); err != nil {
			t.Fatal(err)
		}
		notifier.Wait()
		if posts := spy.snapshot(); len(posts) != 0 {
			t.Fatalf("expected no notification for merged ticket, got %+v", posts)
		}
	})

	t.Run("working after runner interruption", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "https://machinist.example.com", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		newPrivateTicket(t, e, "Interrupted")
		run := assignRun(t, e)
		if err := e.Complete(t.Context(), run.RunID, protocol.Completion{State: tickets.RunCancelled}); err != nil {
			t.Fatal(err)
		}
		notifier.Wait()
		if posts := spy.snapshot(); len(posts) != 0 {
			t.Fatalf("expected no notification for interrupted run, got %+v", posts)
		}
	})

	t.Run("cancelled ticket", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "https://machinist.example.com", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		ticket := newPrivateTicket(t, e, "Cancelled")
		run := assignRun(t, e)
		if _, err := e.Store.Cancel(t.Context(), ticket.ID, "alice"); err != nil {
			t.Fatal(err)
		}
		if err := e.Complete(t.Context(), run.RunID, protocol.Completion{State: tickets.RunFailed, Error: "stopped"}); err != nil {
			t.Fatal(err)
		}
		notifier.Wait()
		if posts := spy.snapshot(); len(posts) != 0 {
			t.Fatalf("expected no notification for cancelled ticket, got %+v", posts)
		}
	})

	t.Run("pr_ready flipped back to working by new input", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "https://machinist.example.com", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: 42}, nil }
		ticket := newPrivateTicket(t, e, "Follow-up arrived")
		run := assignRun(t, e)
		if _, _, err := e.Store.AddInput(t.Context(), ticket.ID, tickets.Input{Kind: tickets.EntryComment, Author: "alice", Body: "One more thing"}); err != nil {
			t.Fatal(err)
		}
		if err := e.Complete(t.Context(), run.RunID, succeed(protocol.OutcomePRReady, "Opened")); err != nil {
			t.Fatal(err)
		}
		notifier.Wait()
		if posts := spy.snapshot(); len(posts) != 0 {
			t.Fatalf("expected no notification when new input returned ticket to working, got %+v", posts)
		}
	})
}

func TestWebhookForHandedOverPullRequest(t *testing.T) {
	t.Run("auto-merged PR never notifies", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "https://machinist.example.com", io.Discard)
		e, pull, _ := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
		e.Notify = notifier

		// Pending check: no notification.
		checkPRs(t, e)
		notifier.Wait()
		if posts := spy.snapshot(); len(posts) != 0 {
			t.Fatalf("notified while check pending: %+v", posts)
		}

		// Passing check merges automatically: no notification.
		pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
		checkPRs(t, e)
		notifier.Wait()
		if posts := spy.snapshot(); len(posts) != 0 {
			t.Fatalf("notified after auto-merge: %+v", posts)
		}
	})

	t.Run("left open for a person notifies once as pr_ready", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "https://machinist.example.com", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		e.AutoMerge = false
		withPR(e)
		pull := &fakePull{
			pr:     ship.PullRequest{Number: 42, URL: "https://github.com/owner/repo/pull/42", State: "OPEN", MergeStateStatus: "CLEAN", HeadRefOid: "sha1"},
			checks: []ship.Check{{Name: "test", Bucket: "pending"}},
		}
		e.PullRequests = func(string, int) ship.GitHub { return pull }
		ticket := newPrivateTicket(t, e, "Review me")
		run := assignRun(t, e)
		completion := succeed(protocol.OutcomeNoChange, "Waiting on checks")
		completion.Ship = &protocol.Ship{Verdict: ship.Accepted}
		if err := e.Complete(t.Context(), run.RunID, completion); err != nil {
			t.Fatal(err)
		}
		notifier.Wait()
		if posts := spy.snapshot(); len(posts) != 0 {
			t.Fatalf("notified before checks finished: %+v", posts)
		}

		pull.checks = []ship.Check{{Name: "test", Bucket: "pass"}}
		checkPRs(t, e)
		notifier.Wait()
		posts := spy.snapshot()
		if len(posts) != 1 || posts[0].Payload.TicketID != ticket.ID || posts[0].Payload.State != tickets.StatePRReady {
			t.Fatalf("posts = %+v", posts)
		}

		// Subsequent PR checks do not notify again.
		checkPRs(t, e)
		notifier.Wait()
		if len(spy.snapshot()) != 1 {
			t.Fatalf("notified more than once: %d", len(spy.snapshot()))
		}
	})

	t.Run("failed check notifies once as failed", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "https://machinist.example.com", io.Discard)
		e, pull, ticket := handedOver(t, true, &protocol.Ship{Verdict: ship.Accepted})
		e.Notify = notifier

		pull.checks = []ship.Check{{Name: "test", Bucket: "fail", Link: "https://ci.example.test/secret"}}
		checkPRs(t, e)
		notifier.Wait()
		posts := spy.snapshot()
		if len(posts) != 1 || posts[0].Payload.TicketID != ticket.ID || posts[0].Payload.State != tickets.StateFailed {
			t.Fatalf("posts = %+v", posts)
		}
		if strings.Contains(posts[0].Raw, "ci.example.test/secret") {
			t.Fatalf("leaked check failure message: %s", posts[0].Raw)
		}
	})
}

func TestWebhookNotifiesOnEngineFailurePaths(t *testing.T) {
	t.Run("missing preset in Tick", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		ticket := newPrivateTicket(t, e, "Missing preset")
		delete(e.Factory.Presets, "default")
		if err := e.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		notifier.Wait()
		posts := spy.snapshot()
		if len(posts) != 1 || posts[0].Payload.TicketID != ticket.ID || posts[0].Payload.State != tickets.StateFailed {
			t.Fatalf("posts = %+v", posts)
		}
	})

	t.Run("lost runner maxAttempts in Tick", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		ticket := newPrivateTicket(t, e, "Lost runner")
		for range maxAttempts {
			run := assignRun(t, e)
			if err := e.Complete(t.Context(), run.RunID, protocol.Completion{State: tickets.RunCancelled}); err != nil {
				t.Fatal(err)
			}
		}
		notifier.Wait()
		if len(spy.snapshot()) != 0 {
			t.Fatalf("notified before max attempts reached")
		}
		if err := e.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		notifier.Wait()
		posts := spy.snapshot()
		if len(posts) != 1 || posts[0].Payload.TicketID != ticket.ID || posts[0].Payload.State != tickets.StateFailed {
			t.Fatalf("posts = %+v", posts)
		}
	})

	t.Run("GitHub token error in Assign", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusOK)
		notifier := NewWebhookNotifier(spy.server.URL, "", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		e.GitHubToken = func(context.Context, string) (string, error) {
			return "", errors.New("app not installed")
		}
		ticket := newPrivateTicket(t, e, "Token failure")
		if err := e.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if run, err := e.Assign(t.Context(), "runner-1", []string{"steps"}, nil, false); err != nil || run != nil {
			t.Fatalf("run = %v, err = %v", run, err)
		}
		notifier.Wait()
		posts := spy.snapshot()
		if len(posts) != 1 || posts[0].Payload.TicketID != ticket.ID || posts[0].Payload.State != tickets.StateFailed {
			t.Fatalf("posts = %+v", posts)
		}
	})
}

func TestFailingWebhookIsLoggedNotRetriedAndNeverBlocksOrFailsTicket(t *testing.T) {
	t.Run("non-2xx status is logged once and not retried", func(t *testing.T) {
		spy := newWebhookSpy(t, http.StatusInternalServerError)
		var log bytes.Buffer
		notifier := NewWebhookNotifier(spy.server.URL+"/services/T000/SECRET_TOKEN", "https://machinist.example.com", &log)
		e := newEngine(t)
		e.Notify = notifier
		ticket := newPrivateTicket(t, e, "Failing webhook")
		run := assignRun(t, e)
		if err := e.Complete(t.Context(), run.RunID, protocol.Completion{State: tickets.RunFailed, Error: "exit 1"}); err != nil {
			t.Fatalf("Complete failed because of webhook: %v", err)
		}
		notifier.Wait()
		if got := state(t, e, ticket.ID); got != tickets.StateFailed {
			t.Fatalf("state = %s, want failed", got)
		}
		if posts := spy.snapshot(); len(posts) != 1 {
			t.Fatalf("posts = %d, want 1 (no retries)", len(posts))
		}
		logged := log.String()
		if !strings.Contains(logged, ticket.ID) || !strings.Contains(logged, "500") {
			t.Fatalf("log = %q", logged)
		}
		if strings.Contains(logged, "SECRET_TOKEN") {
			t.Fatalf("log leaked webhook secret: %q", logged)
		}
	})

	t.Run("unreachable webhook is logged with redacted URL and does not fail ticket", func(t *testing.T) {
		closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		webhookURL := closed.URL + "/services/T000/SECRET_TOKEN"
		closed.Close()

		var log bytes.Buffer
		notifier := NewWebhookNotifier(webhookURL, "", &log)
		e := newEngine(t)
		e.Notify = notifier
		e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: 9}, nil }
		ticket := newPrivateTicket(t, e, "Unreachable webhook")
		run := assignRun(t, e)
		if err := e.Complete(t.Context(), run.RunID, succeed(protocol.OutcomePRReady, "Opened")); err != nil {
			t.Fatalf("Complete failed because of unreachable webhook: %v", err)
		}
		notifier.Wait()
		if got := state(t, e, ticket.ID); got != tickets.StatePRReady {
			t.Fatalf("state = %s, want pr_ready", got)
		}
		logged := log.String()
		if !strings.Contains(logged, ticket.ID) {
			t.Fatalf("log = %q", logged)
		}
		if strings.Contains(logged, "SECRET_TOKEN") {
			t.Fatalf("log leaked webhook secret: %q", logged)
		}
	})

	t.Run("slow webhook never blocks Complete", func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		var received atomic.Int32
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			received.Add(1)
			<-release
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(func() {
			unblock()
			slow.Close()
		})

		notifier := NewWebhookNotifier(slow.URL, "", io.Discard)
		e := newEngine(t)
		e.Notify = notifier
		newPrivateTicket(t, e, "Slow webhook")
		run := assignRun(t, e)

		done := make(chan error, 1)
		go func() {
			done <- e.Complete(t.Context(), run.RunID, protocol.Completion{State: tickets.RunFailed, Error: "exit 1"})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Complete blocked on slow webhook")
		}
		unblock()
		notifier.Wait()
		if received.Load() != 1 {
			t.Fatalf("received = %d, want 1", received.Load())
		}
	})
}

func TestNoWebhookConfigured(t *testing.T) {
	if notifier := NewWebhookNotifier("", "https://machinist.example.com", io.Discard); notifier != nil {
		t.Fatalf("expected nil notifier for empty webhook URL, got %+v", notifier)
	}
	e := newEngine(t)
	e.FindPR = func(context.Context, string, string) (*PR, error) { return &PR{Number: 12}, nil }

	prTicket := newPrivateTicket(t, e, "PR without webhook")
	run := assignRun(t, e)
	if err := e.Complete(t.Context(), run.RunID, succeed(protocol.OutcomePRReady, "Opened")); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, prTicket.ID); got != tickets.StatePRReady {
		t.Fatalf("state = %s, want pr_ready", got)
	}

	failTicket := newPrivateTicket(t, e, "Fail without webhook")
	run = assignRun(t, e)
	if err := e.Complete(t.Context(), run.RunID, protocol.Completion{State: tickets.RunFailed, Error: "exit 1"}); err != nil {
		t.Fatal(err)
	}
	if got := state(t, e, failTicket.ID); got != tickets.StateFailed {
		t.Fatalf("state = %s, want failed", got)
	}
}
