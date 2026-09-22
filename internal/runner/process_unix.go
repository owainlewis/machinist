//go:build darwin || linux

package runner

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	err := syscall.Kill(-process.Pid, syscall.SIGKILL)
	if ignorableProcessTreeTerminationError(err, func() error {
		return syscall.Kill(-process.Pid, 0)
	}) {
		return nil
	}
	return err
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
