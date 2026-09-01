package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

const (
	outputChunkBytes       = 32 << 10
	outputDrainGrace       = time.Second
	tokenUsageFileName     = "token_usage"
	tokenUsageEnvironment  = "MACHINIST_TOKEN_USAGE_PATH"
	maxTokenUsageFileBytes = 64
)

type State string

const (
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	StateTimedOut  State = "timed_out"
)

type Options struct {
	RunID         string
	ArtifactKey   string
	Command       config.ResolvedCommand
	Repository    string
	DataDirectory string
	Stdout        io.Writer
	Stderr        io.Writer
}

type Result struct {
	ID             string    `json:"id"`
	Command        string    `json:"command"`
	CommandHash    string    `json:"command_hash"`
	Definition     string    `json:"definition"`
	Repository     string    `json:"repository"`
	State          State     `json:"state"`
	ExitCode       int       `json:"exit_code"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    time.Time `json:"completed_at"`
	DurationMillis int64     `json:"duration_millis"`
	TokenUsage     *int64    `json:"token_usage,omitempty"`
	EventsPath     string    `json:"events_path"`
}

type OutcomeError struct {
	State    State
	ExitCode int
	Cause    error
}

func (err *OutcomeError) Error() string {
	if err.Cause != nil {
		return err.Cause.Error()
	}
	return string(err.State)
}

func (err *OutcomeError) Unwrap() error { return err.Cause }

type RuntimeError struct {
	Cause error
}

func (err *RuntimeError) Error() string { return err.Cause.Error() }
func (err *RuntimeError) Unwrap() error { return err.Cause }

type processWait struct {
	state *os.ProcessState
	err   error
}

func Execute(ctx context.Context, options Options) (result Result, returnErr error) {
	if options.Stdout == nil || options.Stderr == nil {
		return Result{}, errors.New("stdout and stderr are required")
	}
	repository, err := ResolveRepository(options.Repository)
	if err != nil {
		return Result{}, err
	}
	runID := options.RunID
	if runID == "" {
		runID, err = randomID("run", 12)
		if err != nil {
			return Result{}, &RuntimeError{Cause: err}
		}
	} else if !validRunID(runID) {
		return Result{}, fmt.Errorf("invalid run ID %q", runID)
	}
	runDirectory := filepath.Join(options.DataDirectory, "runs", runID)
	if options.ArtifactKey != "" {
		if !validArtifactKey(options.ArtifactKey) {
			return Result{}, fmt.Errorf("invalid artifact key %q", options.ArtifactKey)
		}
		runDirectory = filepath.Join(runDirectory, options.ArtifactKey)
	}
	if err := createDurableDirectory(runDirectory); err != nil {
		return Result{}, &RuntimeError{Cause: fmt.Errorf("create run directory: %w", err)}
	}
	eventsPath := filepath.Join(runDirectory, "events.jsonl")
	log, err := newEventLog(eventsPath)
	if err != nil {
		return Result{}, &RuntimeError{Cause: err}
	}
	defer func() {
		if err := log.close(); err != nil {
			returnErr = addRuntimeError(returnErr, err)
		}
	}()

	startedAt := time.Now().UTC()
	result = Result{
		ID:          runID,
		Command:     options.Command.Name,
		CommandHash: options.Command.Hash,
		Definition:  options.Command.Definition,
		Repository:  repository,
		State:       StateFailed,
		ExitCode:    1,
		StartedAt:   startedAt,
		EventsPath:  eventsPath,
	}
	if err := log.append("run.started", "", fmt.Sprintf("command=%s repository=%s", options.Command.Name, repository)); err != nil {
		return result, &RuntimeError{Cause: err}
	}
	tokenUsagePath := filepath.Join(runDirectory, tokenUsageFileName)
	if err := os.Remove(tokenUsagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return completeFailure(&result, log, runDirectory, fmt.Errorf("reset executor token usage report: %w", err))
	}

	executorCommand := structuredCommand(options.Command.Executor, options.Command.Command)
	command := exec.Command(executorCommand[0], executorCommand[1:]...)
	command.Dir = repository
	command.Env = append(sanitizedEnvironment(os.Environ()), "MACHINIST_RUN_ID="+runID, "MACHINIST_REPOSITORY="+repository, tokenUsageEnvironment+"="+tokenUsagePath)
	configureProcess(command)

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		return completeFailure(&result, log, runDirectory, fmt.Errorf("create command stdin: %w", err))
	}
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		closeFiles(stdinReader, stdinWriter)
		return completeFailure(&result, log, runDirectory, fmt.Errorf("create command stdout: %w", err))
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		closeFiles(stdinReader, stdinWriter, stdoutReader, stdoutWriter)
		return completeFailure(&result, log, runDirectory, fmt.Errorf("create command stderr: %w", err))
	}
	command.Stdin = stdinReader
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	if ctx.Err() != nil {
		closeFiles(stdinReader, stdinWriter, stdoutReader, stdoutWriter, stderrReader, stderrWriter)
		return completeOutcome(&result, log, runDirectory, StateCancelled, 130, errors.New("run cancelled"))
	}

	if err := command.Start(); err != nil {
		closeFiles(stdinReader, stdinWriter, stdoutReader, stdoutWriter, stderrReader, stderrWriter)
		return completeFailure(&result, log, runDirectory, fmt.Errorf("start command %q: %w", executorCommand[0], err))
	}
	closeFiles(stdinReader, stdoutWriter, stderrWriter)
	if err := log.append("process.started", "", fmt.Sprintf("pid=%d", command.Process.Pid)); err != nil {
		_ = stdinWriter.Close()
		_ = terminateProcessTree(command.Process)
		_, _ = command.Process.Wait()
		closeFiles(stdoutReader, stderrReader)
		return completeFailure(&result, log, runDirectory, err)
	}

	inputResult := make(chan error, 1)
	go writePrompt(stdinWriter, options.Command.Prompt, inputResult)
	streamErrors := make(chan error, 2)
	var streams sync.WaitGroup
	streams.Add(2)
	usageCollector := newUsageCollector(options.Command.Executor, executorCommand)
	stdoutDestination := options.Stdout
	if usageCollector != nil {
		stdoutDestination = io.MultiWriter(options.Stdout, usageCollector)
	}
	go pumpStream(&streams, stdoutReader, stdoutDestination, "stdout", log, streamErrors)
	go pumpStream(&streams, stderrReader, options.Stderr, "stderr", log, streamErrors)
	streamsDone := make(chan struct{})
	go func() {
		streams.Wait()
		close(streamsDone)
	}()
	processResult := make(chan processWait, 1)
	go func() {
		state, err := command.Process.Wait()
		processResult <- processWait{state: state, err: err}
	}()

	closeInput := func() { _ = stdinWriter.Close() }
	closeStreams := func() { closeFiles(stdoutReader, stderrReader) }
	state, exitCode, outcome := supervise(ctx, command.Process, options.Command.Timeout, processResult, inputResult, streamErrors, streamsDone, closeInput, closeStreams, options.Stdout, options.Stderr)
	var collectedTokenUsage *int64
	collectedTokenUsageIsAuthoritative := usageCollector != nil
	if usageCollector != nil {
		collectedTokenUsage = usageCollector.tokenUsage()
	}
	if err := finish(&result, log, runDirectory, state, exitCode, outcome, collectedTokenUsage, collectedTokenUsageIsAuthoritative); err != nil {
		if outcome != nil {
			return result, &OutcomeError{State: state, ExitCode: exitCode, Cause: errors.Join(outcome, err)}
		}
		return result, &RuntimeError{Cause: err}
	}
	if outcome != nil {
		return result, &OutcomeError{State: state, ExitCode: exitCode, Cause: outcome}
	}
	return result, nil
}

func validRunID(value string) bool {
	if len(value) != len("run_")+24 || !strings.HasPrefix(value, "run_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "run_"))
	return err == nil
}

func validArtifactKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func supervise(ctx context.Context, process *os.Process, timeout time.Duration, processResult <-chan processWait, inputResult <-chan error, streamErrors <-chan error, streamsDone <-chan struct{}, closeInput, closeStreams func(), outputWriters ...io.Writer) (State, int, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	timeoutAt := time.Now().Add(timeout)
	setOutputDeadline(timeoutAt, outputWriters...)
	inputDone := false
	var inputErr error
	// abort stops the process immediately, releases every pipe, and returns the
	// process result so callers can decide how to report the failure.
	abort := func() processWait {
		setOutputDeadline(time.Now(), outputWriters...)
		_ = terminateProcessTree(process)
		closeInput()
		closeStreams()
		return <-processResult
	}

	for {
		select {
		case waited := <-processResult:
			killErr := terminateProcessTree(process)
			closeInput()
			drainDeadline := earlierDeadline(timeoutAt, time.Now().Add(outputDrainGrace))
			setOutputDeadline(drainDeadline, outputWriters...)
			state, exitCode, outcome := processOutcome(waited)
			if outcome == nil && killErr != nil {
				state, exitCode, outcome = StateFailed, 1, fmt.Errorf("stop remaining command processes: %w", killErr)
			}
			inputErr = awaitInput(inputResult, inputDone, inputErr)
			waitForStreams(streamsDone, closeStreams, time.Until(drainDeadline))
			if outcome == nil && inputErr != nil {
				state, exitCode, outcome = StateFailed, 1, inputErr
			}
			if outcome == nil {
				if streamErr := receiveError(streamErrors); streamErr != nil {
					state, exitCode, outcome = StateFailed, 1, streamErr
				}
			}
			return state, exitCode, outcome
		case err := <-inputResult:
			inputDone = true
			inputErr = err
			if err != nil {
				waited := abort()
				<-streamsDone
				return operationFailure(waited, err)
			}
		case err := <-streamErrors:
			waited := abort()
			inputErr = awaitInput(inputResult, inputDone, inputErr)
			<-streamsDone
			return operationFailure(waited, errors.Join(err, inputErr))
		case <-ctx.Done():
			abort()
			_ = awaitInput(inputResult, inputDone, inputErr)
			<-streamsDone
			return StateCancelled, 130, errors.New("run cancelled")
		case <-timer.C:
			abort()
			_ = awaitInput(inputResult, inputDone, inputErr)
			<-streamsDone
			return StateTimedOut, 124, fmt.Errorf("command exceeded timeout %s", timeout)
		}
	}
}

func waitForStreams(done <-chan struct{}, closeStreams func(), grace time.Duration) {
	if grace <= 0 {
		closeStreams()
		<-done
		return
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		closeStreams()
		<-done
	}
}

type writeDeadliner interface {
	SetWriteDeadline(time.Time) error
}

func setOutputDeadline(deadline time.Time, writers ...io.Writer) {
	for _, writer := range writers {
		if deadliner, ok := writer.(writeDeadliner); ok {
			_ = deadliner.SetWriteDeadline(deadline)
		}
	}
}

func earlierDeadline(first, second time.Time) time.Time {
	if first.Before(second) {
		return first
	}
	return second
}

func operationFailure(waited processWait, failure error) (State, int, error) {
	state, exitCode, processErr := processOutcome(waited)
	if processErr != nil && exitCode != terminatedExitCode() {
		return state, exitCode, errors.Join(processErr, failure)
	}
	return StateFailed, 1, failure
}

func ResolveRepository(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = "."
	}
	absPath, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("inspect repository path %q: %w", absPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository path %q is not a directory", absPath)
	}
	command := exec.Command("git", "-C", absPath, "rev-parse", "--show-toplevel")
	command.Env = sanitizedEnvironment(os.Environ())
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("repository path %q is not inside a Git worktree", absPath)
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", fmt.Errorf("Git returned an empty repository root for %q", absPath)
	}
	return filepath.Clean(root), nil
}

func pumpStream(group *sync.WaitGroup, source *os.File, destination io.Writer, stream string, log *eventLog, errorsChannel chan<- error) {
	defer group.Done()
	defer source.Close()
	buffer := make([]byte, outputChunkBytes)
	for {
		count, readErr := source.Read(buffer)
		if count > 0 {
			chunk := buffer[:count]
			written, writeErr := destination.Write(chunk)
			if writeErr == nil && written != len(chunk) {
				writeErr = io.ErrShortWrite
			}
			if writeErr != nil {
				sendError(errorsChannel, fmt.Errorf("write %s: %w", stream, writeErr))
				return
			}
			if err := log.appendOutput(stream, chunk); err != nil {
				sendError(errorsChannel, err)
				return
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, os.ErrClosed) {
				sendError(errorsChannel, fmt.Errorf("read %s: %w", stream, readErr))
			}
			return
		}
	}
}

func writePrompt(destination io.WriteCloser, prompt string, result chan<- error) {
	written, writeErr := io.WriteString(destination, prompt)
	if writeErr == nil && written != len(prompt) {
		writeErr = io.ErrShortWrite
	}
	closeErr := destination.Close()
	if written == len(prompt) && errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	result <- errors.Join(writeErr, closeErr)
}

func processOutcome(waited processWait) (State, int, error) {
	if waited.err != nil {
		return StateFailed, 1, fmt.Errorf("wait for command: %w", waited.err)
	}
	if waited.state.Success() {
		return StateSucceeded, 0, nil
	}
	exitCode := processExitCode(waited.state)
	return StateFailed, exitCode, fmt.Errorf("command exited with status %d", exitCode)
}

func completeFailure(result *Result, log *eventLog, runDirectory string, outcome error) (Result, error) {
	return completeOutcome(result, log, runDirectory, StateFailed, 1, outcome)
}

func completeOutcome(result *Result, log *eventLog, runDirectory string, state State, exitCode int, outcome error) (Result, error) {
	if err := finish(result, log, runDirectory, state, exitCode, outcome, nil, false); err != nil {
		outcome = errors.Join(outcome, err)
	}
	return *result, &OutcomeError{State: state, ExitCode: exitCode, Cause: outcome}
}

func finish(result *Result, log *eventLog, runDirectory string, state State, exitCode int, outcome error, collectedTokenUsage *int64, collectedTokenUsageIsAuthoritative bool) error {
	message := ""
	if outcome != nil {
		message = outcome.Error()
	}
	if err := log.append("run.completed", "", fmt.Sprintf("state=%s exit_code=%d message=%s", state, exitCode, message)); err != nil {
		return err
	}
	if err := log.sync(); err != nil {
		return err
	}
	completedAt := time.Now().UTC()
	result.State = state
	result.ExitCode = exitCode
	result.CompletedAt = completedAt
	result.DurationMillis = completedAt.Sub(result.StartedAt).Milliseconds()
	if collectedTokenUsageIsAuthoritative {
		result.TokenUsage = collectedTokenUsage
	} else {
		result.TokenUsage = readTokenUsage(filepath.Join(runDirectory, tokenUsageFileName))
	}
	if err := writeResult(filepath.Join(runDirectory, "result.json"), *result); err != nil {
		return err
	}
	return nil
}

func readTokenUsage(path string) *int64 {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxTokenUsageFileBytes+1))
	if err != nil || len(body) > maxTokenUsageFileBytes {
		return nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil || value < 0 {
		return nil
	}
	return &value
}

func writeResult(path string, result Result) error {
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run result: %w", err)
	}
	body = append(body, '\n')
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create run result: %w", err)
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("write run result: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync run result: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close run result: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish run result: %w", err)
	}
	removeTemporary = false
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %q for sync: %w", path, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync directory %q: %w", path, err)
	}
	return nil
}

func createDurableDirectory(path string) error {
	path = filepath.Clean(path)
	missing := make([]string, 0, 3)
	current := path
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("path %q is not a directory", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect directory %q: %w", current, err)
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("find existing parent for %q", path)
		}
		current = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		directory := missing[index]
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create directory %q: %w", directory, err)
		}
		info, err := os.Stat(directory)
		if err != nil {
			return fmt.Errorf("inspect created directory %q: %w", directory, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("path %q is not a directory", directory)
		}
		if err := syncDirectory(filepath.Dir(directory)); err != nil {
			return err
		}
	}
	return nil
}

func sanitizedEnvironment(environ []string) []string {
	clean := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if isRepositoryGitEnvironment(name) {
			continue
		}
		clean = append(clean, entry)
	}
	return clean
}

func isRepositoryGitEnvironment(name string) bool {
	switch name {
	case "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_CEILING_DIRECTORIES",
		"GIT_COMMON_DIR",
		"GIT_CONFIG",
		"GIT_CONFIG_COUNT",
		"GIT_CONFIG_PARAMETERS",
		"GIT_DIR",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM",
		"GIT_GRAFT_FILE",
		"GIT_IMPLICIT_WORK_TREE",
		"GIT_INDEX_FILE",
		"GIT_INTERNAL_SUPER_PREFIX",
		"GIT_NAMESPACE",
		"GIT_NO_REPLACE_OBJECTS",
		"GIT_OBJECT_DIRECTORY",
		"GIT_PREFIX",
		"GIT_REPLACE_REF_BASE",
		"GIT_SHALLOW_FILE",
		"GIT_WORK_TREE":
		return true
	}
	return strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}

func awaitInput(inputResult <-chan error, done bool, known error) error {
	if done {
		return known
	}
	return <-inputResult
}

func receiveError(channel <-chan error) error {
	select {
	case err := <-channel:
		return err
	default:
		return nil
	}
}

func sendError(channel chan<- error, err error) {
	select {
	case channel <- err:
	default:
	}
}

func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func addRuntimeError(existing, additional error) error {
	if existing == nil {
		return &RuntimeError{Cause: additional}
	}
	var outcome *OutcomeError
	if errors.As(existing, &outcome) {
		return &OutcomeError{State: outcome.State, ExitCode: outcome.ExitCode, Cause: errors.Join(outcome.Cause, additional)}
	}
	var runtime *RuntimeError
	if errors.As(existing, &runtime) {
		return &RuntimeError{Cause: errors.Join(runtime.Cause, additional)}
	}
	return errors.Join(existing, additional)
}

func randomID(prefix string, bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate %s ID: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(buffer), nil
}
