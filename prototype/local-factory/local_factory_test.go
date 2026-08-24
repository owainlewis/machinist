package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeGitHub struct {
	issues []issue
}

func (f fakeGitHub) Issue(_ context.Context, repository string, number int) (issue, error) {
	for _, value := range f.issues {
		if value.Repository == repository && value.Number == number {
			return value, nil
		}
	}
	return issue{}, fmt.Errorf("issue not found")
}

func (f fakeGitHub) LabeledIssues(context.Context, string, string) ([]issue, error) {
	return append([]issue(nil), f.issues...), nil
}

func (fakeGitHub) UpdateIssueBody(context.Context, issue, string) error { return nil }
func (fakeGitHub) CommentIssue(context.Context, issue, string) error    { return nil }
func (fakeGitHub) OpenDraftPR(context.Context, issue, string, string, string, string) (string, error) {
	return "https://github.com/acme/widgets/pull/2", nil
}

func TestParseIssueReference(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"acme/widgets#42", "https://github.com/acme/widgets/issues/42"} {
		repository, number, err := parseIssueReference(value)
		if err != nil || repository != "acme/widgets" || number != 42 {
			t.Fatalf("parseIssueReference(%q) = %q, %d, %v", value, repository, number, err)
		}
	}
	if _, _, err := parseIssueReference("widgets/42"); err == nil {
		t.Fatal("expected invalid reference to fail")
	}
}

func TestInitialiseWritesEditableProject(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "factory")
	if err := initialise(directory, "acme/widgets", "/tmp/widgets", "origin/main"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"factory.toml", "agents/foreman.md", "agents/planner.md", "agents/builder.md", "agents/verifier.md"} {
		body, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
	configPath := filepath.Join(directory, "factory.toml")
	if _, err := loadConfig(configPath); err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(strings.Replace(string(body), `"acme/widgets"`, `" acme/widgets "`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Repositories[0].GitHub != "acme/widgets" {
		t.Fatalf("repository name was not normalized: %q", loaded.Config.Repositories[0].GitHub)
	}
}

func TestExplicitRetryOnlyRequeuesFailedOrBlockedWork(t *testing.T) {
	t.Parallel()
	state := newStore(t.TempDir())
	originalIssue := issue{Repository: "acme/widgets", Number: 7, Title: "Retry me", Body: "Old body"}
	item, _, err := state.create(originalIssue)
	if err != nil {
		t.Fatal(err)
	}
	if _, retried, err := state.retry(item.ID, originalIssue); err != nil || retried {
		t.Fatalf("queued retry = %t, %v", retried, err)
	}
	if err := state.artifact(item.ID, "review.md", []byte("obsolete review\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := state.update(item.ID, func(current *work) error {
		current.State = stateFailed
		current.Failure = "agent stopped"
		current.VerifyRuns = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	refreshedIssue := originalIssue
	refreshedIssue.Body = "New details from the user"
	if err := state.archiveAttemptArtifactsUnlocked(item.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(state.workDir(item.ID), "issue.md"), []byte(renderIssue(refreshedIssue)), 0o644); err != nil {
		t.Fatal(err)
	}
	retried, changed, err := state.retry(item.ID, refreshedIssue)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || retried.State != stateQueued || retried.Attempt != 2 || retried.VerifyRuns != 0 || retried.Failure != "" {
		t.Fatalf("unexpected retry: %#v, changed=%t", retried, changed)
	}
	if retried.Issue.Body != refreshedIssue.Body {
		t.Fatalf("retry retained stale issue body %q", retried.Issue.Body)
	}
	snapshot, err := state.readArtifact(item.ID, "issue.md")
	if err != nil || !strings.Contains(string(snapshot), refreshedIssue.Body) {
		t.Fatalf("retry snapshot was not refreshed: %v\n%s", err, snapshot)
	}
	if _, err := state.readArtifact(item.ID, "review.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry retained prior review: %v", err)
	}
	archivedReview, err := os.ReadFile(filepath.Join(state.workDir(item.ID), "attempts", "attempt-1", "review.md"))
	if err != nil || string(archivedReview) != "obsolete review\n" {
		t.Fatalf("prior review was not archived: %v, %q", err, archivedReview)
	}
	archivedIssue, err := os.ReadFile(filepath.Join(state.workDir(item.ID), "attempts", "attempt-1", "issue.md"))
	if err != nil || !strings.Contains(string(archivedIssue), originalIssue.Body) || strings.Contains(string(archivedIssue), refreshedIssue.Body) {
		t.Fatalf("partial retry overwrote archived issue: %v\n%s", err, archivedIssue)
	}
}

func TestWorkIDPreservesRepositoryIdentity(t *testing.T) {
	t.Parallel()
	first := workID("acme/foo.bar", 1)
	second := workID("acme/foo-bar", 1)
	if first == second {
		t.Fatalf("distinct repositories collided at %q", first)
	}
}

func TestConcurrentRetryAdmitsOneFreshAttempt(t *testing.T) {
	t.Parallel()
	state := newStore(t.TempDir())
	original := issue{Repository: "acme/widgets", Number: 8, Title: "Retry once", Body: "Old body"}
	item, _, err := state.create(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.update(item.ID, func(current *work) error {
		current.State = stateFailed
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	type result struct {
		item    work
		changed bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, body := range []string{"First refresh", "Second refresh"} {
		refreshed := original
		refreshed.Body = body
		go func() {
			<-start
			value, changed, err := state.retry(item.ID, refreshed)
			results <- result{item: value, changed: changed, err: err}
		}()
	}
	close(start)
	changedCount := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.changed {
			changedCount++
		}
	}
	if changedCount != 1 {
		t.Fatalf("concurrent retries admitted %d attempts", changedCount)
	}
	stored, err := state.get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", stored.Attempt)
	}
	snapshot, err := state.readArtifact(item.ID, "issue.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snapshot), stored.Issue.Body) {
		t.Fatalf("snapshot and stored issue diverged:\n%s\n%#v", snapshot, stored.Issue)
	}
}

func TestVerificationVerdictUsesMinimalMarkdownContract(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		body    string
		verdict string
		valid   bool
	}{
		{"Verdict: PASS\n\nChecks passed.", "PASS", true},
		{"Verdict: REVISE\n\nFix the bug.", "REVISE", true},
		{"Everything looks good.", "", false},
	} {
		verdict, err := verificationVerdict([]byte(test.body))
		if (err == nil) != test.valid || verdict != test.verdict {
			t.Fatalf("verificationVerdict(%q) = %q, %v", test.body, verdict, err)
		}
	}
}

func TestRuntimeAuthorityIsHeldByOneServer(t *testing.T) {
	t.Parallel()
	state := newStore(t.TempDir())
	if err := state.activate("server-token"); err != nil {
		t.Fatal(err)
	}
	secondState := newStore(state.root)
	if err := secondState.activate("second-token"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second server claimed authority: %v", err)
	}
	state.deactivate("invented-token")
	if err := secondState.activate("second-token"); err == nil {
		t.Fatal("wrong token released runtime authority")
	}
	state.deactivate("server-token")
	if err := secondState.activate("second-token"); err != nil {
		t.Fatalf("released authority could not be reclaimed: %v", err)
	}
	secondState.deactivate("second-token")
}

func TestInternalAPIRejectsInventedAuthority(t *testing.T) {
	t.Parallel()
	server := server{runner: &agentRunner{authToken: "server-token"}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/internal", strings.NewReader(`{"work_id":"work-1","action":"finish"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer invented-token")
	response := httptest.NewRecorder()
	server.handleInternal(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestPositionalArgumentMustComeFirst(t *testing.T) {
	t.Parallel()
	if _, _, err := takeFirstArgument([]string{"--config", "ticket"}); err == nil {
		t.Fatal("flag value was consumed as the positional argument")
	}
	value, remaining, err := takeFirstArgument([]string{"ticket", "--config", "factory.toml"})
	if err != nil || value != "ticket" || len(remaining) != 2 {
		t.Fatalf("positional parse = %q, %v, %v", value, remaining, err)
	}
}

func TestFactoryEnvironmentDoesNotLeakCapabilities(t *testing.T) {
	t.Parallel()
	filtered := withoutFactoryEnvironment([]string{"PATH=/bin", "FACTORY_AUTH_TOKEN=attacker", "FACTORY_ROLE=stale"})
	if len(filtered) != 1 || filtered[0] != "PATH=/bin" {
		t.Fatalf("filtered environment = %v", filtered)
	}
}

func TestForemanCommandQuotesExecutable(t *testing.T) {
	t.Parallel()
	runner := agentRunner{executable: "/tmp/factory tools/factory", config: loadedConfig{Path: "/tmp/factory.toml"}}
	context := runner.foremanContext(work{ID: "work", Issue: issue{Repository: "acme/widgets", Number: 1}})
	if !strings.Contains(context, `'/tmp/factory tools/factory' internal`) {
		t.Fatalf("executable was not shell quoted:\n%s", context)
	}
}

func TestManagedPlanBodyReplacesOnlyFactoryBlock(t *testing.T) {
	t.Parallel()
	first := managedPlanBody("User description", "First plan")
	second := managedPlanBody(first, "Second plan")
	if strings.Count(second, "<!-- factory-plan:start -->") != 1 {
		t.Fatalf("managed block duplicated:\n%s", second)
	}
	if !strings.Contains(second, "User description") || !strings.Contains(second, "Second plan") || strings.Contains(second, "First plan") {
		t.Fatalf("managed plan replacement failed:\n%s", second)
	}
}

func TestConfigRejectsNonPositivePollInterval(t *testing.T) {
	t.Parallel()
	value := validTestConfig()
	for _, interval := range []string{"0s", "-1s"} {
		value.Server.PollEvery = interval
		if err := validateConfig(value); err == nil || !strings.Contains(err.Error(), "greater than zero") {
			t.Fatalf("poll interval %q was accepted: %v", interval, err)
		}
	}
}

func TestVerifierContextNamesDetachedCheckout(t *testing.T) {
	t.Parallel()
	runner := agentRunner{store: newStore(t.TempDir())}
	item := work{ID: "work", Workspace: "/tmp/mutable-builder", Issue: issue{Repository: "acme/widgets", Number: 1}}
	context := runner.roleContext(item, "verify", "/tmp/detached-verifier")
	if !strings.Contains(context, "Checkout: /tmp/detached-verifier") || strings.Contains(context, item.Workspace) {
		t.Fatalf("verifier context points at the wrong checkout:\n%s", context)
	}
}

func TestRunAPIRejectsSimpleCrossOriginContentType(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/run", strings.NewReader(`{"issue":"acme/widgets#1"}`))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	server := server{config: loadedConfig{Config: validTestConfig()}, store: newStore(t.TempDir()), github: fakeGitHub{}}
	server.handleRun(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusUnsupportedMediaType)
	}
}

func TestRunAPIDoesNotRetryWhileOldAttemptIsActive(t *testing.T) {
	t.Parallel()
	cfg := loadedConfig{Config: validTestConfig()}
	state := newStore(t.TempDir())
	value := issue{Repository: "acme/widgets", Number: 1, Title: "Blocked but still unwinding"}
	item, _, err := state.create(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.update(item.ID, func(current *work) error {
		current.State = stateBlocked
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server := server{config: cfg, store: state, github: fakeGitHub{issues: []issue{value}}, running: map[string]struct{}{item.ID: {}}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/run", strings.NewReader(`{"issue":"acme/widgets#1"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleRun(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", response.Code, http.StatusOK)
	}
	current, err := state.get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Attempt != 1 || current.State != stateBlocked {
		t.Fatalf("active attempt was retried: %#v", current)
	}
}

func TestGitHubWritesRequireRemoteBaseRef(t *testing.T) {
	t.Parallel()
	value := validTestConfig()
	value.Repositories[0].BaseRef = "HEAD"
	_, err := newServer(loadedConfig{Config: value}, fakeGitHub{}, true, log.New(io.Discard, "", 0))
	if err == nil || !strings.Contains(err.Error(), "origin/<branch>") {
		t.Fatalf("write mode accepted local base ref: %v", err)
	}
}

func TestAgentCancellationKillsDescendantProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & child=$!; echo $child; wait")
	configureAgentCommand(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("read descendant PID: %v", scanner.Err())
	}
	childPID, err := strconv.Atoi(scanner.Text())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := command.Wait(); err == nil {
		t.Fatal("cancelled command exited successfully")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant process %d survived cancellation: %v", childPID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAgentExitKillsBackgroundDescendant(t *testing.T) {
	command := exec.CommandContext(t.Context(), "sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!; exit 0")
	configureAgentCommand(command)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupAgentCommand(command); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background descendant %d survived leader exit: %v", childPID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func validTestConfig() config {
	return config{
		Version:      1,
		MaxRevisions: 2,
		Server: serverConfig{
			Listen:        "127.0.0.1:7338",
			PollEvery:     "1m",
			TriggerLabel:  "factory",
			MaxConcurrent: 1,
		},
		Repositories: []repositoryConfig{{GitHub: "acme/widgets", Path: "/tmp/widgets", BaseRef: "origin/main"}},
		Agents: map[string]agentConfig{
			"foreman":  {Runtime: "command", Prompt: "foreman.md", Command: []string{"true"}},
			"planner":  {Runtime: "command", Prompt: "planner.md", Command: []string{"true"}},
			"builder":  {Runtime: "command", Prompt: "builder.md", Command: []string{"true"}},
			"verifier": {Runtime: "command", Prompt: "verifier.md", Command: []string{"true"}},
		},
		Roles: roleConfig{Foreman: "foreman", Plan: "planner", Build: "builder", Verify: "verifier"},
	}
}

func TestRunnerCompletesRevisionLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the local factory binary")
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	mustRun(t, "", "git", "init", repository)
	mustRun(t, repository, "git", "config", "user.name", "Test")
	mustRun(t, repository, "git", "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repository, "git", "add", "README.md")
	mustRun(t, repository, "git", "commit", "-m", "chore: initial")

	packageDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "factory")
	mustRun(t, packageDirectory, "go", "build", "-o", binary, ".")

	project := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(project, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	agentScript := filepath.Join(root, "agent.sh")
	coordinatorScript := filepath.Join(root, "foreman.sh")
	writeExecutable(t, agentScript, `#!/bin/sh
set -eu
case "$FACTORY_ROLE" in
  plan)
    printf 'Outcome: READY\n\nPlan: change result.txt and verify it.\n'
    ;;
  build)
    if [ -f result.txt ]; then
      printf 'correct\n' > result.txt
      printf 'Revised result.txt after verifier feedback.\n'
    else
      printf 'first attempt\n' > result.txt
      printf 'Created the first candidate.\n'
    fi
    ;;
  verify)
    if grep -q '^correct$' result.txt; then
      printf 'Verdict: PASS\n\nThe exact candidate is correct.\n'
    else
      printf 'Verdict: REVISE\n\nresult.txt has the wrong value.\n'
    fi
    ;;
esac
`)
	writeExecutable(t, coordinatorScript, `#!/bin/sh
set -eu
factory="$FACTORY_EXECUTABLE"
base="$factory internal --config $FACTORY_CONFIG --work $FACTORY_WORK_ID"
$base delegate plan
$base publish-plan
$base delegate build
$base delegate verify
$base delegate build
$base delegate verify
$base finish
printf 'Foreman completed one revision loop.\n'
`)
	for _, name := range []string{"foreman", "planner", "builder", "verifier"} {
		if err := os.WriteFile(filepath.Join(project, "agents", name+".md"), []byte("Test "+name+" prompt.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(project, "factory.toml")
	configBody := fmt.Sprintf(`version = 1
state_directory = ".state"
max_revisions = 2
[server]
listen = "127.0.0.1:0"
poll_every = "1h"
trigger_label = "factory-test-never"
max_concurrent = 1
[[repositories]]
github = "acme/widgets"
path = %q
base_ref = "HEAD"
[roles]
foreman = "foreman"
plan = "planner"
build = "builder"
verify = "verifier"
[agents.foreman]
runtime = "command"
prompt = "agents/foreman.md"
command = [%q]
[agents.planner]
runtime = "command"
prompt = "agents/planner.md"
command = [%q]
[agents.builder]
runtime = "command"
prompt = "agents/builder.md"
command = [%q]
[agents.verifier]
runtime = "command"
prompt = "agents/verifier.md"
command = [%q]
`, repository, coordinatorScript, agentScript, agentScript, agentScript)
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}
	internalServer := &server{}
	internalHTTP := httptest.NewServer(http.HandlerFunc(internalServer.handleInternal))
	t.Cleanup(internalHTTP.Close)
	configBody = strings.Replace(configBody, `listen = "127.0.0.1:0"`, fmt.Sprintf("listen = %q", strings.TrimPrefix(internalHTTP.URL, "http://")), 1)
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	state := newStore(cfg.Config.StateDirectory)
	const authToken = "test-runtime-authority"
	value := issue{Repository: "acme/widgets", Number: 1, Title: "Produce the correct result", Body: "The result must be correct.", URL: "https://github.com/acme/widgets/issues/1"}
	item, _, err := state.create(value)
	if err != nil {
		t.Fatal(err)
	}
	runner := agentRunner{config: cfg, store: state, github: fakeGitHub{issues: []issue{value}}, executable: binary, authToken: authToken}
	internalServer.runner = &runner
	if err := runner.runWork(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := state.get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != stateReady || completed.VerifyRuns != 2 {
		t.Fatalf("completed work = state %q, verify runs %d", completed.State, completed.VerifyRuns)
	}
	if completed.VerifiedSHA == "" || completed.VerifiedSHA != completed.HeadSHA {
		t.Fatalf("candidate SHA %q was not the verified SHA %q", completed.HeadSHA, completed.VerifiedSHA)
	}
	result, err := os.ReadFile(filepath.Join(completed.Workspace, "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "correct\n" {
		t.Fatalf("result.txt = %q", result)
	}
	if completed.PRURL != "" {
		t.Fatalf("dry run unexpectedly opened PR %q", completed.PRURL)
	}
	for _, artifact := range []string{"plan.md", "build.md", "review.md", "foreman.md"} {
		if _, err := state.readArtifact(item.ID, artifact); err != nil {
			t.Fatalf("missing %s: %v", artifact, err)
		}
	}
	for run := 1; run <= completed.VerifyRuns; run++ {
		verificationPath := filepath.Join(cfg.Config.StateDirectory, "checkouts", item.ID, "verify", fmt.Sprintf("attempt-1-run-%d", run))
		if _, err := os.Stat(verificationPath); !os.IsNotExist(err) {
			t.Fatalf("verification worktree %q was retained: %v", verificationPath, err)
		}
	}
	if _, err := state.update(item.ID, func(current *work) error {
		current.State = stateRunning
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, agentScript, `#!/bin/sh
set -eu
printf 'Malformed verification report.\n'
`)
	if _, err := runner.delegate(context.Background(), item.ID, "verify"); err == nil || !strings.Contains(err.Error(), "must begin") {
		t.Fatalf("malformed re-verification succeeded: %v", err)
	}
	afterFailedVerification, err := state.get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailedVerification.VerifiedSHA != "" {
		t.Fatalf("failed re-verification retained approval %q", afterFailedVerification.VerifiedSHA)
	}
	if _, err := state.update(item.ID, func(current *work) error {
		current.State = stateReady
		current.VerifyRuns = completed.VerifyRuns
		current.VerifiedSHA = completed.VerifiedSHA
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	verificationWorkspace, err := prepareVerificationWorkspace(context.Background(), cfg, completed, completed.HeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verificationWorkspace, "verifier-change.txt"), []byte("must not be accepted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, verificationWorkspace, "git", "add", "verifier-change.txt")
	mustRun(t, verificationWorkspace, "git", "-c", "user.name=Verifier", "-c", "user.email=verifier@example.com", "commit", "-m", "bad verifier change")
	if err := ensureExactHead(context.Background(), verificationWorkspace, completed.HeadSHA); err == nil || !strings.Contains(err.Error(), "HEAD changed") {
		t.Fatalf("accepted verifier commit: %v", err)
	}
	if err := removeVerificationWorkspace(context.Background(), cfg, completed, verificationWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := state.update(item.ID, func(current *work) error {
		current.State = stateRunning
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(completed.Workspace, "unverified.txt"), []byte("late change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runner.finish(context.Background(), item.ID); err == nil || !strings.Contains(err.Error(), "changed after verification") {
		t.Fatalf("finish accepted unverified worktree change: %v", err)
	}
	if _, err := state.update(item.ID, func(current *work) error {
		current.State = stateQueued
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runner.block(context.Background(), item.ID, "stale foreman"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("stale foreman changed queued work: %v", err)
	}
	if _, err := state.update(item.ID, func(current *work) error {
		current.State = stateFailed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	retried, changed, err := state.retry(item.ID, value)
	if err != nil || !changed {
		t.Fatalf("retry failed: changed=%t, err=%v", changed, err)
	}
	retryWorkspace, retryBranch, err := prepareWorkspace(context.Background(), cfg, retried)
	if err != nil {
		t.Fatal(err)
	}
	if retryWorkspace == completed.Workspace || retryBranch == completed.Branch {
		t.Fatalf("retry reused workspace %q or branch %q", retryWorkspace, retryBranch)
	}
	if _, err := os.Stat(filepath.Join(retryWorkspace, "result.txt")); !os.IsNotExist(err) {
		t.Fatalf("retry inherited result.txt: %v", err)
	}
	if reusedWorkspace, reusedBranch, err := prepareWorkspace(context.Background(), cfg, retried); err != nil || reusedWorkspace != retryWorkspace || reusedBranch != retryBranch {
		t.Fatalf("clean workspace was not safely reused: %q, %q, %v", reusedWorkspace, reusedBranch, err)
	}
	if err := os.WriteFile(filepath.Join(retryWorkspace, "stale.txt"), []byte("stale attempt state\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareWorkspace(context.Background(), cfg, retried); err == nil || !strings.Contains(err.Error(), "not safe to reuse") {
		t.Fatalf("dirty attempt workspace was reused: %v", err)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, directory, name string, args ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), name, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}
