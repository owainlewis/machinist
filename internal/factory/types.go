// Package factory owns browser conversations and coding tasks. Its records are
// separate from leased batch jobs, so restarting never requeues an agent turn.
package factory

import (
	"context"
	"github.com/owainlewis/machinist/internal/config"
	"time"
)

type Event struct {
	ProviderID string `json:"-"`
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	Title      string `json:"title,omitempty"`
	At         string `json:"at"`
}
type Session struct {
	ForemanProfile *config.ResolvedAgent `json:"-"`
	Reported       bool                  `json:"-"`
	Delivery       bool                  `json:"delivery,omitempty"`
	ReportQueue    []string              `json:"-"`
	ID             string                `json:"id"`
	ProjectID      string                `json:"project_id"`
	TaskID         string                `json:"task_id,omitempty"`
	Role           string                `json:"role"`
	Status         string                `json:"status"`
	ProviderID     string                `json:"-"`
	Directory      string                `json:"-"`
	Pending        string                `json:"-"`
	RequestID      string                `json:"-"`
	Step           int                   `json:"-"`
	Error          string                `json:"error,omitempty"`
	CreatedAt      string                `json:"created_at"`
}
type Check struct {
	StepID   string `json:"step_id"`
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Output   string `json:"output"`
	Revision string `json:"revision"`
}
type Task struct {
	ProjectSnapshot       config.FactoryProject           `json:"-"`
	HostSnapshot          config.FactoryHost              `json:"-"`
	DesignApprovalVersion int                             `json:"-"`
	CodeApprovalVersion   int                             `json:"-"`
	GitHubHead            string                          `json:"-"`
	GitHubChecksPass      bool                            `json:"-"`
	BaseRevision          string                          `json:"-"`
	ID                    string                          `json:"id"`
	ProjectID             string                          `json:"project_id"`
	Title                 string                          `json:"title"`
	Brief                 string                          `json:"brief"`
	Pipeline              string                          `json:"pipeline"`
	Stage                 string                          `json:"stage"`
	Status                string                          `json:"status"`
	Activity              string                          `json:"activity"`
	ApprovalSubject       string                          `json:"approval_subject,omitempty"`
	Step                  int                             `json:"step"`
	Design                string                          `json:"design,omitempty"`
	Version               int                             `json:"version"`
	Revision              string                          `json:"revision,omitempty"`
	PRURL                 string                          `json:"pr_url,omitempty"`
	Review                string                          `json:"review,omitempty"`
	Checks                []Check                         `json:"checks"`
	Repairs               int                             `json:"repairs"`
	CreatedAt             string                          `json:"created_at"`
	GitHubError           string                          `json:"github_error,omitempty"`
	ObservedAt            string                          `json:"observed_at,omitempty"`
	Directory             string                          `json:"-"`
	Branch                string                          `json:"-"`
	Steps                 []config.FactoryStep            `json:"-"`
	Agents                map[string]config.ResolvedAgent `json:"-"`
	CodeApproved          string                          `json:"-"`
}

// Disk records include private workspace and immutable definitions. Public JSON
// deliberately excludes these fields and provider IDs.
type taskRecord struct {
	ProjectSnapshot       config.FactoryProject
	HostSnapshot          config.FactoryHost
	DesignApprovalVersion int
	CodeApprovalVersion   int
	GitHubHead            string
	GitHubChecksPass      bool
	BaseRevision          string
	Task
	Directory    string
	Branch       string
	Steps        []config.FactoryStep
	Agents       map[string]config.ResolvedAgent
	CodeApproved string
}
type sessionRecord struct {
	ForemanProfile *config.ResolvedAgent
	ReportQueue    []string
	Session
	ProviderID string
	Directory  string
	Pending    string
	RequestID  string
	Step       int
}
type Permission struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	answer    chan bool
}
type RunRequest struct {
	Host                                              string
	Directory, SessionID, Prompt, SystemPrompt, Model string
	ReadOnly                                          bool
	Token, URL, Executable                            string
}
type RunFunc func(context.Context, RunRequest, func(Event), func(context.Context, string) (bool, error)) (string, error)

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Agent profile names do not grant coordinator authority. Only the project
// conversation, which has no task assignment, can manage workers.
func (v *Session) isForeman() bool {
	return v.TaskID == "" && v.Role == "foreman"
}
