package protocol

import "encoding/json"

type PollRequest struct {
	SharedOutputs bool                `json:"shared_outputs,omitempty"`
	Reviews       bool                `json:"reviews,omitempty"`
	Artifacts     bool                `json:"artifacts,omitempty"`
	Workflows     bool                `json:"workflows,omitempty"`
	InstanceID    string              `json:"instance_id"`
	Name          string              `json:"name"`
	Executors     []string            `json:"executors"`
	Repositories  []string            `json:"repositories"`
	Models        map[string][]string `json:"models,omitempty"`
}

type PollResponse struct {
	Run *RunSpec `json:"run,omitempty"`
}

type RunSpec struct {
	Revision        *Revision           `json:"revision,omitempty"`
	Task            *Task               `json:"task,omitempty"`
	Inputs          map[string]Artifact `json:"inputs,omitempty"`
	RequiredOutputs []string            `json:"required_outputs,omitempty"`
	ArtifactLimits  *ArtifactLimits     `json:"artifact_limits,omitempty"`
	Workflow        bool                `json:"workflow,omitempty"`
	ID              string              `json:"id"`
	JobID           string              `json:"job_id"`
	Command         string              `json:"command"`
	CommandHash     string              `json:"command_hash"`
	Executor        string              `json:"executor"`
	Model           string              `json:"model,omitempty"`
	Repository      string              `json:"repository"`
	RenderedPrompt  string              `json:"rendered_prompt"`
	TimeoutMillis   int64               `json:"timeout_millis"`
	LeaseToken      string              `json:"lease_token"`
}

type Heartbeat struct {
	InstanceID string `json:"instance_id"`
	LeaseToken string `json:"lease_token"`
}

type Completion struct {
	Artifacts        []string        `json:"artifacts,omitempty"`
	PublicationError string          `json:"publication_error,omitempty"`
	InstanceID       string          `json:"instance_id"`
	LeaseToken       string          `json:"lease_token"`
	State            string          `json:"state"`
	ExitCode         int             `json:"exit_code"`
	Error            string          `json:"error,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	Events           string          `json:"events,omitempty"`
}
