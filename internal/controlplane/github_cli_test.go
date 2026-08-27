package controlplane

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedGitHubResult struct {
	stdout string
	stderr string
	err    error
}

type scriptedGitHubRunner struct {
	mu      sync.Mutex
	results []scriptedGitHubResult
	calls   [][]string
}

func (runner *scriptedGitHubRunner) Run(_ context.Context, executable string, args []string) ([]byte, []byte, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.calls = append(runner.calls, append([]string{executable}, args...))
	if len(runner.results) == 0 {
		return nil, nil, errors.New("unexpected github CLI call")
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	return []byte(result.stdout), []byte(result.stderr), result.err
}

func newScriptedGitHubCLI(results ...scriptedGitHubResult) (*GitHubCLI, *scriptedGitHubRunner) {
	runner := &scriptedGitHubRunner{results: results}
	cli := NewGitHubCLI("test-gh", time.Second)
	cli.runner = runner
	return cli, runner
}

func TestGitHubCLISearchCombinesRepositoriesAndOrdersOldest(t *testing.T) {
	cli, runner := newScriptedGitHubCLI(scriptedGitHubResult{stdout: `[
  {"number":9,"repository":{"nameWithOwner":"Owner/Zed"},"state":"open","url":"https://github.com/Owner/Zed/issues/9","isPullRequest":false,"createdAt":"2026-01-02T00:00:00Z"},
  {"number":2,"repository":{"nameWithOwner":"owner/alpha"},"state":"open","url":"https://github.com/owner/alpha/issues/2","isPullRequest":false,"createdAt":"2026-01-01T00:00:00Z"}
]`})

	candidates, err := cli.SearchRequestedIssues(context.Background(), []string{"Owner/Zed", "owner/alpha", "OWNER/ZED"}, "machinist:requested", 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{candidates[0].Repository, candidates[1].Repository}; !reflect.DeepEqual(got, []string{"owner/alpha", "Owner/Zed"}) {
		t.Fatalf("unexpected candidate order: %v", got)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	call := strings.Join(runner.calls[0], " ")
	if !strings.Contains(call, "--repo owner/alpha --repo Owner/Zed") {
		t.Fatalf("repositories were not combined deterministically: %s", call)
	}
	if !strings.Contains(call, "--state open") {
		t.Fatalf("search did not exclude closed issues before applying its limit: %s", call)
	}
	if strings.Contains(call, "sh -c") {
		t.Fatalf("search unexpectedly used a shell: %s", call)
	}
}

func TestGitHubCLISearchBatchesAndAppliesGlobalLimit(t *testing.T) {
	cli, runner := newScriptedGitHubCLI(
		scriptedGitHubResult{stdout: `[{"number":2,"repository":{"nameWithOwner":"o/a"},"state":"open","url":"https://github.com/o/a/issues/2","isPullRequest":false,"createdAt":"2026-01-03T00:00:00Z"}]`},
		scriptedGitHubResult{stdout: `[{"number":1,"repository":{"nameWithOwner":"o/b"},"state":"open","url":"https://github.com/o/b/issues/1","isPullRequest":false,"createdAt":"2026-01-01T00:00:00Z"}]`},
		scriptedGitHubResult{stdout: `[{"number":3,"repository":{"nameWithOwner":"o/c"},"state":"open","url":"https://github.com/o/c/issues/3","isPullRequest":false,"createdAt":"2026-01-02T00:00:00Z"}]`},
	)
	base := []string{"search", "issues", "--label", "requested", "--state", "open", "--sort", "created", "--order", "asc", "--limit", "100", "--json", "number,repository,state,url,isPullRequest,createdAt"}
	cli.maxArgumentBytes = argumentBytes(base) + len("--repo") + 1 + len("o/a") + 1

	candidates, err := cli.SearchRequestedIssues(context.Background(), []string{"o/c", "o/b", "o/a"}, "requested", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(runner.calls))
	}
	if got := []int{candidates[0].Number, candidates[1].Number}; !reflect.DeepEqual(got, []int{1, 3}) {
		t.Fatalf("global oldest candidates = %v, want [1 3]", got)
	}
}

func TestGitHubCLISearchDeduplicatesCandidatesAcrossBatches(t *testing.T) {
	duplicate := `[{"number":1,"repository":{"nameWithOwner":"o/a"},"state":"open","url":"https://github.com/o/a/issues/1","isPullRequest":false,"createdAt":"2026-01-01T00:00:00Z"}]`
	cli, _ := newScriptedGitHubCLI(scriptedGitHubResult{stdout: duplicate}, scriptedGitHubResult{stdout: duplicate})
	cli.maxArgumentBytes = 1
	candidates, err := cli.SearchRequestedIssues(context.Background(), []string{"o/a", "o/b"}, "requested", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
}

func TestGitHubCLIIssueDetailsReadsLatestLabelEventActor(t *testing.T) {
	cli, runner := newScriptedGitHubCLI(
		scriptedGitHubResult{stdout: `{"number":7,"html_url":"https://github.com/o/r/issues/7","state":"open","created_at":"2026-01-01T00:00:00Z","labels":[{"name":"machinist:requested"},{"name":"bug"}]}`},
		scriptedGitHubResult{stdout: `[[
  {"id":41,"event":"labeled","created_at":"2026-01-02T00:00:00Z","actor":{"login":"first"},"label":{"name":"machinist:requested"}},
  {"id":42,"event":"unlabeled","created_at":"2026-01-03T00:00:00Z","actor":{"login":"first"},"label":{"name":"machinist:requested"}}
],[
  {"id":9007199254740993,"event":"labeled","created_at":"2026-01-04T00:00:00Z","actor":{"login":"maintainer"},"label":{"name":"machinist:requested"}}
]]`},
	)

	details, err := cli.IssueDetails(context.Background(), "o/r", 7, "machinist:requested")
	if err != nil {
		t.Fatal(err)
	}
	if details.RequestedEvent == nil {
		t.Fatal("missing requested event")
	}
	if details.RequestedEvent.ID != "9007199254740993" || details.RequestedEvent.Actor != "maintainer" || details.RequestedEvent.OccurrenceKey != "github.com:9007199254740993" {
		t.Fatalf("unexpected event: %+v", details.RequestedEvent)
	}
	if !reflect.DeepEqual(details.Labels, []string{"bug", "machinist:requested"}) {
		t.Fatalf("labels = %v", details.Labels)
	}
	if !strings.Contains(strings.Join(runner.calls[1], " "), "--paginate --slurp") {
		t.Fatalf("timeline call was not paginated: %v", runner.calls[1])
	}
}

func TestGitHubCLIIssueDetailsMatchesRequestedLabelCaseInsensitively(t *testing.T) {
	cli, _ := newScriptedGitHubCLI(
		scriptedGitHubResult{stdout: `{"number":7,"html_url":"https://github.com/o/r/issues/7","state":"open","created_at":"2026-01-01T00:00:00Z","labels":[{"name":"Machinist:Requested"}]}`},
		scriptedGitHubResult{stdout: `[[{"id":41,"event":"labeled","created_at":"2026-01-02T00:00:00Z","actor":{"login":"maintainer"},"label":{"name":"Machinist:Requested"}}]]`},
	)

	details, err := cli.IssueDetails(context.Background(), "o/r", 7, "machinist:requested")
	if err != nil {
		t.Fatal(err)
	}
	if details.RequestedEvent == nil || details.RequestedEvent.ID != "41" {
		t.Fatalf("requested event = %#v", details.RequestedEvent)
	}
}

func TestGitHubCLIIssueDetailsIdentifiesPullRequestAndMissingEvent(t *testing.T) {
	cli, _ := newScriptedGitHubCLI(
		scriptedGitHubResult{stdout: `{"number":7,"html_url":"https://github.com/o/r/pull/7","state":"closed","created_at":"2026-01-01T00:00:00Z","pull_request":{"url":"x"},"labels":[]}`},
		scriptedGitHubResult{stdout: `[]`},
	)
	details, err := cli.IssueDetails(context.Background(), "o/r", 7, "requested")
	if err != nil {
		t.Fatal(err)
	}
	if !details.IsPullRequest || details.RequestedEvent != nil {
		t.Fatalf("unexpected details: %+v", details)
	}
	if GitHubIssueIsEligible(details, []string{"o/r"}) {
		t.Fatal("closed pull request was eligible")
	}
}

func TestGitHubIssueEligibilityRejectsUnconfiguredRepository(t *testing.T) {
	details := GitHubIssueDetails{
		GitHubCandidate: GitHubCandidate{Repository: "o/r", State: "OPEN"},
		RequestedEvent:  &GitHubLabelEvent{ID: "1"},
	}
	if !GitHubIssueIsEligible(details, []string{"O/R"}) {
		t.Fatal("configured open issue was rejected")
	}
	if GitHubIssueIsEligible(details, []string{"o/other"}) {
		t.Fatal("unconfigured repository was eligible")
	}
}

func TestGitHubCLIPermissionAndWritePolicy(t *testing.T) {
	cli, runner := newScriptedGitHubCLI(scriptedGitHubResult{stdout: `{"permission":"maintain"}`})
	permission, err := cli.Permission(context.Background(), "o/r", "some-user")
	if err != nil {
		t.Fatal(err)
	}
	if permission != "maintain" || !GitHubPermissionCanWrite(permission) {
		t.Fatalf("permission = %q", permission)
	}
	if GitHubPermissionCanWrite("triage") || GitHubPermissionCanWrite("read") {
		t.Fatal("non-write permission was accepted")
	}
	if !strings.Contains(strings.Join(runner.calls[0], " "), "repos/o/r/collaborators/some-user/permission") {
		t.Fatalf("unexpected permission call: %v", runner.calls[0])
	}
}

func TestGitHubCLIReplaceLabelIsRepairableAfterPartialFailure(t *testing.T) {
	cli, runner := newScriptedGitHubCLI(
		scriptedGitHubResult{},
		scriptedGitHubResult{stderr: "temporary API failure", err: errors.New("exit 1")},
	)
	err := cli.ReplaceRequestLabel(context.Background(), "o/r", 7, "machinist:requested", "machinist:queued")
	if err == nil {
		t.Fatal("expected partial label update failure")
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(runner.calls))
	}
	if got := strings.Join(runner.calls[0], " "); !strings.Contains(got, "--add-label machinist:queued") {
		t.Fatalf("queued label was not added first: %s", got)
	}
	if got := strings.Join(runner.calls[1], " "); !strings.Contains(got, "--remove-label machinist:requested") {
		t.Fatalf("request label was not removed second: %s", got)
	}
}

func TestGitHubCLIReplaceLabelRejectsCaseInsensitiveCollision(t *testing.T) {
	cli, runner := newScriptedGitHubCLI()
	err := cli.ReplaceRequestLabel(context.Background(), "o/r", 7, "Machinist:Queued", "machinist:queued")
	if err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("colliding labels reached executable: %v", runner.calls)
	}
}

func TestGitHubCLIClassifiesAuthenticationAndRateLimits(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		kind   GitHubCLIErrorKind
	}{
		{name: "authentication", stderr: "HTTP 401: Bad credentials ghp_supersecret", kind: GitHubCLIErrorAuth},
		{name: "rate limit", stderr: "API rate limit exceeded", kind: GitHubCLIErrorRateLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cli, _ := newScriptedGitHubCLI(scriptedGitHubResult{stderr: test.stderr, err: errors.New("exit status 1")})
			_, err := cli.SearchRequestedIssues(context.Background(), []string{"o/r"}, "requested", 100)
			var cliErr *GitHubCLIError
			if !errors.As(err, &cliErr) || cliErr.Kind != test.kind {
				t.Fatalf("error = %#v, want kind %q", err, test.kind)
			}
			if strings.Contains(err.Error(), "ghp_supersecret") || len(cliErr.Detail) > maxGitHubErrorBytes+3 {
				t.Fatalf("error was not bounded and sanitized: %v", err)
			}
		})
	}
}

type blockingGitHubRunner struct{}

func (blockingGitHubRunner) Run(ctx context.Context, _ string, _ []string) ([]byte, []byte, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

func TestGitHubCLIClassifiesTimeout(t *testing.T) {
	cli := NewGitHubCLI("test-gh", time.Millisecond)
	cli.runner = blockingGitHubRunner{}
	_, err := cli.SearchRequestedIssues(context.Background(), []string{"o/r"}, "requested", 100)
	var cliErr *GitHubCLIError
	if !errors.As(err, &cliErr) || cliErr.Kind != GitHubCLIErrorTimeout {
		t.Fatalf("error = %#v, want timeout", err)
	}
}

func TestGitHubCLIRejectsMalformedOutput(t *testing.T) {
	cli, _ := newScriptedGitHubCLI(scriptedGitHubResult{stdout: `{"not":"an array"}`})
	_, err := cli.SearchRequestedIssues(context.Background(), []string{"o/r"}, "requested", 100)
	var cliErr *GitHubCLIError
	if !errors.As(err, &cliErr) || cliErr.Kind != GitHubCLIErrorMalformed {
		t.Fatalf("error = %#v, want malformed output", err)
	}
}

func TestGitHubCLIRejectsUnsafeInputsBeforeExecution(t *testing.T) {
	cli, runner := newScriptedGitHubCLI()
	if _, err := cli.SearchRequestedIssues(context.Background(), []string{"o/r;touch bad"}, "requested", 100); err == nil {
		t.Fatal("unsafe repository was accepted")
	}
	if _, err := cli.Permission(context.Background(), "o/r", "name/path"); err == nil {
		t.Fatal("unsafe actor was accepted")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unsafe input reached executable: %v", runner.calls)
	}
}
