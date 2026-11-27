package controlplane

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/sources"
)

type stubGitHubTracker struct {
	polls atomic.Int32
}

func (s *stubGitHubTracker) Name() string { return "github" }
func (s *stubGitHubTracker) Assignments(context.Context, time.Time) ([]sources.Assignment, error) {
	s.polls.Add(1)
	return nil, nil
}
func (s *stubGitHubTracker) Start(context.Context, string) error { return nil }
func (s *stubGitHubTracker) Replies(context.Context, time.Time, time.Time) ([]sources.Reply, error) {
	return nil, nil
}
func (s *stubGitHubTracker) Return(context.Context, string, string) error  { return nil }
func (s *stubGitHubTracker) Comment(context.Context, string, string) error { return nil }
func (s *stubGitHubTracker) Fail(context.Context, string, string) error    { return nil }
func (s *stubGitHubTracker) Finish(context.Context, string) error          { return nil }
func (s *stubGitHubTracker) States(context.Context, []string) (map[string]sources.IssueState, error) {
	return map[string]sources.IssueState{}, nil
}

func TestGitHubIssuesPollerWiringAndValidation(t *testing.T) {
	intake := linearIntake(t, "owner/alpha", "owner/beta")
	base := config.Config{
		Server: config.Server{DefaultPreset: "default"},
		GitHub: config.GitHub{Handle: "machinist-bot", Repositories: []string{"owner/alpha", "owner/beta"}},
		GitHubIssues: config.GitHubIssues{
			AllowedUsers: []string{"alice"},
			Token:        "ghp_test",
		},
	}

	// Default repositories inherit all github.repositories.
	poller, err := githubIssuesPoller(base, testFactory(), nil, intake, intake.Store, io.Discard)
	if err != nil || poller == nil {
		t.Fatalf("poller = %v, err = %v", poller, err)
	}
	if !slices.Equal(poller.Rules.Repositories, []string{"owner/alpha", "owner/beta"}) || poller.Rules.Preset != "default" || !poller.Rules.CaseInsensitiveUsers {
		t.Fatalf("rules = %+v", poller.Rules)
	}
	if poller.Interval != 60*time.Second || poller.MaxJobsPerHour != 10 {
		t.Fatalf("interval=%s cap=%d", poller.Interval, poller.MaxJobsPerHour)
	}

	// Explicit subset of github.repositories.
	subset := base
	subset.GitHubIssues.Repositories = []string{"Owner/Beta"}
	poller, err = githubIssuesPoller(subset, testFactory(), nil, intake, intake.Store, io.Discard)
	if err != nil || poller == nil {
		t.Fatalf("subset poller = %v, err = %v", poller, err)
	}
	if !slices.Equal(poller.Rules.Repositories, []string{"owner/beta"}) {
		t.Fatalf("subset repos = %v", poller.Rules.Repositories)
	}

	// Repository outside github.repositories is rejected.
	outside := base
	outside.GitHubIssues.Repositories = []string{"owner/unknown"}
	if _, err := githubIssuesPoller(outside, testFactory(), nil, intake, intake.Store, io.Discard); err == nil || !strings.Contains(err.Error(), "must also be listed in github.repositories") {
		t.Fatalf("outside repo err = %v", err)
	}

	// Unknown preset is rejected.
	badPreset := base
	badPreset.GitHubIssues.Preset = "nonexistent"
	if _, err := githubIssuesPoller(badPreset, testFactory(), nil, intake, intake.Store, io.Discard); err == nil || !strings.Contains(err.Error(), "not in the factory") {
		t.Fatalf("bad preset err = %v", err)
	}

	// Missing credentials without app or GH_TOKEN is rejected.
	t.Setenv(config.GitHubTokenEnv, "")
	t.Setenv("GITHUB_TOKEN", "")
	noCreds := base
	noCreds.GitHubIssues.Token = ""
	if _, err := githubIssuesPoller(noCreds, testFactory(), nil, intake, intake.Store, io.Discard); err == nil || !strings.Contains(err.Error(), config.GitHubTokenEnv) {
		t.Fatalf("missing creds err = %v", err)
	}

	// GitHub App provides TokenFor without needing GH_TOKEN.
	appCfg := appConfig(t, "")
	appCfg.GitHub.Repositories = []string{"owner/alpha"}
	appCfg.GitHubIssues = config.GitHubIssues{AllowedUsers: []string{"alice"}}
	srv, err := New(appCfg, intake.Store, testFactory(), "v", io.Discard)
	if err != nil {
		t.Fatalf("New with GitHub App + github_issues: %v", err)
	}
	if srv.githubIssues == nil {
		t.Fatal("expected githubIssues poller to be enabled")
	}
	tracker, ok := srv.githubIssues.Tracker.(*sources.GitHubIssues)
	if !ok || tracker.TokenFor == nil || tracker.Token != "" {
		t.Fatalf("expected App-backed TokenFor, got %+v", tracker)
	}
}

func TestLeaderSupervisorRunsGitHubIssuesPoller(t *testing.T) {
	fakegh.Install(t, testConfig().GitHub.Handle)
	store := openTestStore(t)
	cfg := testConfig()
	srv, err := New(cfg, store, testFactory(), "v1", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubGitHubTracker{}
	srv.githubIssues = &sources.Poller{
		Tracker:        stub,
		Rules:          sources.TrackerRules{AllowedUsers: []string{"alice"}, CaseInsensitiveUsers: true, Preset: "default", Repositories: cfg.GitHub.Repositories},
		Intake:         srv.intake,
		Store:          store,
		Interval:       20 * time.Millisecond,
		MaxJobsPerHour: 10,
		Log:            io.Discard,
	}
	srv.leaderCfg = leaderConfig{
		holderID:        "cp_gh_issues",
		ttl:             5 * time.Second,
		renewInterval:   200 * time.Millisecond,
		acquireInterval: 50 * time.Millisecond,
		rpcTimeout:      2 * time.Second,
		safetyMargin:    500 * time.Millisecond,
		releaseTimeout:  2 * time.Second,
		drainTimeout:    2 * time.Second,
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.runLeaderSupervisorWithDrain(ctx, nil)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if stub.polls.Load() >= 2 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	cancel()
	<-done
	if stub.polls.Load() < 2 {
		t.Fatalf("githubIssues poller polls = %d, want >= 2 under leader lease", stub.polls.Load())
	}
}

func TestReloadGitHubIssuesEnableUpdateDisableAndRejectInvalid(t *testing.T) {
	t.Setenv(config.GitHubTokenEnv, "ghp_reload_token")
	rf := newReloadFixture(t)

	// 1. Enable github_issues via reload.
	writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default
  run_timeout: 30m

github:
  handle: machinist-bot
  repositories:
    - owner/alpha
    - owner/beta

github_issues:
  allowed_users: [alice]
  poll_seconds: 30
  max_jobs_per_hour: 8
`)
	status, res, errMsg := rf.postReload(t, rf.token)
	if status != http.StatusOK {
		t.Fatalf("enable reload failed: %d %q", status, errMsg)
	}
	if !slices.Contains(res.Changed, "github_issues: enabled") {
		t.Fatalf("changed = %v, want github_issues: enabled", res.Changed)
	}
	poller := rf.server.state().githubIssues
	if poller == nil || poller.Interval != 30*time.Second || poller.MaxJobsPerHour != 8 || !slices.Equal(poller.Rules.Repositories, []string{"owner/alpha", "owner/beta"}) {
		t.Fatalf("reloaded githubIssues poller = %+v", poller)
	}

	// 2. Update github_issues settings via reload.
	writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default
  run_timeout: 30m

github:
  handle: machinist-bot
  repositories:
    - owner/alpha
    - owner/beta

github_issues:
  label: bot-work
  allowed_users: [alice, bob]
  repositories: [owner/beta]
  poll_seconds: 45
  max_jobs_per_hour: 12
`)
	status, res, errMsg = rf.postReload(t, rf.token)
	if status != http.StatusOK {
		t.Fatalf("update reload failed: %d %q", status, errMsg)
	}
	if !slices.Contains(res.Changed, "github_issues: updated (label, allowed_users, repositories, poll_seconds, max_jobs_per_hour)") {
		t.Fatalf("changed = %v", res.Changed)
	}
	poller = rf.server.state().githubIssues
	if poller == nil || poller.Interval != 45*time.Second || poller.MaxJobsPerHour != 12 || !slices.Equal(poller.Rules.Repositories, []string{"owner/beta"}) {
		t.Fatalf("updated githubIssues poller = %+v", poller)
	}

	// 3. Invalid github_issues config rejects reload and preserves previous poller.
	writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default
  run_timeout: 30m

github:
  handle: machinist-bot
  repositories:
    - owner/alpha

github_issues:
  allowed_users: [alice]
  preset: missing-preset
`)
	status, _, errMsg = rf.postReload(t, rf.token)
	if status != http.StatusBadRequest || !strings.Contains(errMsg, "missing-preset") {
		t.Fatalf("invalid reload = %d %q", status, errMsg)
	}
	if got := rf.server.state().githubIssues; got != poller {
		t.Fatal("failed reload altered active githubIssues poller")
	}

	// 4. Disable github_issues via reload.
	writeFile(t, rf.configPath, `server:
  listen: 127.0.0.1:0
  database: .machinist/tickets.db
  factory: factory
  default_preset: default
  run_timeout: 30m

github:
  handle: machinist-bot
  repositories:
    - owner/alpha
`)
	status, res, errMsg = rf.postReload(t, rf.token)
	if status != http.StatusOK {
		t.Fatalf("disable reload failed: %d %q", status, errMsg)
	}
	if !slices.Contains(res.Changed, "github_issues: disabled") {
		t.Fatalf("changed = %v, want github_issues: disabled", res.Changed)
	}
	if rf.server.state().githubIssues != nil {
		t.Fatal("expected githubIssues poller to be nil after disable")
	}
}
