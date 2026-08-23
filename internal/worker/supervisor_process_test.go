//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package worker

import (
	"bytes"
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

	"github.com/owainlewis/factory/internal/protocol"
)

// supervisorSession drives a real RunSupervisor over its control pipe and
// collects the protocol messages it writes.
type supervisorSession struct {
	t        *testing.T
	control  *os.File
	output   *os.File
	decoded  chan supervisorMessage
	result   chan error
	ready    supervisorMessage
	finished bool
}

func startSupervisorSession(t *testing.T, init supervisorInit) *supervisorSession {
	t.Helper()
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(init)
	if err != nil {
		t.Fatal(err)
	}
	session := &supervisorSession{
		t:       t,
		control: controlWrite,
		output:  outputWrite,
		decoded: make(chan supervisorMessage, 1024),
		result:  make(chan error, 1),
	}
	go func() {
		session.result <- RunSupervisor(controlRead, bytes.NewReader(input), outputWrite,
			newLimitBuffer(maxSupervisorErrorBytes))
		controlRead.Close()
	}()
	go func() {
		defer close(session.decoded)
		defer outputRead.Close()
		decoder := json.NewDecoder(outputRead)
		for {
			var message supervisorMessage
			if err := decoder.Decode(&message); err != nil {
				return
			}
			session.decoded <- message
		}
	}()
	t.Cleanup(func() {
		_ = controlWrite.Close()
		if !session.finished {
			select {
			case <-session.result:
			case <-time.After(30 * time.Second):
				t.Error("supervisor did not exit after its control pipe closed")
			}
			_ = outputWrite.Close()
		}
	})
	return session
}

func (session *supervisorSession) awaitReady() supervisorMessage {
	session.t.Helper()
	message := session.await("ready")
	if message.Type != "ready" || message.SupervisorPID <= 0 || message.ProcessGroupID <= 0 ||
		message.ProcessIdentity == "" || message.GroupIdentity == "" {
		session.t.Fatalf("readiness message = %+v", message)
	}
	session.ready = message
	return message
}

func (session *supervisorSession) await(description string) supervisorMessage {
	session.t.Helper()
	select {
	case message, ok := <-session.decoded:
		if !ok {
			session.t.Fatalf("supervisor closed its output before sending %s", description)
		}
		return message
	case <-time.After(60 * time.Second):
		session.t.Fatalf("supervisor did not send %s", description)
		return supervisorMessage{}
	}
}

func (session *supervisorSession) send(command string) {
	session.t.Helper()
	if _, err := io.WriteString(session.control, command+"\n"); err != nil {
		session.t.Fatal(err)
	}
}

func (session *supervisorSession) closeControl() {
	session.t.Helper()
	if err := session.control.Close(); err != nil {
		session.t.Fatal(err)
	}
}

// awaitExit waits for the terminal message and for RunSupervisor to return.
func (session *supervisorSession) awaitExit() (supervisorMessage, error) {
	session.t.Helper()
	var exit supervisorMessage
	for {
		message := session.await("an exit message")
		if message.Type == "exit" {
			exit = message
			break
		}
	}
	return exit, session.awaitReturn()
}

func (session *supervisorSession) awaitReturn() error {
	session.t.Helper()
	select {
	case err := <-session.result:
		session.finished = true
		_ = session.output.Close()
		return err
	case <-time.After(60 * time.Second):
		session.t.Fatal("RunSupervisor did not return")
		return nil
	}
}

func writeRuntimeScript(t *testing.T, directory, name, body string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func newSupervisorInit(t *testing.T, runtime, executable string, timeoutSeconds int) supervisorInit {
	t.Helper()
	worktree := t.TempDir()
	return supervisorInit{
		Runtime:           runtime,
		RuntimeExecutable: executable,
		Worktree:          worktree,
		ResultPath:        filepath.Join(worktree, "result"),
		Prompt:            "do the work",
		TimeoutSeconds:    timeoutSeconds,
	}
}

func waitFor(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readRuntimePID(t *testing.T, path string) int {
	t.Helper()
	waitFor(t, 30*time.Second, "the fake runtime to report its process", func() bool {
		body, err := os.ReadFile(path)
		return err == nil && strings.TrimSpace(string(body)) != ""
	})
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestRunSupervisorRejectsInvalidInput(t *testing.T) {
	valid := supervisorInit{
		Runtime: protocol.RuntimeCodex, RuntimeExecutable: "/bin/true",
		Worktree: "/tmp", ResultPath: "/tmp/result", TimeoutSeconds: 60,
	}
	cases := []struct {
		name   string
		mutate func(*supervisorInit)
		body   string
	}{
		{name: "unknown runtime", mutate: func(init *supervisorInit) { init.Runtime = "unknown" }},
		{name: "no executable", mutate: func(init *supervisorInit) { init.RuntimeExecutable = "" }},
		{name: "no worktree", mutate: func(init *supervisorInit) { init.Worktree = "" }},
		{name: "no result path", mutate: func(init *supervisorInit) { init.ResultPath = "" }},
		{name: "no timeout", mutate: func(init *supervisorInit) { init.TimeoutSeconds = 0 }},
		{name: "negative timeout", mutate: func(init *supervisorInit) { init.TimeoutSeconds = -1 }},
		{
			name:   "timeout above the maximum",
			mutate: func(init *supervisorInit) { init.TimeoutSeconds = int(protocol.MaxTimeout/time.Second) + 1 },
		},
		{name: "run without session", mutate: func(init *supervisorInit) { init.RunID = "run-1" }},
		{name: "session without run", mutate: func(init *supervisorInit) { init.SessionID = "session-1" }},
		{name: "undecodable input", body: "{"},
		{name: "unknown field", body: `{"runtime":"codex","surprise":true}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := testCase.body
			if body == "" {
				init := valid
				testCase.mutate(&init)
				encoded, err := json.Marshal(init)
				if err != nil {
					t.Fatal(err)
				}
				body = string(encoded)
			}
			control, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			defer writer.Close()
			output := &bytes.Buffer{}
			if err := RunSupervisor(control, strings.NewReader(body), output,
				newLimitBuffer(maxSupervisorErrorBytes)); err == nil {
				t.Fatal("RunSupervisor accepted invalid input")
			}
			if output.Len() != 0 {
				t.Fatalf("RunSupervisor wrote %q before validating its input", output.String())
			}
		})
	}
}

func TestRunSupervisorRequiresAControlPipe(t *testing.T) {
	err := RunSupervisor(nil, strings.NewReader("{}"), &bytes.Buffer{}, newLimitBuffer(maxSupervisorErrorBytes))
	if err == nil || !strings.Contains(err.Error(), "control pipe") {
		t.Fatalf("RunSupervisor without a control pipe returned %v", err)
	}
}

func TestRunSupervisorDefaultsToCodex(t *testing.T) {
	directory := t.TempDir()
	init := newSupervisorInit(t, "", writeRuntimeScript(t, directory, "runtime", "exit 0\n"), 60)
	init.Runtime = ""
	session := startSupervisorSession(t, init)
	session.awaitReady()
	session.send("start")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "exited" || exit.ExitCode != 0 {
		t.Fatalf("exit message = %+v", exit)
	}
}

func TestRunSupervisorCancelsARunningRuntime(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "runtime.pid")
	script := writeRuntimeScript(t, directory, "runtime",
		"echo $$ > '"+pidPath+"'\necho working\nsleep 300\n")
	init := newSupervisorInit(t, protocol.RuntimePi, script, 600)

	session := startSupervisorSession(t, init)
	ready := session.awaitReady()
	session.send("start")
	runtimePID := readRuntimePID(t, pidPath)

	session.send("cancel")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "cancelled" || exit.Error != "attempt cancelled" {
		t.Fatalf("exit message = %+v", exit)
	}

	// Cancellation must tear the whole process group down, not just the runtime.
	waitFor(t, 30*time.Second, "the runtime process to be reaped", func() bool {
		return !processAlive(runtimePID)
	})
	waitFor(t, 30*time.Second, "the attempt process group to be torn down", func() bool {
		return !processGroupAlive(int(ready.ProcessGroupID))
	})
}

func TestRunSupervisorStopsARuntimeAtItsTimeout(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "runtime", "sleep 300\n")
	// The runtime is stopped one second after it starts, which is too early to
	// wait on anything the fake runtime writes, so this test asserts on the
	// process group rather than on an individual runtime process. The
	// cancellation and parent-lost tests cover per-process reaping.
	init := newSupervisorInit(t, protocol.RuntimePi, script, 1)

	session := startSupervisorSession(t, init)
	ready := session.awaitReady()
	session.send("start")

	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "timeout" || exit.Error != "Session timeout reached" {
		t.Fatalf("exit message = %+v", exit)
	}
	waitFor(t, 30*time.Second, "the attempt process group to be torn down", func() bool {
		return !processGroupAlive(int(ready.ProcessGroupID))
	})
}

func TestRunSupervisorStopsWhenItsParentGoesAway(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "runtime.pid")
	script := writeRuntimeScript(t, directory, "runtime",
		"echo $$ > '"+pidPath+"'\nsleep 300\n")
	init := newSupervisorInit(t, protocol.RuntimePi, script, 600)

	session := startSupervisorSession(t, init)
	ready := session.awaitReady()
	session.send("start")
	runtimePID := readRuntimePID(t, pidPath)

	session.closeControl()
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "parent_lost" || exit.Error != "worker parent process exited" {
		t.Fatalf("exit message = %+v", exit)
	}
	waitFor(t, 30*time.Second, "the runtime process to be reaped", func() bool {
		return !processAlive(runtimePID)
	})
	waitFor(t, 30*time.Second, "the attempt process group to be torn down", func() bool {
		return !processGroupAlive(int(ready.ProcessGroupID))
	})
}

func TestRunSupervisorStopsWhenTheLeaseDeadlinePassesBeforeStart(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "runtime", "sleep 300\n")
	init := newSupervisorInit(t, protocol.RuntimePi, script, 600)

	session := startSupervisorSession(t, init)
	ready := session.awaitReady()
	// A one-millisecond lease leaves no room for the termination grace, so the
	// supervisor stops the attempt almost immediately.
	session.send("renew 1")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "lease_lost" || exit.Error != "control-plane lease renewal deadline passed" {
		t.Fatalf("exit message = %+v", exit)
	}
	waitFor(t, 30*time.Second, "the attempt process group to be torn down", func() bool {
		return !processGroupAlive(int(ready.ProcessGroupID))
	})
}

func TestRunSupervisorHandlesStopCommandsBeforeStart(t *testing.T) {
	cases := []struct {
		name    string
		command string
		reason  string
		message string
	}{
		{name: "cancel", command: "cancel", reason: "cancelled", message: "attempt cancelled"},
		{name: "lease lost", command: "lease_lost", reason: "lease_lost", message: "control-plane lease was lost"},
		{name: "preparation failure", command: "fail", reason: "supervisor_error", message: "attempt preparation failed"},
		{name: "timeout", command: "timeout", reason: "timeout", message: "Session timeout reached"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			script := writeRuntimeScript(t, directory, "runtime", "sleep 300\n")
			session := startSupervisorSession(t, newSupervisorInit(t, protocol.RuntimePi, script, 600))
			ready := session.awaitReady()

			session.send(testCase.command)
			exit, err := session.awaitExit()
			if err != nil {
				t.Fatal(err)
			}
			if exit.Reason != testCase.reason || exit.Error != testCase.message {
				t.Fatalf("exit message = %+v", exit)
			}
			waitFor(t, 30*time.Second, "the attempt process group to be torn down", func() bool {
				return !processGroupAlive(int(ready.ProcessGroupID))
			})
		})
	}
}

func TestRunSupervisorHandlesStopCommandsWhileRunning(t *testing.T) {
	cases := []struct {
		name    string
		command string
		reason  string
		message string
	}{
		{name: "preparation failure", command: "fail", reason: "supervisor_error", message: "attempt supervisor failed"},
		{
			name:    "lease lost",
			command: "lease_lost",
			reason:  "lease_lost",
			message: "control-plane lease renewal deadline passed",
		},
		{
			name: "outcome reported", command: "outcome_reported",
			reason: "outcome_reported", message: "",
		},
		{
			// A renewal this short leaves no room for the termination grace, so
			// the lease deadline passes almost immediately.
			name:    "renewal deadline",
			command: "renew 1",
			reason:  "lease_lost",
			message: "control-plane lease renewal deadline passed",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			pidPath := filepath.Join(directory, "runtime.pid")
			script := writeRuntimeScript(t, directory, "runtime",
				"echo $$ > '"+pidPath+"'\nsleep 300\n")
			session := startSupervisorSession(t, newSupervisorInit(t, protocol.RuntimePi, script, 600))
			ready := session.awaitReady()
			session.send("start")
			runtimePID := readRuntimePID(t, pidPath)

			session.send(testCase.command)
			exit, err := session.awaitExit()
			if err != nil {
				t.Fatal(err)
			}
			if exit.Reason != testCase.reason || exit.Error != testCase.message {
				t.Fatalf("exit message = %+v", exit)
			}
			waitFor(t, 30*time.Second, "the runtime process to be reaped", func() bool {
				return !processAlive(runtimePID)
			})
			waitFor(t, 30*time.Second, "the attempt process group to be torn down", func() bool {
				return !processGroupAlive(int(ready.ProcessGroupID))
			})
		})
	}
}

func TestRunSupervisorRejectsAnUnknownControlCommand(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "runtime", "sleep 300\n")
	session := startSupervisorSession(t, newSupervisorInit(t, protocol.RuntimePi, script, 600))
	ready := session.awaitReady()

	session.send("detonate")
	err := session.awaitReturn()
	if err == nil || !strings.Contains(err.Error(), "unknown supervisor command") {
		t.Fatalf("RunSupervisor returned %v", err)
	}
	waitFor(t, 30*time.Second, "the attempt process group to be torn down", func() bool {
		return !processGroupAlive(int(ready.ProcessGroupID))
	})
}

func TestRunSupervisorReportsARuntimeThatCannotStart(t *testing.T) {
	directory := t.TempDir()
	init := newSupervisorInit(t, protocol.RuntimePi, filepath.Join(directory, "absent-runtime"), 60)

	session := startSupervisorSession(t, init)
	ready := session.awaitReady()
	session.send("start")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "supervisor_error" || exit.ExitCode != -1 {
		t.Fatalf("exit message = %+v", exit)
	}
	if !strings.Contains(exit.Error, "start Pi") {
		t.Fatalf("exit error = %q", exit.Error)
	}
	waitFor(t, 30*time.Second, "the attempt process group to be torn down", func() bool {
		return !processGroupAlive(int(ready.ProcessGroupID))
	})
}

func TestRunSupervisorCapturesPiResultAndOutput(t *testing.T) {
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "runtime",
		"cat > '"+filepath.Join(directory, "prompt")+"'\nprintf 'final answer\\n'\n")
	init := newSupervisorInit(t, protocol.RuntimePi, script, 60)

	session := startSupervisorSession(t, init)
	session.awaitReady()
	session.send("start")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "exited" || exit.ExitCode != 0 || exit.Error != "" {
		t.Fatalf("exit message = %+v", exit)
	}
	if exit.Result != "final answer" || exit.Truncated {
		t.Fatalf("exit result = %q, truncated %v", exit.Result, exit.Truncated)
	}
	prompt, err := os.ReadFile(filepath.Join(directory, "prompt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(prompt) != init.Prompt {
		t.Fatalf("runtime received prompt %q, want %q", prompt, init.Prompt)
	}
}

func TestRunSupervisorReportsAnEmptyPiResult(t *testing.T) {
	directory := t.TempDir()
	init := newSupervisorInit(t, protocol.RuntimePi, writeRuntimeScript(t, directory, "runtime", "exit 0\n"), 60)

	session := startSupervisorSession(t, init)
	session.awaitReady()
	session.send("start")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "exited" || exit.Error != "Pi returned no result" {
		t.Fatalf("exit message = %+v", exit)
	}
}

func TestRunSupervisorReportsRuntimeFailureWithItsStderrTail(t *testing.T) {
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "runtime", "echo 'runtime blew up' >&2\nexit 3\n")
	init := newSupervisorInit(t, protocol.RuntimePi, script, 60)

	session := startSupervisorSession(t, init)
	session.awaitReady()
	session.send("start")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "exited" || exit.ExitCode != 3 {
		t.Fatalf("exit message = %+v", exit)
	}
	if !strings.Contains(exit.Error, "exit status 3") || !strings.Contains(exit.Error, "runtime blew up") {
		t.Fatalf("exit error = %q", exit.Error)
	}
}

func TestRunSupervisorReadsTheCodexResultFile(t *testing.T) {
	directory := t.TempDir()
	worktree := t.TempDir()
	resultPath := filepath.Join(worktree, "result")
	script := writeRuntimeScript(t, directory, "runtime", "printf 'codex answer' > '"+resultPath+"'\n")
	init := supervisorInit{
		Runtime: protocol.RuntimeCodex, RuntimeExecutable: script, Worktree: worktree,
		ResultPath: resultPath, Prompt: "do the work", TimeoutSeconds: 60,
		RunID: "run-1", SessionID: "session-1",
	}

	session := startSupervisorSession(t, init)
	session.awaitReady()
	session.send("start")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "exited" || exit.Result != "codex answer" || exit.Truncated {
		t.Fatalf("exit message = %+v", exit)
	}
	if _, err := os.Stat(resultPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("supervisor left the result file behind: %v", err)
	}
}

func TestRunSupervisorCapturesTheClaudeCodeResultEvent(t *testing.T) {
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "runtime",
		"echo '{\"type\":\"system\",\"subtype\":\"init\"}'\n"+
			"echo '{\"type\":\"result\",\"result\":\"claude answer\",\"is_error\":false}'\n")
	init := newSupervisorInit(t, protocol.RuntimeClaudeCode, script, 60)

	session := startSupervisorSession(t, init)
	session.awaitReady()
	session.send("start")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "exited" || exit.Result != "claude answer" || exit.Error != "" {
		t.Fatalf("exit message = %+v", exit)
	}
}

func TestRunSupervisorReportsAMissingClaudeCodeResultEvent(t *testing.T) {
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "runtime", "echo '{\"type\":\"system\"}'\n")
	init := newSupervisorInit(t, protocol.RuntimeClaudeCode, script, 60)

	session := startSupervisorSession(t, init)
	session.awaitReady()
	session.send("start")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "exited" || exit.Error != "Claude Code returned no terminal result event" {
		t.Fatalf("exit message = %+v", exit)
	}
}

func TestRunSupervisorReportsAClaudeCodeTerminalError(t *testing.T) {
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "runtime",
		"echo '{\"type\":\"result\",\"result\":\"the model refused\",\"is_error\":true}'\n")
	init := newSupervisorInit(t, protocol.RuntimeClaudeCode, script, 60)

	session := startSupervisorSession(t, init)
	session.awaitReady()
	session.send("start")
	exit, err := session.awaitExit()
	if err != nil {
		t.Fatal(err)
	}
	if exit.Reason != "exited" || exit.Error != "the model refused" {
		t.Fatalf("exit message = %+v", exit)
	}
}

func TestRunSupervisorForwardsRuntimeOutputAsEvents(t *testing.T) {
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "runtime",
		"echo standard output\necho standard error >&2\nprintf 'result\\n'\n")
	init := newSupervisorInit(t, protocol.RuntimePi, script, 60)

	session := startSupervisorSession(t, init)
	session.awaitReady()
	session.send("start")

	streams := map[string][]string{}
	for {
		message := session.await("runtime output")
		if message.Type == "exit" {
			break
		}
		if message.Type != "output" {
			t.Fatalf("unexpected message %+v", message)
		}
		streams[message.Stream] = append(streams[message.Stream], message.Text)
	}
	if err := session.awaitReturn(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(streams["stdout"], "|"); got != "standard output|result" {
		t.Fatalf("stdout events = %q", got)
	}
	if got := strings.Join(streams["stderr"], "|"); got != "standard error" {
		t.Fatalf("stderr events = %q", got)
	}
}

func TestStopStartedSupervisorGroupTearsDownTheWholeGroup(t *testing.T) {
	t.Parallel()
	anchor, identity, childPID := startTestProcessGroup(t)
	groupID := anchor.Process.Pid

	if err := stopStartedSupervisorGroup(anchor, identity, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := anchor.Wait(); err == nil {
		t.Fatal("anchor exited cleanly after being stopped")
	}
	waitFor(t, 30*time.Second, "the group child to exit", func() bool {
		return !processAlive(childPID)
	})
	waitFor(t, 30*time.Second, "the process group to be torn down", func() bool {
		return !processGroupAlive(groupID)
	})
}

func TestStopStartedSupervisorGroupForceStopsAnUnverifiedGroup(t *testing.T) {
	t.Parallel()
	anchor, _, childPID := startTestProcessGroup(t)
	groupID := anchor.Process.Pid

	// A mismatched identity means the supervisor refuses to signal the group by
	// identity, but it still force-stops a group it started itself.
	if err := stopStartedSupervisorGroup(anchor, "not the recorded identity", terminationGrace); err != nil {
		t.Fatal(err)
	}
	_ = anchor.Wait()
	waitFor(t, 30*time.Second, "the group child to exit", func() bool {
		return !processAlive(childPID)
	})
	waitFor(t, 30*time.Second, "the process group to be torn down", func() bool {
		return !processGroupAlive(groupID)
	})
}

func TestStopStartedSupervisorGroupRejectsAnUnstartedAnchor(t *testing.T) {
	if err := stopStartedSupervisorGroup(nil, "identity", 0); err == nil {
		t.Fatal("stopStartedSupervisorGroup accepted a nil anchor")
	}
	if err := stopStartedSupervisorGroup(exec.Command("/bin/true"), "identity", 0); err == nil {
		t.Fatal("stopStartedSupervisorGroup accepted an unstarted anchor")
	}
}

// startTestProcessGroup starts an anchor that ignores SIGTERM, plus a child in
// the same group, mirroring how the supervisor anchors an attempt.
func startTestProcessGroup(t *testing.T) (*exec.Cmd, string, int) {
	t.Helper()
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "child.pid")
	anchor := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 3600; done")
	configureNewProcessGroup(anchor)
	if err := anchor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Only signal a group that is still alive, so a reaped group's recycled
		// process-group ID is never signalled by mistake.
		if anchor.Process != nil && processGroupAlive(anchor.Process.Pid) {
			_ = forceStopStartedProcessGroup(anchor.Process.Pid)
		}
	})
	identity, err := processIdentity(anchor.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sh", "-c", "echo $$ > '"+pidPath+"'; sleep 300")
	configureExistingProcessGroup(child, anchor.Process.Pid)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = child.Wait() }()
	return anchor, identity, readRuntimePID(t, pidPath)
}

func TestWaitForSupervisorCommand(t *testing.T) {
	t.Parallel()
	t.Run("exits on its own", func(t *testing.T) {
		command := exec.Command("/bin/sh", "-c", "exit 0")
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		if err := waitForSupervisorCommand(command, 30*time.Second); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("is killed after the timeout", func(t *testing.T) {
		command := exec.Command("/bin/sh", "-c", "trap '' TERM; while :; do sleep 3600; done")
		configureNewProcessGroup(command)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		pid := command.Process.Pid
		// waitForSupervisorCommand kills the process, not its group, so the
		// forked sleep would otherwise outlive the test.
		t.Cleanup(func() { _ = forceStopStartedProcessGroup(pid) })
		err := waitForSupervisorCommand(command, 100*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "did not exit within") {
			t.Fatalf("waitForSupervisorCommand returned %v", err)
		}
		waitFor(t, 30*time.Second, "the command to be killed", func() bool {
			return !processAlive(pid)
		})
	})
}

func TestWaitForSupervisorReaders(t *testing.T) {
	t.Parallel()
	t.Run("readers finish", func(t *testing.T) {
		stdout, stdoutWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr, stderrWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer closeAll(stdout, stdoutWriter, stderr, stderrWriter)
		readers := &sync.WaitGroup{}
		readers.Add(1)
		go readers.Done()
		if err := waitForSupervisorReaders(readers, stdout, stderr, 30*time.Second); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stuck readers are released by closing their pipes", func(t *testing.T) {
		stdout, stdoutWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr, stderrWriter, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer closeAll(stdoutWriter, stderrWriter)
		readers := &sync.WaitGroup{}
		readers.Add(2)
		for _, pipe := range []*os.File{stdout, stderr} {
			go func(pipe *os.File) {
				defer readers.Done()
				_, _ = io.Copy(io.Discard, pipe)
			}(pipe)
		}
		err = waitForSupervisorReaders(readers, stdout, stderr, 100*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "remained open") {
			t.Fatalf("waitForSupervisorReaders returned %v", err)
		}
	})
}

func closeAll(files ...*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

func TestStartSupervisorRunsTheControlProtocol(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	script := writeRuntimeScript(t, directory, "supervisor",
		"cat > '"+filepath.Join(directory, "init.json")+"'\n"+
			"printf '{\"type\":\"ready\",\"supervisor_pid\":%s,\"process_identity\":\"identity\","+
			"\"process_group_id\":%s,\"group_identity\":\"group\"}\\n' \"$$\" \"$$\"\n"+
			"while IFS= read -r line <&3; do\n"+
			"  if [ \"$line\" = \"cancel\" ]; then break; fi\n"+
			"done\n"+
			"printf '{\"type\":\"exit\",\"reason\":\"cancelled\",\"error\":\"attempt cancelled\"}\\n'\n"+
			"echo 'supervisor finished' >&2\n")
	init := supervisorInit{
		Runtime: protocol.RuntimeCodex, RuntimeExecutable: "/bin/true", Worktree: directory,
		ResultPath: filepath.Join(directory, "result"), Prompt: "do the work", TimeoutSeconds: 60,
	}

	errorOutput := newLimitBuffer(maxSupervisorErrorBytes)
	process, err := startSupervisor([]string{script}, init, errorOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.awaitReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	if process.supervisorPID <= 0 || process.processGroupID <= 0 ||
		process.supervisorIdentity != "identity" || process.groupIdentity != "group" {
		t.Fatalf("supervisor readiness = %+v", process)
	}

	if err := process.send("cancel"); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-process.messages:
		if message.Type != "exit" || message.Reason != "cancelled" {
			t.Fatalf("exit message = %+v", message)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("supervisor did not report an exit")
	}
	if err := process.closeControl(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-process.wait:
		if err != nil {
			t.Fatalf("supervisor exited with %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("supervisor did not exit")
	}

	body, err := os.ReadFile(filepath.Join(directory, "init.json"))
	if err != nil {
		t.Fatal(err)
	}
	var received supervisorInit
	if err := json.Unmarshal(body, &received); err != nil {
		t.Fatal(err)
	}
	if received != init {
		t.Fatalf("supervisor received %+v, want %+v", received, init)
	}
	waitFor(t, 30*time.Second, "the supervisor stderr to be mirrored", func() bool {
		return strings.Contains(errorOutput.String(), "supervisor finished")
	})
}

func TestStartSupervisorRejectsBadCommandLines(t *testing.T) {
	init := supervisorInit{Runtime: protocol.RuntimeCodex}
	if _, err := startSupervisor(nil, init, newLimitBuffer(1024)); err == nil {
		t.Fatal("startSupervisor accepted an empty command line")
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := startSupervisor([]string{missing}, init, newLimitBuffer(1024)); err == nil {
		t.Fatal("startSupervisor accepted a missing executable")
	}
}
