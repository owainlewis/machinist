//go:build darwin || linux

package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(process *os.Process, owners ...string) error {
	if process == nil {
		return nil
	}
	// Snapshot ancestry before stopping the harness: tool PTYs can create their
	// own process groups, so signalling only the harness group misses them.
	descendants, scanErr := processDescendants(process.Pid)
	if len(owners) > 0 && owners[0] != "" {
		owned, err := ownedProcesses(owners[0])
		scanErr = errors.Join(scanErr, err)
		seen := map[int]bool{process.Pid: true}
		for _, pid := range descendants {
			seen[pid] = true
		}
		for _, pid := range owned {
			if !seen[pid] {
				descendants = append(descendants, pid)
				seen[pid] = true
			}
		}
	}
	var cleanupErr error
	for i := len(descendants) - 1; i >= 0; i-- {
		pid := descendants[i]
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("stop descendant %d: %w", pid, err))
		}
	}
	err := syscall.Kill(-process.Pid, syscall.SIGKILL)
	if ignorableProcessTreeTerminationError(err, func() error {
		return syscall.Kill(-process.Pid, 0)
	}) {
		return errors.Join(scanErr, cleanupErr)
	}
	return errors.Join(err, scanErr, cleanupErr)
}

func ignorableProcessTreeTerminationError(err error, probeProcessGroup func() error) bool {
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	if runtime.GOOS != "darwin" || !errors.Is(err, syscall.EPERM) {
		return false
	}
	// Darwin can briefly return EPERM while an exited group is being reaped.
	// Only ignore it once a probe confirms the group is gone; an accessible or
	// persistently inaccessible group must still report the cleanup failure.
	for attempt := 0; attempt < 6; attempt++ {
		probeErr := probeProcessGroup()
		if errors.Is(probeErr, syscall.ESRCH) {
			return true
		}
		if !errors.Is(probeErr, syscall.EPERM) || attempt == 5 {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func processExitCode(state *os.ProcessState) int {
	if exitCode := state.ExitCode(); exitCode >= 0 {
		return exitCode
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}

func terminatedExitCode() int {
	return 128 + int(syscall.SIGKILL)
}

// ps is available on both supported Unix hosts. Bound the scan so an unhealthy
// host cannot hang cancellation. Only processes connected to this root by PPID
// are selected; names and command lines never confer ownership.
func processDescendants(root int) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("enumerate command descendants: %w", err)
	}
	children := map[int][]int{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		if e1 == nil && e2 == nil {
			children[parent] = append(children[parent], pid)
		}
	}
	var result []int
	seen := map[int]bool{root: true}
	var visit func(int)
	visit = func(parent int) {
		for _, pid := range children[parent] {
			if !seen[pid] {
				seen[pid] = true
				result = append(result, pid)
				visit(pid)
			}
		}
	}
	visit(root)
	return result, nil
}

// A random per-execution marker is inherited by tools even after their parent
// exits. Do not log ps output: it contains process environments. This is cleanup
// ownership, not a sandbox; tools can deliberately remove inherited variables.
func ownedProcesses(owner string) ([]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "ps", "axeww", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("enumerate owned command processes: %w", err)
	}
	marker := "MACHINIST_PROCESS_OWNER=" + owner
	var result []int
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, field := range fields[1:] {
			if field == marker {
				if pid, e := strconv.Atoi(fields[0]); e == nil {
					result = append(result, pid)
				}
				break
			}
		}
	}
	return result, nil
}

// signalProcessGroup sends sig to the command's process group, so a shell
// step's children get it too.
func signalProcessGroup(process *os.Process, sig syscall.Signal) error {
	if process == nil {
		return nil
	}
	return syscall.Kill(-process.Pid, sig)
}
