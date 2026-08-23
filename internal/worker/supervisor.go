package worker

import (
	"bufio"
	"context"
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
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	supervisorCommand       = "__factory_worker_supervisor"
	supervisorReadyTimeout  = 10 * time.Second
	terminationGrace        = 5 * time.Second
	supervisorCleanupWait   = 5 * time.Second
	maxSupervisorLineBytes  = 1 << 20
	maxSupervisorErrorBytes = 64 << 10
)

type supervisorInit struct {
	Runtime           string `json:"runtime"`
	RuntimeExecutable string `json:"runtime_executable"`
	Worktree          string `json:"worktree"`
	ResultPath        string `json:"result_path"`
	Prompt            string `json:"prompt"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	RunID             string `json:"run_id,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	AttemptID         string `json:"attempt_id,omitempty"`
	UpdateSocket      string `json:"update_socket,omitempty"`
	UpdateToken       string `json:"update_token,omitempty"`
}

type supervisorMessage struct {
	Type            string `json:"type"`
	SupervisorPID   int64  `json:"supervisor_pid,omitempty"`
	ProcessIdentity string `json:"process_identity,omitempty"`
	ProcessGroupID  int64  `json:"process_group_id,omitempty"`
	GroupIdentity   string `json:"group_identity,omitempty"`
	Stream          string `json:"stream,omitempty"`
	Text            string `json:"text,omitempty"`
	ExitCode        int    `json:"exit_code,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Result          string `json:"result,omitempty"`
	Error           string `json:"error,omitempty"`
	Truncated       bool   `json:"truncated,omitempty"`
	StopUnverified  bool   `json:"stop_unverified,omitempty"`
}

type supervisorProcess struct {
	command            *exec.Cmd
	control            *os.File
	messages           chan supervisorMessage
	decodeErrors       chan error
	wait               chan error
	stderr             *limitBuffer
	controlMutex       sync.Mutex
	stateMutex         sync.Mutex
	stoppedVerified    bool
	supervisorPID      int64
	supervisorIdentity string
	processGroupID     int64
	groupIdentity      string
}

func IsSupervisorCommand(arguments []string) bool {
	return len(arguments) > 0 && arguments[0] == supervisorCommand
}

func RunSupervisor(control *os.File, input io.Reader, output, errorOutput io.Writer) error {
	if err := ensureSupportedPlatform(); err != nil {
		return err
	}
	if control == nil {
		return errors.New("supervisor control pipe is required")
	}
	var init supervisorInit
	decoder := json.NewDecoder(io.LimitReader(input, protocol.MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&init); err != nil {
		return fmt.Errorf("decode supervisor input: %w", err)
	}
	if init.Runtime == "" {
		init.Runtime = protocol.RuntimeCodex
	}
	if !protocol.SupportedRuntime(init.Runtime) || init.RuntimeExecutable == "" ||
		init.Worktree == "" || init.ResultPath == "" || init.TimeoutSeconds < 1 {
		return errors.New("supervisor input is incomplete")
	}
	if (init.RunID == "") != (init.SessionID == "") {
		return errors.New("supervisor Run and Session identities must be supplied together")
	}
	if (init.AttemptID == "") != (init.UpdateSocket == "") || (init.AttemptID == "") != (init.UpdateToken == "") {
		return errors.New("supervisor agent update context must be supplied together")
	}
	if init.TimeoutSeconds > int(protocol.MaxTimeout/time.Second) {
		return errors.New("supervisor timeout exceeds eight hours")
	}

	anchorToken, err := newUUID(nil)
	if err != nil {
		return fmt.Errorf("create process-group token: %w", err)
	}
	anchor := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 3600; done", "factory-anchor-"+anchorToken)
	anchor.Stdin = nil
	anchor.Stdout = nil
	anchor.Stderr = errorOutput
	configureNewProcessGroup(anchor)
	if err := anchor.Start(); err != nil {
		return fmt.Errorf("start process-group anchor: %w", err)
	}
	anchorPID := anchor.Process.Pid
	anchorIdentity, err := processIdentity(anchorPID)
	if err != nil {
		_ = anchor.Process.Kill()
		_ = anchor.Wait()
		return fmt.Errorf("establish process-group identity: %w", err)
	}
	groupID, err := processGroupID(anchorPID)
	if err != nil || groupID != anchorPID {
		_ = anchor.Process.Kill()
		_ = anchor.Wait()
		return errors.New("process-group anchor did not become its group leader")
	}
	supervisorIdentity, err := processIdentity(os.Getpid())
	if err != nil {
		_ = stopStartedSupervisorGroup(anchor, anchorIdentity, 0)
		_ = anchor.Wait()
		return fmt.Errorf("establish supervisor process identity: %w", err)
	}

	writer := &synchronizedEncoder{encoder: json.NewEncoder(output)}
	if err := writer.send(supervisorMessage{
		Type:            "ready",
		SupervisorPID:   int64(os.Getpid()),
		ProcessIdentity: supervisorIdentity,
		ProcessGroupID:  int64(groupID),
		GroupIdentity:   anchorIdentity,
	}); err != nil {
		_ = stopStartedSupervisorGroup(anchor, anchorIdentity, 0)
		_ = anchor.Wait()
		return fmt.Errorf("report supervisor readiness: %w", err)
	}

	commands := make(chan string, 8)
	controlErrors := make(chan error, 1)
	go readSupervisorControl(control, commands, controlErrors)
	leaseTimer := time.NewTimer(leaseStopDelay(protocol.LeaseDuration))
	defer leaseTimer.Stop()

	for {
		select {
		case command := <-commands:
			action, duration, parseErr := parseControlCommand(command)
			if parseErr != nil {
				_ = stopStartedSupervisorGroup(anchor, anchorIdentity, 0)
				_ = anchor.Wait()
				return parseErr
			}
			switch action {
			case "renew":
				resetTimer(leaseTimer, leaseStopDelay(duration))
			case "cancel":
				_ = stopStartedSupervisorGroup(anchor, anchorIdentity, terminationGrace)
				_ = anchor.Wait()
				return writer.send(supervisorMessage{Type: "exit", Reason: "cancelled", Error: "attempt cancelled"})
			case "lease_lost":
				_ = stopStartedSupervisorGroup(anchor, anchorIdentity, terminationGrace)
				_ = anchor.Wait()
				return writer.send(supervisorMessage{Type: "exit", Reason: "lease_lost", Error: "control-plane lease was lost"})
			case "fail":
				_ = stopStartedSupervisorGroup(anchor, anchorIdentity, terminationGrace)
				_ = anchor.Wait()
				return writer.send(supervisorMessage{Type: "exit", Reason: "supervisor_error", Error: "attempt preparation failed"})
			case "timeout":
				_ = stopStartedSupervisorGroup(anchor, anchorIdentity, terminationGrace)
				_ = anchor.Wait()
				return writer.send(supervisorMessage{Type: "exit", Reason: "timeout", Error: "Session timeout reached"})
			case "start":
				return superviseRuntime(init, anchor, anchorIdentity, groupID, commands, controlErrors, leaseTimer, writer)
			}
		case err := <-controlErrors:
			_ = stopStartedSupervisorGroup(anchor, anchorIdentity, terminationGrace)
			_ = anchor.Wait()
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("read supervisor control pipe: %w", err)
			}
			return nil
		case <-leaseTimer.C:
			_ = stopStartedSupervisorGroup(anchor, anchorIdentity, terminationGrace)
			_ = anchor.Wait()
			return writer.send(supervisorMessage{Type: "exit", Reason: "lease_lost", Error: "control-plane lease renewal deadline passed"})
		}
	}
}

func superviseRuntime(
	init supervisorInit,
	anchor *exec.Cmd,
	anchorIdentity string,
	groupID int,
	commands <-chan string,
	controlErrors <-chan error,
	leaseTimer *time.Timer,
	writer *synchronizedEncoder,
) error {
	var arguments []string
	var claudeResult *claudeResultCapture
	var piResult *plainResultCapture
	switch init.Runtime {
	case protocol.RuntimePi:
		arguments = []string{"--print", "--no-session"}
		piResult = &plainResultCapture{}
	case protocol.RuntimeCodex:
		arguments = []string{"exec", "--json", "--color", "never", "--output-last-message", init.ResultPath, "-"}
	case protocol.RuntimeClaudeCode:
		arguments = []string{
			"--print",
			"--output-format", "stream-json",
			"--verbose",
			"--permission-mode", "bypassPermissions",
		}
		claudeResult = &claudeResultCapture{}
	default:
		return finishSupervisorStartFailure(anchor, anchorIdentity, writer, errors.New("unsupported worker runtime"))
	}
	displayName := runtimeDisplayName(init.Runtime)
	command := exec.Command(init.RuntimeExecutable, arguments...)
	command.Dir = init.Worktree
	command.Env = runtimeEnvironment(init.RunID, init.SessionID, init.AttemptID, init.UpdateSocket, init.UpdateToken)
	configureExistingProcessGroup(command, groupID)
	stdin, err := command.StdinPipe()
	if err != nil {
		return finishSupervisorStartFailure(anchor, anchorIdentity, writer, fmt.Errorf("open %s stdin: %w", displayName, err))
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return finishSupervisorStartFailure(anchor, anchorIdentity, writer, fmt.Errorf("create %s stdout pipe: %w", displayName, err))
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		stdout.Close()
		stdoutWriter.Close()
		return finishSupervisorStartFailure(anchor, anchorIdentity, writer, fmt.Errorf("create %s stderr pipe: %w", displayName, err))
	}
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	if err := command.Start(); err != nil {
		stdout.Close()
		stdoutWriter.Close()
		stderr.Close()
		stderrWriter.Close()
		return finishSupervisorStartFailure(anchor, anchorIdentity, writer, fmt.Errorf("start %s: %w", displayName, err))
	}
	stdoutWriter.Close()
	stderrWriter.Close()
	defer stdout.Close()
	defer stderr.Close()
	promptErrors := make(chan error, 1)
	go func() {
		_, writeErr := io.WriteString(stdin, init.Prompt)
		closeErr := stdin.Close()
		promptErrors <- errors.Join(writeErr, closeErr)
	}()
	stderrTail := &tailBuffer{limit: protocol.MaxErrorBytes}
	var captureLine func([]byte, bool)
	if claudeResult != nil {
		captureLine = claudeResult.capture
	} else if piResult != nil {
		captureLine = piResult.capture
	}
	readers := sync.WaitGroup{}
	readers.Add(2)
	go func() {
		defer readers.Done()
		streamSupervisorOutput(stdout, "stdout", writer, nil, captureLine)
	}()
	go func() {
		defer readers.Done()
		streamSupervisorOutput(stderr, "stderr", writer, stderrTail, nil)
	}()
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()
	workTimer := time.NewTimer(time.Duration(init.TimeoutSeconds) * time.Second)
	defer workTimer.Stop()

	reason := "exited"
	var waitErr error
	running := true
	for running {
		select {
		case command := <-commands:
			action, duration, parseErr := parseControlCommand(command)
			if parseErr != nil {
				reason = "supervisor_error"
				waitErr = parseErr
				running = false
				continue
			}
			switch action {
			case "renew":
				resetTimer(leaseTimer, leaseStopDelay(duration))
			case "cancel":
				reason = "cancelled"
				running = false
			case "lease_lost":
				reason = "lease_lost"
				running = false
			case "fail":
				reason = "supervisor_error"
				running = false
			case "timeout":
				reason = "timeout"
				running = false
			case "outcome_reported":
				reason = supervisorOutcomeReason(leaseTimer.C)
				running = false
			case "start":
			}
		case controlErr := <-controlErrors:
			reason = "parent_lost"
			if controlErr != nil && !errors.Is(controlErr, io.EOF) {
				waitErr = controlErr
			}
			running = false
		case <-leaseTimer.C:
			reason = "lease_lost"
			running = false
		case <-workTimer.C:
			reason = "timeout"
			running = false
		case waitErr = <-wait:
			running = false
		}
	}

	grace := terminationGrace
	if reason == "exited" {
		grace = 0
	} else if reason == "outcome_reported" {
		grace = 10 * time.Second
	}
	stopErr := stopStartedSupervisorGroup(anchor, anchorIdentity, grace)
	var childErr error
	childReaped := reason == "exited"
	if reason != "exited" {
		select {
		case childErr = <-wait:
			childReaped = true
		case <-time.After(2 * time.Second):
			childErr = fmt.Errorf("%s process did not reap after group termination", displayName)
		}
	}
	anchorErr := waitForSupervisorCommand(anchor, supervisorCleanupWait)
	readersErr := waitForSupervisorReaders(&readers, stdout, stderr, supervisorCleanupWait)
	stopUnverified := stopErr != nil || !childReaped || anchorErr != nil || readersErr != nil
	waitErr = supervisorCompletionError(reason, waitErr, stopErr, childErr, anchorErr, readersErr)
	outcomeStopFailed := reason == "outcome_reported" && waitErr != nil
	if outcomeStopFailed {
		reason = "supervisor_error"
	}
	select {
	case promptErr := <-promptErrors:
		if promptErr != nil && !errors.Is(promptErr, os.ErrClosed) && reason == "exited" && waitErr == nil {
			waitErr = fmt.Errorf("send prompt to %s: %w", displayName, promptErr)
		}
	default:
	}

	var result string
	var truncated bool
	if piResult != nil {
		if piResult.result != nil {
			result = strings.TrimSpace(piResult.result.String())
			truncated = piResult.result.Truncated()
		}
		if result == "" && reason == "exited" && waitErr == nil {
			waitErr = errors.New("Pi returned no result")
		}
	} else if claudeResult != nil {
		result = claudeResult.result
		truncated = claudeResult.truncated
		if !claudeResult.found && reason == "exited" && waitErr == nil {
			waitErr = errors.New("Claude Code returned no terminal result event")
		} else if claudeResult.isError && reason == "exited" && waitErr == nil {
			waitErr = errors.New(firstNonEmpty(result, "Claude Code reported a terminal error"))
		}
	} else {
		var resultErr error
		result, truncated, resultErr = readBoundedText(init.ResultPath, protocol.MaxResultBytes)
		if resultErr != nil && reason == "exited" && waitErr == nil {
			waitErr = resultErr
		}
	}
	_ = os.Remove(init.ResultPath)
	message := supervisorMessage{
		Type:           "exit",
		ExitCode:       exitCode(waitErr),
		Reason:         reason,
		Result:         result,
		Truncated:      truncated,
		StopUnverified: stopUnverified,
	}
	if reason != "exited" && !outcomeStopFailed {
		message.Error = map[string]string{
			"cancelled":        "attempt cancelled",
			"parent_lost":      "worker parent process exited",
			"lease_lost":       "control-plane lease renewal deadline passed",
			"timeout":          "Session timeout reached",
			"supervisor_error": "attempt supervisor failed",
			"outcome_reported": "",
		}[reason]
	}
	if message.Error == "" && waitErr != nil {
		message.Error = boundedText(waitErr.Error(), protocol.MaxErrorBytes)
	}
	if tail := stderrTail.String(); tail != "" && message.ExitCode != 0 && reason == "exited" {
		if message.Error == "" {
			message.Error = boundedText(tail, protocol.MaxErrorBytes)
		} else {
			message.Error = boundedText(message.Error+": "+tail, protocol.MaxErrorBytes)
		}
	}
	return writer.send(message)
}

func supervisorOutcomeReason(leaseTimer <-chan time.Time) string {
	select {
	case <-leaseTimer:
		return "lease_lost"
	default:
		return "outcome_reported"
	}
}

func supervisorCompletionError(reason string, initial, stop, child, anchor, readers error) error {
	if reason == "outcome_reported" && stop == nil && expectedOutcomeTerminationError(child) {
		// A signal from the verified process-group stop is expected. All other
		// stop, reap, anchor, and reader errors remain terminal failures.
		child = nil
	}
	if anchor != nil {
		anchor = fmt.Errorf("reap process-group anchor: %w", anchor)
	}
	return errors.Join(initial, stop, child, anchor, readers)
}

func expectedOutcomeTerminationError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ProcessState != nil && exitErr.ExitCode() == -1
}

func runtimeEnvironment(runID, sessionID, attemptID, updateSocket, updateToken string) []string {
	environment := make([]string, 0, len(os.Environ())+6)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != "FACTORY_RUN_ID" && key != "FACTORY_SESSION_ID" &&
			key != "FACTORY_WORK_ID" && key != "FACTORY_ATTEMPT_ID" &&
			key != "FACTORY_UPDATE_SOCKET" && key != "FACTORY_UPDATE_TOKEN" {
			environment = append(environment, value)
		}
	}
	if runID != "" {
		environment = append(environment, "FACTORY_RUN_ID="+runID, "FACTORY_SESSION_ID="+sessionID)
	}
	if attemptID != "" {
		environment = append(environment,
			"FACTORY_WORK_ID="+sessionID,
			"FACTORY_ATTEMPT_ID="+attemptID,
			"FACTORY_UPDATE_SOCKET="+updateSocket,
			"FACTORY_UPDATE_TOKEN="+updateToken,
		)
	}
	return environment
}

type plainResultCapture struct {
	result *limitBuffer
}

func (capture *plainResultCapture) capture(fragment []byte, end bool) {
	if capture.result == nil {
		capture.result = newLimitBuffer(protocol.MaxResultBytes)
	}
	if len(fragment) > 0 {
		_, _ = capture.result.Write(fragment)
	}
	if end {
		_, _ = capture.result.Write([]byte{'\n'})
	}
}

func finishSupervisorStartFailure(anchor *exec.Cmd, identity string, writer *synchronizedEncoder, cause error) error {
	_ = stopStartedSupervisorGroup(anchor, identity, 0)
	_ = anchor.Wait()
	return writer.send(supervisorMessage{
		Type: "exit", ExitCode: -1, Reason: "supervisor_error", Error: boundedText(cause.Error(), protocol.MaxErrorBytes),
	})
}

func stopStartedSupervisorGroup(anchor *exec.Cmd, identity string, grace time.Duration) error {
	if anchor == nil || anchor.Process == nil {
		return errors.New("process-group anchor was not started")
	}
	groupID := anchor.Process.Pid
	ownedErr := stopOwnedProcessGroup(groupID, identity, grace)
	if ownedErr == nil {
		return nil
	}
	actualGroupID, groupErr := processGroupID(groupID)
	if groupErr != nil {
		return errors.Join(ownedErr, fmt.Errorf("verify started process group %d: %w", groupID, groupErr))
	}
	if actualGroupID != groupID {
		return errors.Join(ownedErr, fmt.Errorf("started process %d moved to group %d", groupID, actualGroupID))
	}
	if forceErr := forceStopStartedProcessGroup(groupID); forceErr != nil {
		return errors.Join(ownedErr, fmt.Errorf("force-stop started process group %d: %w", groupID, forceErr))
	}
	return nil
}

func waitForSupervisorCommand(command *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		killErr := command.Process.Kill()
		select {
		case waitErr := <-done:
			return errors.Join(fmt.Errorf("process did not exit within %s", timeout), killErr, waitErr)
		case <-time.After(timeout):
			return errors.Join(fmt.Errorf("process did not reap after forced termination"), killErr)
		}
	}
}

func waitForSupervisorReaders(readers *sync.WaitGroup, stdout, stderr *os.File, timeout time.Duration) error {
	done := make(chan struct{})
	go func() {
		readers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		closeErr := errors.Join(stdout.Close(), stderr.Close())
		select {
		case <-done:
			return errors.Join(fmt.Errorf("runtime output pipes remained open after process-group termination"), closeErr)
		case <-time.After(timeout):
			return errors.Join(fmt.Errorf("runtime output readers did not stop after their pipes were closed"), closeErr)
		}
	}
}

type synchronizedEncoder struct {
	mutex   sync.Mutex
	encoder *json.Encoder
}

func (writer *synchronizedEncoder) send(message supervisorMessage) error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.encoder.Encode(message)
}

func readSupervisorControl(control io.Reader, commands chan<- string, errorsChannel chan<- error) {
	scanner := bufio.NewScanner(control)
	for scanner.Scan() {
		commands <- strings.TrimSpace(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		errorsChannel <- err
	} else {
		errorsChannel <- io.EOF
	}
}

func parseControlCommand(command string) (string, time.Duration, error) {
	fields := strings.Fields(command)
	if len(fields) == 1 {
		switch fields[0] {
		case "start", "cancel", "lease_lost", "fail", "timeout", "outcome_reported":
			return fields[0], 0, nil
		}
	}
	if len(fields) == 2 && fields[0] == "renew" {
		milliseconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && milliseconds > 0 && milliseconds <= protocol.LeaseDuration.Milliseconds() {
			return "renew", time.Duration(milliseconds) * time.Millisecond, nil
		}
	}
	return "", 0, fmt.Errorf("unknown supervisor command %q", command)
}

func streamSupervisorOutput(
	reader io.Reader,
	stream string,
	writer *synchronizedEncoder,
	tail *tailBuffer,
	capture func([]byte, bool),
) {
	buffered := bufio.NewReaderSize(reader, 32<<10)
	line := make([]byte, 0, 32<<10)
	hadData := false
	truncated := false
	for {
		fragment, prefix, err := buffered.ReadLine()
		if len(fragment) > 0 {
			hadData = true
			if tail != nil {
				_, _ = tail.Write(fragment)
			}
			if capture != nil {
				capture(fragment, false)
			}
			remaining := maxSupervisorLineBytes - len(line)
			if len(fragment) > remaining {
				fragment = fragment[:remaining]
				truncated = true
			}
			line = append(line, fragment...)
		}
		if !prefix || err != nil {
			if hadData {
				if tail != nil {
					_, _ = tail.Write([]byte{'\n'})
				}
				if capture != nil {
					capture(nil, true)
				}
				_ = writer.send(supervisorMessage{
					Type: "output", Stream: stream, Text: string(line), Truncated: truncated,
				})
			}
			line = line[:0]
			hadData = false
			truncated = false
		}
		if err != nil {
			return
		}
	}
}

type claudeResultCapture struct {
	result    string
	found     bool
	isError   bool
	truncated bool
	line      claudeResultLineCapture
}

type claudeResultLineCapture struct {
	sanitized        []byte
	result           []byte
	invalid          bool
	depth            int
	expectTopKey     bool
	inString         bool
	stringEscaped    bool
	stringIsKey      bool
	keyRaw           []byte
	keyTooLarge      bool
	awaitingColon    bool
	candidateKey     string
	valueKey         string
	inResult         bool
	resultEscaped    bool
	unicodeDigits    int
	unicodeValue     rune
	pendingSurrogate rune
	resultSeen       bool
	resultTruncated  bool
}

func (capture *claudeResultCapture) capture(fragment []byte, end bool) {
	if len(fragment) > 0 {
		capture.line.write(fragment)
	}
	if !end {
		return
	}
	capture.finishLine()
}

func (capture *claudeResultCapture) finishLine() {
	line := &capture.line
	if line.inResult {
		line.invalid = true
	}
	var event struct {
		Type    string `json:"type"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if line.invalid || json.Unmarshal(line.sanitized, &event) != nil || event.Type != "result" {
		line.reset()
		return
	}
	result := event.Result
	truncated := false
	if line.resultSeen {
		result = strings.ToValidUTF8(string(line.result), "\uFFFD")
		truncated = line.resultTruncated || len(result) > protocol.MaxResultBytes
	}
	capture.result = boundedText(result, protocol.MaxResultBytes)
	capture.found = true
	capture.isError = event.IsError
	capture.truncated = truncated || len(event.Result) > protocol.MaxResultBytes
	line.reset()
}

func (line *claudeResultLineCapture) write(fragment []byte) {
	for _, value := range fragment {
		if line.inResult {
			line.writeResultByte(value)
			continue
		}
		line.appendSanitized(value)
		if line.inString {
			line.writeStringByte(value)
			continue
		}
		if value == ' ' || value == '\t' || value == '\r' {
			continue
		}
		if line.awaitingColon {
			line.awaitingColon = false
			if value == ':' {
				line.valueKey = line.candidateKey
				continue
			}
			line.valueKey = ""
		}
		if line.valueKey != "" {
			key := line.valueKey
			line.valueKey = ""
			if key == "result" {
				line.resultSeen = false
				line.result = line.result[:0]
				line.resultTruncated = false
				line.pendingSurrogate = 0
				if value == '"' {
					line.resultSeen = true
					line.inResult = true
					continue
				}
			}
		}
		switch value {
		case '"':
			line.inString = true
			line.stringEscaped = false
			line.stringIsKey = line.depth == 1 && line.expectTopKey
			line.keyRaw = line.keyRaw[:0]
			line.keyTooLarge = false
			line.expectTopKey = false
		case '{', '[':
			line.depth++
			if value == '{' && line.depth == 1 {
				line.expectTopKey = true
			}
		case '}', ']':
			line.depth--
		case ',':
			if line.depth == 1 {
				line.expectTopKey = true
			}
		}
	}
}

func (line *claudeResultLineCapture) appendSanitized(value byte) {
	if len(line.sanitized) == maxSupervisorLineBytes {
		line.invalid = true
		return
	}
	line.sanitized = append(line.sanitized, value)
}

func (line *claudeResultLineCapture) writeStringByte(value byte) {
	if line.stringIsKey && value != '"' {
		if len(line.keyRaw) < 256 {
			line.keyRaw = append(line.keyRaw, value)
		} else {
			line.keyTooLarge = true
		}
	}
	if line.stringEscaped {
		line.stringEscaped = false
		return
	}
	if value == '\\' {
		line.stringEscaped = true
		return
	}
	if value != '"' {
		return
	}
	line.inString = false
	if !line.stringIsKey || line.keyTooLarge {
		return
	}
	quoted := make([]byte, 0, len(line.keyRaw)+2)
	quoted = append(quoted, '"')
	quoted = append(quoted, line.keyRaw...)
	quoted = append(quoted, '"')
	var key string
	if json.Unmarshal(quoted, &key) == nil {
		line.candidateKey = key
		line.awaitingColon = true
	}
}

func (line *claudeResultLineCapture) writeResultByte(value byte) {
	if line.unicodeDigits > 0 {
		digit, ok := hexDigit(value)
		if !ok {
			line.invalid = true
			line.unicodeDigits = 0
			return
		}
		line.unicodeValue = line.unicodeValue<<4 | rune(digit)
		line.unicodeDigits--
		if line.unicodeDigits == 0 {
			line.appendUnicode(line.unicodeValue)
		}
		return
	}
	if line.resultEscaped {
		line.resultEscaped = false
		switch value {
		case '"', '\\', '/':
			line.flushPendingSurrogate()
			line.appendResult([]byte{value})
		case 'b':
			line.flushPendingSurrogate()
			line.appendResult([]byte{'\b'})
		case 'f':
			line.flushPendingSurrogate()
			line.appendResult([]byte{'\f'})
		case 'n':
			line.flushPendingSurrogate()
			line.appendResult([]byte{'\n'})
		case 'r':
			line.flushPendingSurrogate()
			line.appendResult([]byte{'\r'})
		case 't':
			line.flushPendingSurrogate()
			line.appendResult([]byte{'\t'})
		case 'u':
			line.unicodeDigits = 4
			line.unicodeValue = 0
		default:
			line.invalid = true
		}
		return
	}
	switch {
	case value == '"':
		line.flushPendingSurrogate()
		line.inResult = false
		line.appendSanitized(value)
	case value == '\\':
		line.resultEscaped = true
	case value < 0x20:
		line.invalid = true
	default:
		line.flushPendingSurrogate()
		line.appendResult([]byte{value})
	}
}

func (line *claudeResultLineCapture) appendUnicode(value rune) {
	switch {
	case value >= 0xD800 && value <= 0xDBFF:
		line.flushPendingSurrogate()
		line.pendingSurrogate = value
	case value >= 0xDC00 && value <= 0xDFFF && line.pendingSurrogate != 0:
		combined := 0x10000 + (line.pendingSurrogate-0xD800)<<10 + value - 0xDC00
		line.pendingSurrogate = 0
		line.appendRune(combined)
	case value >= 0xDC00 && value <= 0xDFFF:
		line.appendRune(utf8.RuneError)
	default:
		line.flushPendingSurrogate()
		line.appendRune(value)
	}
}

func (line *claudeResultLineCapture) flushPendingSurrogate() {
	if line.pendingSurrogate == 0 {
		return
	}
	line.pendingSurrogate = 0
	line.appendRune(utf8.RuneError)
}

func (line *claudeResultLineCapture) appendRune(value rune) {
	var encoded [utf8.UTFMax]byte
	count := utf8.EncodeRune(encoded[:], value)
	line.appendResult(encoded[:count])
}

func (line *claudeResultLineCapture) appendResult(value []byte) {
	if len(line.result) < protocol.MaxResultBytes+utf8.UTFMax {
		remaining := protocol.MaxResultBytes + utf8.UTFMax - len(line.result)
		if len(value) > remaining {
			value = value[:remaining]
		}
		line.result = append(line.result, value...)
	}
	if len(line.result) > protocol.MaxResultBytes {
		line.resultTruncated = true
	}
}

func (line *claudeResultLineCapture) reset() {
	sanitized := line.sanitized[:0]
	result := line.result[:0]
	*line = claudeResultLineCapture{sanitized: sanitized, result: result}
}

func hexDigit(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

type tailBuffer struct {
	mutex sync.Mutex
	bytes []byte
	limit int
}

func (buffer *tailBuffer) Write(value []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	buffer.bytes = append(buffer.bytes, value...)
	if len(buffer.bytes) > buffer.limit {
		buffer.bytes = append([]byte(nil), buffer.bytes[len(buffer.bytes)-buffer.limit:]...)
	}
	return len(value), nil
}

func (buffer *tailBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return boundedText(string(buffer.bytes), buffer.limit)
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func leaseStopDelay(remaining time.Duration) time.Duration {
	delay := remaining - terminationGrace
	if delay < time.Millisecond {
		return time.Millisecond
	}
	return delay
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

func readBoundedText(path string, limit int) (string, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read Codex result: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return "", false, fmt.Errorf("read Codex result: %w", err)
	}
	truncated := len(body) > limit
	if truncated {
		body = body[:limit]
	}
	return boundedText(string(body), limit), truncated, nil
}

func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func startSupervisor(commandLine []string, init supervisorInit, errorOutput io.Writer) (*supervisorProcess, error) {
	if len(commandLine) == 0 {
		return nil, errors.New("supervisor command is empty")
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create supervisor control pipe: %w", err)
	}
	command := exec.Command(commandLine[0], commandLine[1:]...)
	configureNewProcessGroup(command)
	command.ExtraFiles = []*os.File{controlRead}
	stdin, err := command.StdinPipe()
	if err != nil {
		controlRead.Close()
		controlWrite.Close()
		return nil, fmt.Errorf("open supervisor input: %w", err)
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		controlRead.Close()
		controlWrite.Close()
		return nil, fmt.Errorf("create supervisor output pipe: %w", err)
	}
	command.Stdout = stdoutWriter
	stderr := newLimitBuffer(maxSupervisorErrorBytes)
	command.Stderr = io.MultiWriter(errorOutput, stderr)
	if err := command.Start(); err != nil {
		controlRead.Close()
		controlWrite.Close()
		stdout.Close()
		stdoutWriter.Close()
		return nil, fmt.Errorf("start attempt supervisor: %w", err)
	}
	controlRead.Close()
	stdoutWriter.Close()
	process := &supervisorProcess{
		command:      command,
		control:      controlWrite,
		messages:     make(chan supervisorMessage, 128),
		decodeErrors: make(chan error, 1),
		wait:         make(chan error, 1),
		stderr:       stderr,
	}
	go func() {
		encoder := json.NewEncoder(stdin)
		err := encoder.Encode(init)
		closeErr := stdin.Close()
		if err != nil {
			process.decodeErrors <- errors.Join(err, closeErr)
		}
	}()
	go func() {
		defer stdout.Close()
		// Keep draining the supervisor protocol for the complete attempt. Progress
		// is bounded downstream, while stopping reads here could block the
		// supervisor before it reports or reaps a terminal process.
		decoder := json.NewDecoder(stdout)
		for {
			var message supervisorMessage
			if err := decoder.Decode(&message); err != nil {
				process.decodeErrors <- err
				return
			}
			process.messages <- message
		}
	}()
	go func() {
		process.wait <- command.Wait()
	}()
	return process, nil
}

func (process *supervisorProcess) awaitReady(ctx context.Context) error {
	timer := time.NewTimer(supervisorReadyTimeout)
	defer timer.Stop()
	select {
	case message := <-process.messages:
		if message.Type != "ready" || message.SupervisorPID <= 0 || message.ProcessGroupID <= 0 ||
			message.ProcessIdentity == "" || message.GroupIdentity == "" {
			return errors.New("attempt supervisor returned invalid readiness data")
		}
		process.supervisorPID = message.SupervisorPID
		process.supervisorIdentity = message.ProcessIdentity
		process.processGroupID = message.ProcessGroupID
		process.groupIdentity = message.GroupIdentity
		return nil
	case err := <-process.decodeErrors:
		return fmt.Errorf("read attempt supervisor readiness: %w: %s", err, process.stderr.String())
	case err := <-process.wait:
		return fmt.Errorf("attempt supervisor exited before readiness: %w: %s", err, process.stderr.String())
	case <-timer.C:
		return errors.New("attempt supervisor did not become ready within ten seconds")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (process *supervisorProcess) send(command string) error {
	process.controlMutex.Lock()
	defer process.controlMutex.Unlock()
	if process.control == nil {
		return errors.New("attempt supervisor control pipe is closed")
	}
	_, err := io.WriteString(process.control, command+"\n")
	return err
}

func (process *supervisorProcess) renew(expiry time.Time) error {
	remaining := time.Until(expiry)
	if remaining <= 0 {
		return process.send("lease_lost")
	}
	if remaining > protocol.LeaseDuration {
		remaining = protocol.LeaseDuration
	}
	milliseconds := remaining.Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}
	return process.send("renew " + strconv.FormatInt(milliseconds, 10))
}

func (process *supervisorProcess) closeControl() error {
	process.controlMutex.Lock()
	defer process.controlMutex.Unlock()
	if process.control == nil {
		return nil
	}
	err := process.control.Close()
	process.control = nil
	return err
}

func (process *supervisorProcess) markStopped() {
	process.stateMutex.Lock()
	process.stoppedVerified = true
	process.stateMutex.Unlock()
}

func (process *supervisorProcess) isStopped() bool {
	process.stateMutex.Lock()
	defer process.stateMutex.Unlock()
	return process.stoppedVerified
}

func supervisorCommandLine(executable string) []string {
	return []string{executable, supervisorCommand}
}

func resultPath(dataDirectory, attemptID string) (string, error) {
	directory := filepath.Join(dataDirectory, "tmp")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create worker temporary directory: %w", err)
	}
	if info, err := os.Lstat(directory); err != nil {
		return "", fmt.Errorf("inspect worker temporary directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("worker temporary directory must be a real directory, not a symlink")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("protect worker temporary directory: %w", err)
	}
	if !uuidPattern.MatchString(attemptID) {
		return "", errors.New("invalid attempt ID for result path")
	}
	path := filepath.Join(directory, attemptID+"-result")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create Codex result file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close Codex result file: %w", err)
	}
	return path, nil
}

func supervisorStartRequest(process *supervisorProcess, token string) protocol.StartAttemptRequest {
	supervisorPID := process.supervisorPID
	processGroupID := process.processGroupID
	return protocol.StartAttemptRequest{
		LeaseToken:      token,
		SupervisorPID:   &supervisorPID,
		ProcessIdentity: process.supervisorIdentity,
		ProcessGroupID:  &processGroupID,
	}
}

func processSummary(process *supervisorProcess) string {
	return "supervisor=" + strconv.FormatInt(process.supervisorPID, 10) +
		" process_group=" + strconv.FormatInt(process.processGroupID, 10)
}
