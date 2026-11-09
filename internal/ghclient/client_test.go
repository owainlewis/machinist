package ghclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/fakegh"
)

func TestCommentsReadBothKindsAndAskFromSince(t *testing.T) {
	github := fakegh.Install(t, "bot")
	github.AddComment(fakegh.Comment{ID: 1, Author: "alice", Body: "on issue", Number: 3})
	github.AddComment(fakegh.Comment{ID: 2, Author: "bob", Body: "on pr", Number: 9, OnPR: true})
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	comments, err := New().Comments(t.Context(), "owner/repo", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[0] != (Comment{ID: 1, Body: "on issue", Author: "alice", Number: 3}) || comments[1] != (Comment{ID: 2, Body: "on pr", Author: "bob", Number: 9, OnPR: true}) {
		t.Fatalf("comments = %+v", comments)
	}
	if call := github.Calls()[0]; !strings.Contains(call, "since=2026-01-02T03%3A04%3A05Z") || !strings.Contains(call, "--slurp") {
		t.Fatalf("call = %s", call)
	}
}

func TestPermission(t *testing.T) {
	github := fakegh.Install(t, "bot")
	github.SetPermission("alice", "write")
	client := New()
	if got, err := client.Permission(t.Context(), "owner/repo", "alice"); err != nil || got != "write" {
		t.Fatalf("got %q, err = %v", got, err)
	}
	if got, err := client.Permission(t.Context(), "owner/repo", "stranger"); err != nil || got != "none" {
		t.Fatalf("got %q, err = %v", got, err)
	}
	if _, err := client.Permission(t.Context(), "owner/repo", "../evil"); err == nil {
		t.Fatal("bad login accepted")
	}
	for _, login := range []string{"old-account-", "alice_corp", "dependabot[bot]"} {
		if got, err := client.Permission(t.Context(), "owner/repo", login); err != nil || got != "none" {
			t.Fatalf("%s: got %q, err = %v", login, got, err)
		}
	}
	for permission, want := range map[string]bool{"admin": true, "maintain": true, "write": true, "triage": false, "read": false, "none": false} {
		if CanWrite(permission) != want {
			t.Fatalf("CanWrite(%s) != %v", permission, want)
		}
	}
}

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

func TestPullRequestReportsFailuresOnlyWhenChecksFinish(t *testing.T) {
	github := fakegh.Install(t, "bot")
	client := New()
	github.SetPR(5, fakegh.PR{State: StateOpen, HeadSHA: "abc", Checks: []fakegh.Check{{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE"}, {Name: "lint", Status: "IN_PROGRESS"}}, Reviews: []fakegh.Review{{ID: "r1", Author: "alice", State: ReviewChangesRequested}}})
	pr, err := client.PullRequest(t.Context(), "owner/repo", 5)
	if err != nil || pr.State != StateOpen || pr.HeadSHA != "abc" || len(pr.FailedChecks) != 0 || len(pr.Reviews) != 1 || pr.Reviews[0].Author != "alice" {
		t.Fatalf("pr = %+v, err = %v", pr, err)
	}
	github.SetPR(5, fakegh.PR{State: StateOpen, HeadSHA: "abc", Checks: []fakegh.Check{{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE"}, {Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"}, {Name: "old", Status: "COMPLETED", Conclusion: "CANCELLED"}}})
	if pr, err = client.PullRequest(t.Context(), "owner/repo", 5); err != nil || strings.Join(pr.FailedChecks, ",") != "test" {
		t.Fatalf("pr = %+v, err = %v", pr, err)
	}
	if _, err = client.PullRequest(t.Context(), "owner/repo", 404); err == nil {
		t.Fatal("missing PR accepted")
	}
}

func TestPullRequestQueryIsBalanced(t *testing.T) {
	if opened, closed := strings.Count(pullRequestQuery, "{"), strings.Count(pullRequestQuery, "}"); opened != closed {
		t.Fatalf("%d opening braces, %d closing braces", opened, closed)
	}
}

func TestFailedChecksHandlesStatusesAndLongLists(t *testing.T) {
	status := func(state string) *checkRollup {
		rollup := &checkRollup{}
		rollup.Contexts.Nodes = []checkContext{{Type: "StatusContext", Context: "ci/old", State: state}}
		return rollup
	}
	if got := failedChecks(status("ERROR")); strings.Join(got, ",") != "ci/old" {
		t.Fatalf("failed = %v", got)
	}
	if got := failedChecks(status("PENDING")); got != nil {
		t.Fatalf("failed = %v", got)
	}
	long := status("ERROR")
	long.Contexts.PageInfo.HasNextPage = true
	if got := failedChecks(long); got != nil {
		t.Fatalf("failed = %v", got)
	}
	if got := failedChecks(nil); got != nil {
		t.Fatalf("failed = %v", got)
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

func TestCurrentUserIssueCommitsAndAccess(t *testing.T) {
	github := fakegh.Install(t, "machinist-bot")
	client := New()
	if login, err := client.CurrentUser(t.Context()); err != nil || login != "machinist-bot" {
		t.Fatalf("login = %q, err = %v", login, err)
	}
	github.SetIssue(3, "Title", "Text")
	if issue, err := client.Issue(t.Context(), "owner/repo", 3); err != nil || issue.Title != "Title" || issue.Body != "Text" {
		t.Fatalf("issue = %+v, err = %v", issue, err)
	}
	github.SetCommits(5, "a@example.test", "b@example.test")
	if emails, err := client.CommitEmails(t.Context(), "owner/repo", 5); err != nil || strings.Join(emails, ",") != "a@example.test,b@example.test" {
		t.Fatalf("emails = %v, err = %v", emails, err)
	}
	github.SetAccess(false, true)
	if access, err := client.RepositoryAccess(t.Context(), "owner/repo"); err != nil || access.Protected || !access.Admin || access.DefaultBranch != "main" {
		t.Fatalf("access = %+v, err = %v", access, err)
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
		_, err := client.CurrentUser(t.Context())
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
	_, err := client.CurrentUser(context.Background())
	var failure *Error
	if !errors.As(err, &failure) || failure.Kind != ErrorTimeout {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidRepositoryNeverRunsGh(t *testing.T) {
	github := fakegh.Install(t, "bot")
	if _, err := New().Comments(t.Context(), "../evil/repo", time.Now()); err == nil {
		t.Fatal("bad repository accepted")
	}
	if calls := github.Calls(); len(calls) != 0 {
		t.Fatalf("calls = %v", calls)
	}
}
