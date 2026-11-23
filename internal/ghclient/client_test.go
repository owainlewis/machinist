package ghclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	github.SetBranchPRs("machinist/t_1", []map[string]any{
		{"number": 4, "state": "MERGED", "isCrossRepository": false, "mergedAt": "2026-11-20T10:30:00Z", "closedAt": "2026-11-20T10:30:00Z", "createdAt": "2026-11-20T09:00:00Z"},
	})
	if found, err = client.FindPR(t.Context(), "owner/repo", "machinist/t_1"); err != nil || found.State != StateMerged || found.MergedAt.Format(time.RFC3339) != "2026-11-20T10:30:00Z" || found.CreatedAt.Format(time.RFC3339) != "2026-11-20T09:00:00Z" {
		t.Fatalf("found = %+v, err = %v", found, err)
	}
}

func TestReviewsListsSubmittedReviewsOldestFirstAndMarksBots(t *testing.T) {
	github := fakegh.Install(t, "bot")
	github.SetReviews(7,
		map[string]any{"user": map[string]any{"login": "alice", "type": "User"}, "state": "APPROVED", "submitted_at": "2026-11-20T11:00:00Z"},
		map[string]any{"user": map[string]any{"login": "copilot-pull-request-reviewer[bot]", "type": "Bot"}, "state": "COMMENTED", "submitted_at": "2026-11-20T09:30:00Z"},
		map[string]any{"user": map[string]any{"login": "bob", "type": "User"}, "state": "PENDING"},
		map[string]any{"user": map[string]any{"login": "bob", "type": "User"}, "state": "CHANGES_REQUESTED", "submitted_at": "2026-11-20T10:00:00Z"},
	)
	reviews, err := New().Reviews(t.Context(), "owner/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 3 || reviews[0].Login != "copilot-pull-request-reviewer[bot]" || !reviews[0].Bot || reviews[1].Login != "bob" || reviews[1].Bot || reviews[2].Login != "alice" {
		t.Fatalf("reviews = %+v", reviews)
	}
	if calls := github.Calls(); !strings.Contains(calls[len(calls)-1], "repos/owner/repo/pulls/7/reviews") || !strings.Contains(calls[len(calls)-1], "--paginate") {
		t.Fatalf("calls = %v", calls)
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

func TestWithTokenGivesEachCommandATokenForItsRepository(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "gh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s %s' \"$GH_TOKEN\" \"$GITHUB_TOKEN\" > \""+directory+"/seen\"\necho '{}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "ghp_broad")
	t.Setenv("GITHUB_TOKEN", "ghp_broad")
	asked := ""
	client := (&Client{executable: script, timeout: time.Minute}).WithToken(func(_ context.Context, repository string) (string, error) {
		asked = repository
		return "ghs_scoped", nil
	})
	if _, err := client.Issue(t.Context(), "owner/repo", 1); err != nil {
		t.Fatal(err)
	}
	if seen, _ := os.ReadFile(filepath.Join(directory, "seen")); string(seen) != "ghs_scoped " || asked != "owner/repo" {
		t.Fatalf("seen = %q for %q", seen, asked)
	}
	failing := (&Client{executable: script, timeout: time.Minute}).WithToken(func(context.Context, string) (string, error) {
		return "", errors.New("the GitHub App is not installed on owner/repo")
	})
	var ghError *Error
	if _, err := failing.Issue(t.Context(), "owner/repo", 1); !errors.As(err, &ghError) || ghError.Kind != ErrorAuth || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("error = %v", err)
	}
}

func TestRepositoryAccessReadsProtectionAndAdmin(t *testing.T) {
	github := fakegh.Install(t, "bot")
	client := New()
	if access, err := client.RepositoryAccess(t.Context(), "owner/repo"); err != nil || access != (Access{DefaultBranch: "main", Protected: true}) {
		t.Fatalf("access = %+v, err = %v", access, err)
	}
	github.SetAccess(false, true)
	if access, err := client.RepositoryAccess(t.Context(), "owner/repo"); err != nil || access != (Access{DefaultBranch: "main", Admin: true}) {
		t.Fatalf("access = %+v, err = %v", access, err)
	}
	if _, err := client.RepositoryAccess(t.Context(), "../repo"); err == nil {
		t.Fatal("an invalid repository was read")
	}
	github.Fail("gh: Not Found (HTTP 404)")
	if _, err := client.RepositoryAccess(t.Context(), "owner/repo"); !IsNotFound(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestWatchingAPullRequest(t *testing.T) {
	github := fakegh.Install(t, "bot")
	github.SetBranchPR("machinist/t_1", 7, "OPEN", false)
	client := New()
	ctx := t.Context()
	pr, err := client.PullRequest(ctx, "owner/repo", 7)
	if err != nil || pr.Number != 7 || pr.State != StateOpen || pr.URL != "https://github.com/owner/repo/pull/7" || pr.MergeStateStatus != "CLEAN" || pr.HeadRefOid == "" {
		t.Fatalf("pr = %+v, err = %v", pr, err)
	}
	// gh exits 8 while checks are pending, and still prints them.
	github.SetChecks([]map[string]string{{"name": "test", "bucket": "pending", "link": "https://ci/1"}})
	checks, err := client.Checks(ctx, "owner/repo", 7)
	if err != nil || len(checks) != 1 || checks[0] != (Check{Name: "test", Bucket: "pending", Link: "https://ci/1"}) {
		t.Fatalf("checks = %+v, err = %v", checks, err)
	}
	github.SetNoChecks()
	if checks, err := client.Checks(ctx, "owner/repo", 7); err != nil || len(checks) != 0 {
		t.Fatalf("checks = %+v, err = %v", checks, err)
	}
	if err := client.UpdateBranch(ctx, "owner/repo", 7); err != nil {
		t.Fatal(err)
	}
	if err := client.MergePR(ctx, "owner/repo", 7, "sha1"); err != nil {
		t.Fatal(err)
	}
	if merged := github.Merged(); len(merged) != 1 || merged[0] != "7" {
		t.Fatalf("merged = %v", merged)
	}
	want := []string{
		"pr view 7 --repo owner/repo --json number,url,state,mergeStateStatus,headRefOid",
		"pr checks 7 --repo owner/repo --json name,bucket,link",
		"pr checks 7 --repo owner/repo --json name,bucket,link",
		"pr update-branch 7 --repo owner/repo",
		"api --method GET repos/owner/repo",
		"pr merge 7 --repo owner/repo --merge --delete-branch --match-head-commit sha1",
	}
	if calls := github.Calls(); strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(calls, "\n"))
	}
	github.Fail("gh: Server Error (HTTP 502)")
	if _, err := client.Checks(ctx, "owner/repo", 7); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergePRUsesAMethodTheRepositoryAllows(t *testing.T) {
	github := fakegh.Install(t, "bot")
	github.SetBranchPR("machinist/t_1", 7, "OPEN", false)
	github.SetRepository(map[string]any{"allow_merge_commit": false, "allow_squash_merge": true, "allow_rebase_merge": true})
	if err := New().MergePR(t.Context(), "owner/repo", 7, "sha1"); err != nil {
		t.Fatal(err)
	}
	calls := github.Calls()
	if last := calls[len(calls)-1]; !strings.Contains(last, "pr merge 7 --repo owner/repo --squash ") {
		t.Fatalf("merge = %s", last)
	}
}
