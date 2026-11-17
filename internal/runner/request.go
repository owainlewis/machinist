package runner

import (
	"encoding/hex"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

// Step is one shell command of a preset. It runs with sh -c in the job's
// workspace. TimeoutMillis of zero means the rest of the run's time.
type Step struct {
	Name          string `json:"name"`
	Run           string `json:"run"`
	TimeoutMillis int64  `json:"timeout_millis,omitempty"`
}

// RunRequest is everything a runner needs to start one run. It carries no
// secrets. The steps run in order in one empty workspace.
type RunRequest struct {
	RunID         string            `json:"run_id"`
	TicketID      string            `json:"ticket_id"`
	Preset        string            `json:"preset"`
	Repository    string            `json:"repository"`
	Branch        string            `json:"branch"`
	Vars          map[string]string `json:"vars,omitempty"`
	Prompt        string            `json:"prompt"`
	Steps         []Step            `json:"steps"`
	TimeoutMillis int64             `json:"timeout_millis"`
	GitName       string            `json:"git_name"`
	GitEmail      string            `json:"git_email"`
}

// RunResult is what one run produced, on either runner type.
type RunResult struct {
	State  State
	Report *protocol.Report
	Error  string
	Stages []protocol.StageResult
}

// ValidRunID reports whether value has the form run_ and 24 hex characters.
func ValidRunID(value string) bool {
	if len(value) != len("run_")+24 || !strings.HasPrefix(value, "run_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "run_"))
	return err == nil
}
