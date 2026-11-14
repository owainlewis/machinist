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

const (
	StageAgent  = "agent"
	StageScript = "script"
)

// Stage is one step of a run. An agent stage starts the harness with Prompt. A
// script stage runs Script with sh, and its exit code decides whether it passed.
type Stage struct {
	Name       string      `json:"name"`
	Kind       string      `json:"kind"`
	Prompt     string      `json:"prompt,omitempty"`
	Model      string      `json:"model,omitempty"`
	AgentFiles []AgentFile `json:"agent_files,omitempty"`
	Script     string      `json:"script,omitempty"`
}

// RunRequest is everything a runner needs to start one run. It carries no secrets.
// Stages run in order in one workspace. A request without stages is one agent
// stage made of Prompt, Model and AgentFiles.
type RunRequest struct {
	RunID         string      `json:"run_id"`
	TicketID      string      `json:"ticket_id"`
	Repository    string      `json:"repository"`
	Branch        string      `json:"branch"`
	Prompt        string      `json:"prompt"`
	AgentFiles    []AgentFile `json:"agent_files,omitempty"`
	Stages        []Stage     `json:"stages,omitempty"`
	Harness       string      `json:"harness"`
	Model         string      `json:"model,omitempty"`
	TimeoutMillis int64       `json:"timeout_millis"`
	GitName       string      `json:"git_name"`
	GitEmail      string      `json:"git_email"`
}

// RunResult is what one run produced, on either runner type. Stages is set
// when the request had stages.
type RunResult struct {
	State            State
	Report           *protocol.Report
	Tokens           *int64
	Cost             *float64
	Error            string
	CredentialSource string
	Stages           []protocol.StageResult
}

// ValidRunID reports whether value has the form run_ and 24 hex characters.
func ValidRunID(value string) bool {
	if len(value) != len("run_")+24 || !strings.HasPrefix(value, "run_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "run_"))
	return err == nil
}
