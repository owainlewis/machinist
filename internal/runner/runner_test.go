package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

func TestEscapedDescendantHelper(t *testing.T) {
	if os.Getenv("MACHINIST_TEST_ESCAPED") != "1" {
		return
	}
	if os.Getenv("MACHINIST_TEST_HOLD_STDIN") != "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	if _, err := syscall.Setsid(); err != nil {
		os.Exit(2)
	}
	marker := os.Getenv("MACHINIST_TEST_MARKER")
	if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(3)
	}
	for {
		if _, err := os.Stdout.Write([]byte(".")); err != nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecuteStreamsPromptAndPersistsOrderedResult(t *testing.T) {
	repository := newGitRepository(t)
	dataDirectory := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	result, err := Execute(context.Background(), Options{
		Command:       helperAgent("echo", time.Second),
		Repository:    repository,
		DataDirectory: dataDirectory,
		Stdout:        &stdout,
		Stderr:        &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateSucceeded || result.ExitCode != 0 {
		t.Fatalf("result = %#v", result)
	}
	if stdout.String() != "complete prompt\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.String() != "helper stderr\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if result.Repository != repository {
		t.Fatalf("repository = %q", result.Repository)
	}
	runDirectory := filepath.Dir(result.EventsPath)
	for _, path := range []string{runDirectory, result.EventsPath, filepath.Join(runDirectory, "result.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s permissions = %o", path, info.Mode().Perm())
		}
	}

	resultBody, err := os.ReadFile(filepath.Join(runDirectory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Result
	if err := json.Unmarshal(resultBody, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ID != result.ID || persisted.State != StateSucceeded || persisted.CommandHash != "test-hash" {
		t.Fatalf("persisted result = %#v", persisted)
	}
	if result.DurationMillis < 0 || persisted.DurationMillis != result.DurationMillis || result.TokenUsage != nil || persisted.TokenUsage != nil {
		t.Fatalf("persisted metrics = result %#v, persisted %#v", result, persisted)
	}
	if bytes.Contains(resultBody, []byte(`"token_usage"`)) {
		t.Fatalf("missing token usage was serialized: %s", resultBody)
	}

	events := readEvents(t, result.EventsPath)
	if len(events) != 5 {
		t.Fatalf("events = %#v", events)
	}
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("event %d sequence = %d", index, event.Sequence)
		}
	}
	if got := outputFor(t, events, "stdout"); got != "complete prompt\n" {
		t.Fatalf("recorded stdout = %q", got)
	}
	if events[len(events)-1].Type != "run.completed" {
		t.Fatalf("last event = %#v", events[len(events)-1])
	}
}

func TestExecutePersistsOnlyExplicitValidTokenUsage(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
		want *int64
	}{
		{name: "reported", mode: "tokens", want: int64Pointer(4321)},
		{name: "reported zero", mode: "zero-tokens", want: int64Pointer(0)},
		{name: "invalid", mode: "invalid-tokens"},
		{name: "missing", mode: "echo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Execute(t.Context(), Options{
				Command:       helperAgent(test.mode, time.Second),
				Repository:    newGitRepository(t),
				DataDirectory: t.TempDir(),
				Stdout:        io.Discard,
				Stderr:        io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !equalInt64Pointers(result.TokenUsage, test.want) {
				t.Fatalf("token usage = %v, want %v", result.TokenUsage, test.want)
			}
			body, err := os.ReadFile(filepath.Join(filepath.Dir(result.EventsPath), "result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var persisted Result
			if err := json.Unmarshal(body, &persisted); err != nil {
				t.Fatal(err)
			}
			if !equalInt64Pointers(persisted.TokenUsage, test.want) {
				t.Fatalf("persisted token usage = %v, want %v", persisted.TokenUsage, test.want)
			}
			if test.want == nil && bytes.Contains(body, []byte(`"token_usage"`)) {
				t.Fatalf("unreported token usage was serialized: %s", body)
			}
		})
	}
}

func TestExecuteInjectsMachinistEnvironment(t *testing.T) {
	repository := newGitRepository(t)
	var stdout bytes.Buffer
	result, err := Execute(t.Context(), Options{
		Command:       helperAgent("environment", time.Second),
		Repository:    repository,
		DataDirectory: t.TempDir(),
		Stdout:        &stdout,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{result.ID, repository, filepath.Join(filepath.Dir(result.EventsPath), tokenUsageFileName)}, "\n") + "\n"
	if stdout.String() != want {
		t.Fatalf("executor environment = %q, want %q", stdout.String(), want)
	}
}

func TestExecuteUsesManagedRunIDAndRejectsUnsafeIDs(t *testing.T) {
	repository := newGitRepository(t)
	dataDirectory := t.TempDir()
	runID := "run_0123456789abcdef01234567"
	result, err := Execute(t.Context(), Options{
		RunID:         runID,
		Command:       helperAgent("echo", time.Second),
		Repository:    repository,
		DataDirectory: dataDirectory,
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	if err != nil || result.ID != runID {
		t.Fatalf("result = %#v, %v", result, err)
	}
	attempt, err := Execute(t.Context(), Options{
		RunID:         runID,
		ArtifactKey:   "lease_abcdef0123456789",
		Command:       helperAgent("echo", time.Second),
		Repository:    repository,
		DataDirectory: dataDirectory,
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	if err != nil || attempt.ID != runID || filepath.Base(filepath.Dir(attempt.EventsPath)) != "lease_abcdef0123456789" {
		t.Fatalf("attempt result = %#v, %v", attempt, err)
	}
	if _, err := Execute(t.Context(), Options{RunID: "../escape", Command: helperAgent("echo", time.Second), Repository: repository, DataDirectory: dataDirectory, Stdout: io.Discard, Stderr: io.Discard}); err == nil || !strings.Contains(err.Error(), "invalid run ID") {
		t.Fatalf("unsafe run ID error = %v", err)
	}
	if _, err := Execute(t.Context(), Options{RunID: runID, ArtifactKey: "../escape", Command: helperAgent("echo", time.Second), Repository: repository, DataDirectory: dataDirectory, Stdout: io.Discard, Stderr: io.Discard}); err == nil || !strings.Contains(err.Error(), "invalid artifact key") {
		t.Fatalf("unsafe artifact key error = %v", err)
	}
}

func TestExecuteUsesRepositoryAsWorkingDirectory(t *testing.T) {
	repository := newGitRepository(t)
	nested := filepath.Join(repository, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	agent := helperAgent("pwd", time.Second)
	result, err := Execute(context.Background(), Options{
		Command:       agent,
		Repository:    nested,
		DataDirectory: t.TempDir(),
		Stdout:        &stdout,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Repository != repository || strings.TrimSpace(stdout.String()) != repository {
		t.Fatalf("repository = %q, stdout = %q", result.Repository, stdout.String())
	}
}

func TestExecuteIgnoresInheritedRepositoryGitEnvironment(t *testing.T) {
	requested := newGitRepository(t)
	other := newGitRepository(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	var stdout bytes.Buffer
	result, err := Execute(context.Background(), Options{
		Command: config.ResolvedCommand{
			Name:       "plan",
			Command:    []string{"/bin/sh", "-c", "cat >/dev/null; git rev-parse --show-toplevel"},
			Prompt:     "complete prompt\n",
			Timeout:    time.Second,
			Definition: "/definition/config.toml",
			Hash:       "test-hash",
		},
		Repository:    requested,
		DataDirectory: t.TempDir(),
		Stdout:        &stdout,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Repository != requested || strings.TrimSpace(stdout.String()) != requested {
		t.Fatalf("repository = %q, stdout = %q", result.Repository, stdout.String())
	}
}

func TestExecuteFinishesWhenDescendantKeepsOutputPipesOpen(t *testing.T) {
	started := time.Now()
	result, err := Execute(context.Background(), Options{
		Command:       helperAgent("background", 10*time.Second),
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateSucceeded || time.Since(started) > 2*time.Second {
		t.Fatalf("result = %#v, duration = %s", result, time.Since(started))
	}
}

func TestExecuteFinishesWhenSessionDetachedDescendantKeepsPipesOpen(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "detached.pid")
	script := `cat >/dev/null; MACHINIST_TEST_ESCAPED=1 MACHINIST_TEST_MARKER="$2" "$1" -test.run=^TestEscapedDescendantHelper$ & while [ ! -f "$2" ]; do sleep 0.01; done; exit 0`
	agent := config.ResolvedCommand{
		Name:       "plan",
		Command:    []string{"/bin/sh", "-c", script, "machinist-helper", os.Args[0], marker},
		Prompt:     "complete prompt\n",
		Timeout:    5 * time.Second,
		Definition: "/definition/config.toml",
		Hash:       "test-hash",
	}
	started := time.Now()
	result, err := Execute(context.Background(), Options{
		Command:       agent,
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateSucceeded || time.Since(started) > 3*time.Second {
		t.Fatalf("result = %#v, duration = %s", result, time.Since(started))
	}
}

func TestExecuteFinishesWhenSessionDetachedDescendantKeepsAllPipesOpen(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "detached.pid")
	script := `MACHINIST_TEST_ESCAPED=1 MACHINIST_TEST_HOLD_STDIN=1 MACHINIST_TEST_MARKER="$2" "$1" -test.run=^TestEscapedDescendantHelper$ & while [ ! -f "$2" ]; do sleep 0.01; done; exit 0`
	agent := config.ResolvedCommand{
		Name:       "plan",
		Command:    []string{"/bin/sh", "-c", script, "machinist-helper", os.Args[0], marker},
		Prompt:     strings.Repeat("prompt", 40<<10),
		Timeout:    5 * time.Second,
		Definition: "/definition/config.toml",
		Hash:       "test-hash",
	}
	started := time.Now()
	result, err := Execute(context.Background(), Options{
		Command:       agent,
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.ExitCode != 1 || outcome.State != StateFailed {
		t.Fatalf("error = %#v", err)
	}
	if result.State != StateFailed || time.Since(started) > 3*time.Second {
		t.Fatalf("result = %#v, duration = %s", result, time.Since(started))
	}
}

func TestEventLogTruncatesRecordingWithoutFailing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := newEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	log.outputLimit = 5
	for _, chunk := range []string{"abc", "def", "ghi"} {
		if err := log.appendOutput("stdout", []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.close(); err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, path)
	if got := outputFor(t, events, "stdout"); got != "abcde" {
		t.Fatalf("recorded output = %q", got)
	}
	truncations := 0
	for _, event := range events {
		if event.Type == "process.output_truncated" {
			truncations++
		}
	}
	if truncations != 1 {
		t.Fatalf("truncation events = %d; events = %#v", truncations, events)
	}
}

func TestEventLogBoundsActualFileAcrossTinyChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := newEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	log.fileLimit = 4 << 10
	for range 1000 {
		if err := log.appendOutput("stdout", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > log.fileLimit {
		t.Fatalf("event file size = %d, limit = %d", info.Size(), log.fileLimit)
	}
	events := readEvents(t, path)
	if events[len(events)-1].Type != "process.output_truncated" {
		t.Fatalf("last event = %#v", events[len(events)-1])
	}
}

func TestWritePromptAcceptsSupervisorCloseAfterCompleteDelivery(t *testing.T) {
	result := make(chan error, 1)
	writePrompt(completeThenClosedWriter{}, "complete prompt\n", result)
	if err := <-result; err != nil {
		t.Fatalf("write prompt: %v", err)
	}
}

func TestExecuteSucceedsWhenAgentReadsExactPromptLength(t *testing.T) {
	result, err := Execute(context.Background(), Options{
		Command:       helperAgent("exact-prompt", time.Second),
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateSucceeded || result.ExitCode != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteStreamsAndRecordsOutputWithoutChangingBytes(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
		want string
	}{
		{name: "partial CRLF and unterminated", mode: "raw", want: "one\r\ntwo"},
		{name: "line larger than one MiB", mode: "large", want: strings.Repeat("x", 2<<20)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			result, err := Execute(context.Background(), Options{
				Command:       helperAgent(test.mode, 10*time.Second),
				Repository:    newGitRepository(t),
				DataDirectory: t.TempDir(),
				Stdout:        &stdout,
				Stderr:        io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			if stdout.String() != test.want {
				t.Fatalf("stdout length = %d, want %d", stdout.Len(), len(test.want))
			}
			if recorded := outputFor(t, readEvents(t, result.EventsPath), "stdout"); recorded != test.want {
				t.Fatalf("recorded output length = %d, want %d", len(recorded), len(test.want))
			}
		})
	}
}

func TestExecuteStreamsOutputBeforeAgentExits(t *testing.T) {
	writer := newSignalWriter()
	done := make(chan error, 1)
	repository := newGitRepository(t)
	dataDirectory := t.TempDir()
	go func() {
		_, err := Execute(context.Background(), Options{
			Command:       helperAgent("stream", 5*time.Second),
			Repository:    repository,
			DataDirectory: dataDirectory,
			Stdout:        writer,
			Stderr:        io.Discard,
		})
		done <- err
	}()

	select {
	case <-writer.firstWrite:
	case <-time.After(time.Second):
		t.Fatal("first output was not streamed promptly")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := writer.String(); got != "partialdone" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestExecuteTreatsOutputWriteFailureAsRuntimeFailure(t *testing.T) {
	result, err := Execute(context.Background(), Options{
		Command:       helperAgent("echo", time.Second),
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        errorWriter{err: syscall.EPIPE},
		Stderr:        io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.ExitCode != 1 || outcome.State != StateFailed {
		t.Fatalf("error = %#v", err)
	}
	assertPersistedState(t, result, StateFailed, 1)
}

func TestExecuteReturnsAgentExitStatusAndPersistsFailure(t *testing.T) {
	result, err := Execute(context.Background(), Options{
		Command:       helperAgent("fail", time.Second),
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.ExitCode != 7 || outcome.State != StateFailed {
		t.Fatalf("error = %#v", err)
	}
	if result.State != StateFailed || result.ExitCode != 7 {
		t.Fatalf("result = %#v", result)
	}
	assertPersistedState(t, result, StateFailed, 7)
}

func TestExecuteTimesOutAgent(t *testing.T) {
	started := time.Now()
	result, err := Execute(context.Background(), Options{
		Command:       helperAgent("sleep", 50*time.Millisecond),
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.ExitCode != 124 || outcome.State != StateTimedOut {
		t.Fatalf("error = %#v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("timeout did not stop the process promptly")
	}
	assertPersistedState(t, result, StateTimedOut, 124)
}

func TestExecuteCancelsAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	result, err := Execute(ctx, Options{
		Command:       helperAgent("sleep", time.Minute),
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.ExitCode != 130 || outcome.State != StateCancelled {
		t.Fatalf("error = %#v", err)
	}
	assertPersistedState(t, result, StateCancelled, 130)
}

func TestExecuteDoesNotStartAgentWhenAlreadyCancelled(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Execute(ctx, Options{
		Command: config.ResolvedCommand{
			Name:       "plan",
			Command:    []string{"/bin/sh", "-c", `touch "$1"`, "machinist-test", marker},
			Prompt:     "complete prompt\n",
			Timeout:    time.Second,
			Definition: "/definition/config.toml",
			Hash:       "test-hash",
		},
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.ExitCode != 130 || outcome.State != StateCancelled {
		t.Fatalf("error = %#v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent marker exists or could not be checked: %v", err)
	}
	assertPersistedState(t, result, StateCancelled, 130)
}

func TestExecuteTimeoutInterruptsBlockedOutputPipe(t *testing.T) {
	assertBlockedOutputStops(t, context.Background(), 100*time.Millisecond, StateTimedOut, 124)
}

func TestExecuteCancellationInterruptsBlockedOutputPipe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	assertBlockedOutputStops(t, ctx, time.Minute, StateCancelled, 130)
}

func TestExecuteRecordsCommandStartFailure(t *testing.T) {
	result, err := Execute(context.Background(), Options{
		Command: config.ResolvedCommand{
			Name:       "missing",
			Command:    []string{filepath.Join(t.TempDir(), "not-an-agent")},
			Prompt:     "prompt",
			Timeout:    time.Second,
			Definition: "config.toml",
			Hash:       "hash",
		},
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.ExitCode != 1 {
		t.Fatalf("error = %#v", err)
	}
	assertPersistedState(t, result, StateFailed, 1)
}

func TestResolveRepositoryReturnsRootAndRejectsNonGitDirectory(t *testing.T) {
	repository := newGitRepository(t)
	nested := filepath.Join(repository, "one", "two")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := ResolveRepository(nested)
	if err != nil {
		t.Fatal(err)
	}
	if root != repository {
		t.Fatalf("root = %q", root)
	}
	if _, err := ResolveRepository(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not inside a Git worktree") {
		t.Fatalf("expected Git error, got %v", err)
	}
}

func helperAgent(mode string, timeout time.Duration) config.ResolvedCommand {
	script := ""
	switch mode {
	case "echo":
		script = "cat; printf 'helper stderr\\n' >&2"
	case "fail":
		script = "exit 7"
	case "sleep":
		script = "sleep 60"
	case "pwd":
		script = "cat >/dev/null; pwd"
	case "background":
		script = "cat >/dev/null; sleep 60 & exit 0"
	case "raw":
		script = "cat >/dev/null; printf 'one\\r\\ntwo'"
	case "large":
		script = `cat >/dev/null; awk 'BEGIN { for (i = 0; i < 2097152; i++) printf "x" }'`
	case "stream":
		script = "cat >/dev/null; printf partial; sleep 1; printf done"
	case "flood":
		script = "cat >/dev/null; yes machinist"
	case "exact-prompt":
		script = "dd bs=16 count=1 of=/dev/null 2>/dev/null"
	case "environment":
		script = `cat >/dev/null; printf '%s\n%s\n%s\n' "$MACHINIST_RUN_ID" "$MACHINIST_REPOSITORY" "$MACHINIST_TOKEN_USAGE_PATH"`
	case "tokens":
		script = `cat >/dev/null; printf 4321 > "$MACHINIST_TOKEN_USAGE_PATH"`
	case "zero-tokens":
		script = `cat >/dev/null; printf 0 > "$MACHINIST_TOKEN_USAGE_PATH"`
	case "invalid-tokens":
		script = `cat >/dev/null; printf unknown > "$MACHINIST_TOKEN_USAGE_PATH"`
	default:
		panic("unknown helper mode")
	}
	return config.ResolvedCommand{
		Name:       "plan",
		Command:    []string{"/bin/sh", "-c", script},
		Prompt:     "complete prompt\n",
		Timeout:    timeout,
		Definition: "/definition/config.toml",
		Hash:       "test-hash",
	}
}

func int64Pointer(value int64) *int64 { return &value }

func equalInt64Pointers(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func assertBlockedOutputStops(t *testing.T, ctx context.Context, timeout time.Duration, wantState State, wantExitCode int) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	started := time.Now()
	result, err := Execute(ctx, Options{
		Command:       helperAgent("flood", timeout),
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        writer,
		Stderr:        io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.ExitCode != wantExitCode || outcome.State != wantState {
		t.Fatalf("error = %#v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("blocked output stopped after %s", time.Since(started))
	}
	assertPersistedState(t, result, wantState, wantExitCode)
}

func newGitRepository(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	command := exec.Command("git", "init", "--quiet", directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readEvents(t *testing.T, path string) []Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var events []Event
	for decoder.More() {
		var event Event
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func assertPersistedState(t *testing.T, result Result, state State, exitCode int) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(filepath.Dir(result.EventsPath), "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Result
	if err := json.Unmarshal(body, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.State != state || persisted.ExitCode != exitCode {
		t.Fatalf("persisted result = %#v", persisted)
	}
}

func outputFor(t *testing.T, events []Event, stream string) string {
	t.Helper()
	var output bytes.Buffer
	for _, event := range events {
		if event.Type != "process.output" || event.Stream != stream {
			continue
		}
		if event.Encoding != "base64" {
			t.Fatalf("output encoding = %q", event.Encoding)
		}
		chunk, err := base64.StdEncoding.DecodeString(event.Data)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(chunk)
	}
	return output.String()
}

type signalWriter struct {
	mu         sync.Mutex
	buffer     bytes.Buffer
	firstWrite chan struct{}
	once       sync.Once
}

func newSignalWriter() *signalWriter {
	return &signalWriter{firstWrite: make(chan struct{})}
}

func (writer *signalWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.once.Do(func() { close(writer.firstWrite) })
	return writer.buffer.Write(data)
}

func (writer *signalWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.buffer.String()
}

type errorWriter struct{ err error }

func (writer errorWriter) Write([]byte) (int, error) { return 0, writer.err }

type completeThenClosedWriter struct{}

func (completeThenClosedWriter) Write(data []byte) (int, error) { return len(data), nil }
func (completeThenClosedWriter) Close() error                   { return os.ErrClosed }
