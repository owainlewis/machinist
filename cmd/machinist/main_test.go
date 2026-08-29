//go:build darwin || linux

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBinaryHandlesClosedOutputPipe(t *testing.T) {
	directory := t.TempDir()
	script := `cat >/dev/null; awk 'BEGIN { for (i = 0; i < 2097152; i++) printf "x" }'`
	binary, repository, workerPath := setupBinaryRun(t, directory, []string{"/bin/sh", "-c", script}, "10s")
	command := exec.Command(binary, "worker", "run", "--command=plan", "--prompt=run the configured agent", "--repo="+repository, "--config="+workerPath)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	oneByte := make([]byte, 1)
	if _, err := stdout.Read(oneByte); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("error = %v, stderr = %q", err, stderr.String())
	}
}

func TestBinaryTimeoutInterruptsOpenUnreadOutputPipe(t *testing.T) {
	directory := t.TempDir()
	script := "cat >/dev/null; yes machinist"
	binary, repository, workerPath := setupBinaryRun(t, directory, []string{"/bin/sh", "-c", script}, "200ms")
	command := exec.Command(binary, "worker", "run", "--command=plan", "--prompt=run the configured agent", "--repo="+repository, "--config="+workerPath)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 124 {
			t.Fatalf("error = %v, stderr = %q", err, stderr.String())
		}
	case <-time.After(3 * time.Second):
		_ = command.Process.Kill()
		_ = stdout.Close()
		<-done
		t.Fatal("Machinist did not time out with an open unread stdout pipe")
	}
}

func TestBinaryTimeoutInterruptsOpenUnreadErrorPipe(t *testing.T) {
	directory := t.TempDir()
	script := "cat >/dev/null; yes machinist >&2"
	binary, repository, workerPath := setupBinaryRun(t, directory, []string{"/bin/sh", "-c", script}, "200ms")
	command := exec.Command(binary, "worker", "run", "--command=plan", "--prompt=run the configured agent", "--repo="+repository, "--config="+workerPath)
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	command.Stdout = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 124 {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(3 * time.Second):
		_ = command.Process.Kill()
		_ = stderr.Close()
		<-done
		t.Fatal("Machinist did not time out with an open unread stderr pipe")
	}
}

func TestAgentGetsDefaultSIGPIPEDisposition(t *testing.T) {
	compiler, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("C compiler is required to inspect inherited signal disposition")
	}
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "sigpipe.c")
	source := "#include <signal.h>\n#include <unistd.h>\nint main(void) { char b[256]; while (read(0, b, sizeof b) > 0) {} raise(SIGPIPE); return 0; }\n"
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	agentBinary := filepath.Join(directory, "sigpipe-agent")
	if output, err := exec.Command(compiler, "-o", agentBinary, sourcePath).CombinedOutput(); err != nil {
		t.Fatalf("compile signal helper: %v: %s", err, output)
	}
	binary, repository, workerPath := setupBinaryRun(t, directory, []string{agentBinary}, "10s")
	command := exec.Command(binary, "worker", "run", "--command=plan", "--prompt=run the configured agent", "--repo="+repository, "--config="+workerPath)
	var stderr bytes.Buffer
	command.Stdout = io.Discard
	command.Stderr = &stderr
	err = command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 141 {
		t.Fatalf("error = %v, stderr = %q", err, stderr.String())
	}
}

func setupBinaryRun(t *testing.T, directory string, agentCommand []string, timeout string) (string, string, string) {
	t.Helper()
	binary := filepath.Join(directory, "machinist")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build machinist: %v: %s", err, output)
	}
	repository := filepath.Join(directory, "repository")
	if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(directory, "plan.md"), []byte("{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	quotedCommand := make([]string, len(agentCommand))
	for index, argument := range agentCommand {
		quotedCommand[index] = strconv.Quote(argument)
	}
	definition := "[commands.plan]\n" +
		"executor = \"test\"\n" +
		"prompt_file = \"plan.md\"\n" +
		"timeout = " + strconv.Quote(timeout) + "\n"
	if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	workerPath := filepath.Join(directory, "worker.toml")
	worker := "data_directory = " + strconv.Quote(filepath.Join(directory, "data")) + "\n" +
		"\n" +
		"[executors.test]\n" +
		"command = [" + strings.Join(quotedCommand, ", ") + "]\n"
	if err := os.WriteFile(workerPath, []byte(worker), 0o600); err != nil {
		t.Fatal(err)
	}
	return binary, repository, workerPath
}
