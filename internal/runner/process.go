package runner

import (
	"os"
	"os/exec"
)

// ConfigureProcess isolates a supervised command in its own process group.
// Call before Start so cancellation can stop the whole process tree.
func ConfigureProcess(cmd *exec.Cmd) { configureProcess(cmd) }

// TerminateProcessTree stops a process group configured by ConfigureProcess.
func TerminateProcessTree(process *os.Process) error { return terminateProcessTree(process) }
