//go:build darwin || linux

package runner

import (
	"context"
	"github.com/owainlewis/machinist/internal/config"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestDetachedTreeHelper(t *testing.T) {
	role := os.Getenv("MACHINIST_TREE_HELPER")
	if role == "" {
		return
	}
	if role == "parent" || role == "orphan-parent" {
		c := exec.Command(os.Args[0], "-test.run=^TestDetachedTreeHelper$")
		c.Env = append(os.Environ(), "MACHINIST_TREE_HELPER=child")
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("MACHINIST_TREE_PID"), []byte(strconv.Itoa(c.Process.Pid)), 0600)
		if role == "orphan-parent" {
			os.Exit(0)
		}
		_ = c.Wait()
	} else {
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}
func TestCancelStopsDetachedDescendantButNotSentinel(t *testing.T) {
	sentinel := exec.Command(os.Args[0], "-test.run=^TestDetachedTreeHelper$")
	sentinel.Env = append(os.Environ(), "MACHINIST_TREE_HELPER=sentinel")
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sentinel.Process.Kill(); _ = sentinel.Wait() }()
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	t.Setenv("MACHINIST_TREE_HELPER", "parent")
	t.Setenv("MACHINIST_TREE_PID", pidPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		res, _ := Execute(ctx, Options{Repository: ".", Prompt: testPrompt, Environment: os.Environ(), DataDirectory: dir, Command: config.ResolvedCommand{Name: "detached", Command: []string{os.Args[0], "-test.run=^TestDetachedTreeHelper$"}, Timeout: time.Minute}, Stdout: io.Discard, Stderr: io.Discard})
		done <- res
	}()
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(pidPath)
		pid, _ = strconv.Atoi(string(b))
		if pid > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child not started")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	select {
	case res := <-done:
		if res.State != StateCancelled {
			t.Fatal(res.State)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel hung")
	}
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) != syscall.ESRCH {
		t.Fatalf("detached child %d survived", pid)
	}
	if err := sentinel.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated sentinel stopped: %v", err)
	}
}

func TestCancellationDoesNotConfirmFailedProcessEnumeration(t *testing.T) {
	bin := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result, err := Execute(ctx, Options{Repository: ".", Prompt: testPrompt, Environment: os.Environ(), DataDirectory: t.TempDir(), Command: config.ResolvedCommand{Name: "scanner-failure", Command: []string{"/bin/sh", "-c", "/bin/sleep 60"}, Timeout: time.Minute}, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || result.State != StateFailed || result.ProcessStopped || !result.CleanupUnconfirmed {
		t.Fatalf("false shutdown confirmation: %+v %v", result, err)
	}
}

func TestCancelledBeforeLaunchIsAlreadyStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Execute(ctx, Options{Repository: ".", Prompt: testPrompt, Environment: os.Environ(), DataDirectory: t.TempDir(), Command: config.ResolvedCommand{Name: "not-started", Command: []string{"/bin/sh", "-c", "exit 99"}, Timeout: time.Minute}, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || result.State != StateCancelled || !result.ProcessStopped {
		t.Fatalf("prelaunch cancel: %+v %v", result, err)
	}
}

func TestNaturalExitStopsReparentedTool(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "orphan.pid")
	t.Setenv("MACHINIST_TREE_HELPER", "orphan-parent")
	t.Setenv("MACHINIST_TREE_PID", pidPath)
	result, err := Execute(t.Context(), Options{Repository: ".", Prompt: testPrompt, Environment: os.Environ(), DataDirectory: dir, Command: config.ResolvedCommand{Name: "orphan", Command: []string{os.Args[0], "-test.run=^TestDetachedTreeHelper$"}, Timeout: time.Minute}, Stdout: io.Discard, Stderr: io.Discard})
	b, _ := os.ReadFile(pidPath)
	pid, _ := strconv.Atoi(string(b))
	if pid <= 0 {
		t.Fatal("missing tool pid")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if err != nil || !result.ProcessStopped {
		t.Fatalf("cleanup failed: %+v %v", result, err)
	}
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("reparented tool %d survived", pid)
}
