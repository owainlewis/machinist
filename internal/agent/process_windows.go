//go:build windows

package agent

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}
func stopProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
