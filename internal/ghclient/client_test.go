package ghclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/fakegh"
)

func TestFindPRPrefersOpenAndIgnoresForks(t *testing.T) {
	github := fakegh.Install(t, "bot")
	client := New()
	if found, err := client.FindPR(t.Context(), "owner/repo", "machinist/t_1"); err != nil || found != nil {
		t.Fatalf("found = %+v, err = %v", found, err)
	}
	github.SetBranchPRs("machinist/t_1", []map[string]any{
		{"number": 9, "state": "OPEN", "isCrossRepository": true},
		{"number": 4, "state": "CLOSED", "isCrossRepository": false},
		{"number": 6, "state": "CLOSED", "isCrossRepository": false},
	})
	found, err := client.FindPR(t.Context(), "owner/repo", "machinist/t_1")
	if err != nil || found == nil || found.Number != 6 || found.State != StateClosed {
		t.Fatalf("found = %+v, err = %v", found, err)
	}
	github.SetBranchPRs("machinist/t_1", []map[string]any{
		{"number": 4, "state": "OPEN", "isCrossRepository": false},
		{"number": 6, "state": "CLOSED", "isCrossRepository": false},
	})
	if found, err = client.FindPR(t.Context(), "owner/repo", "machinist/t_1"); err != nil || found.Number != 4 {
		t.Fatalf("found = %+v, err = %v", found, err)
	}
}

func TestPostCommentSendsBodyOnStdin(t *testing.T) {
	github := fakegh.Install(t, "bot")
	if err := New().PostComment(t.Context(), "owner/repo", 12, "Hello `there`\n$(whoami)"); err != nil {
		t.Fatal(err)
	}
	posted := github.Posted()
	if len(posted) != 1 || posted[0].Number != 12 || posted[0].Body != "Hello `there`\n$(whoami)" {
		t.Fatalf("posted = %+v", posted)
	}
}

func TestIssue(t *testing.T) {
	github := fakegh.Install(t, "machinist-bot")
	client := New()
	github.SetIssue(3, "Title", "Text")
	if issue, err := client.Issue(t.Context(), "owner/repo", 3); err != nil || issue.Title != "Title" || issue.Body != "Text" {
		t.Fatalf("issue = %+v, err = %v", issue, err)
	}
}

func TestErrorsAreClassifiedAndRedacted(t *testing.T) {
	github := fakegh.Install(t, "bot")
	client := New()
	for message, kind := range map[string]ErrorKind{
		"gh: Bad credentials (HTTP 401) ghp_abc123": ErrorAuth,
		"API rate limit exceeded":                   ErrorRateLimit,
		"something else":                            ErrorCommand,
	} {
		github.Fail(message)
		_, err := client.Issue(t.Context(), "owner/repo", 1)
		var failure *Error
		if !errors.As(err, &failure) || failure.Kind != kind {
			t.Fatalf("%q: err = %v", message, err)
		}
		if strings.Contains(err.Error(), "ghp_abc123") {
			t.Fatalf("secret leaked: %v", err)
		}
	}
}

func TestTimeoutIsClassified(t *testing.T) {
	fakegh.Install(t, "bot")
	client := New()
	client.timeout = time.Nanosecond
	_, err := client.Issue(context.Background(), "owner/repo", 1)
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != ErrorTimeout {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidRepositoryNeverRunsGh(t *testing.T) {
	github := fakegh.Install(t, "bot")
	if _, err := New().Issue(t.Context(), "../evil/repo", 1); err == nil {
		t.Fatal("bad repository accepted")
	}
	if calls := github.Calls(); len(calls) != 0 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestTokenScopes(t *testing.T) {
	for _, test := range []struct{ name, header, want string }{
		{"wide", " repo , delete_repo, admin:public_key ", "repo,delete_repo,admin:public_key"},
		{"narrow", "repo", "repo"},
		{"fine-grained", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			github := fakegh.Install(t, "bot")
			if test.header != "" {
				github.SetScopes(test.header)
			}
			scopes, err := New().TokenScopes(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(scopes, ","); got != test.want {
				t.Fatalf("scopes = %q, want %q", got, test.want)
			}
			if calls := github.Calls(); len(calls) != 1 || calls[0] != "api --method GET --include user" {
				t.Fatalf("calls = %v", calls)
			}
		})
	}
}
