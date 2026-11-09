package runner

import (
	"encoding/hex"
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

// AgentFile is one subagent definition written into the clone before a run.
type AgentFile struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

// RunRequest is everything a runner needs to start one run. It carries no secrets.
type RunRequest struct {
	RunID         string      `json:"run_id"`
	TicketID      string      `json:"ticket_id"`
	Repository    string      `json:"repository"`
	Branch        string      `json:"branch"`
	Prompt        string      `json:"prompt"`
	AgentFiles    []AgentFile `json:"agent_files,omitempty"`
	Harness       string      `json:"harness"`
	Model         string      `json:"model,omitempty"`
	TimeoutMillis int64       `json:"timeout_millis"`
	GitName       string      `json:"git_name"`
	GitEmail      string      `json:"git_email"`
}

// RunResult is what one run produced, on either runner type.
type RunResult struct {
	State            State
	Report           *protocol.Report
	Tokens           *int64
	Cost             *float64
	Error            string
	CredentialSource string
}

// ValidRunID reports whether value has the form run_ and 24 hex characters.
func ValidRunID(value string) bool {
	if len(value) != len("run_")+24 || !strings.HasPrefix(value, "run_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "run_"))
	return err == nil
}
