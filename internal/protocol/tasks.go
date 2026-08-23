package protocol

import (
	"encoding/json"
	"time"
)

const (
	MaxTasks            = 500
	MaxTaskRepositories = 100
	MaxTaskPromptBytes  = 64 * 1024
)

type TaskSchedule struct {
	Enabled       bool       `json:"enabled"`
	Cron          string     `json:"cron,omitempty"`
	Timezone      string     `json:"timezone,omitempty"`
	NextDueAt     *time.Time `json:"next_due_at,omitempty"`
	PendingDueAt  *time.Time `json:"pending_due_at,omitempty"`
	HealthStatus  string     `json:"health_status"`
	HealthCode    string     `json:"health_code,omitempty"`
	HealthMessage string     `json:"health_message,omitempty"`
}

type TaskRepository struct {
	ID             string `json:"id"`
	RemoteIdentity string `json:"remote_identity"`
}

type Task struct {
	ID                 string           `json:"id"`
	Name               string           `json:"name"`
	Prompt             string           `json:"prompt,omitempty"`
	PromptPreview      string           `json:"prompt_preview,omitempty"`
	Runtime            string           `json:"runtime"`
	ExecutionProfileID string           `json:"execution_profile_id,omitempty"`
	TimeoutSeconds     int              `json:"timeout_seconds"`
	ConcurrencyLimit   int              `json:"concurrency_limit"`
	Generation         int              `json:"generation"`
	Archived           bool             `json:"archived"`
	ReadOnly           bool             `json:"read_only"`
	Repositories       []TaskRepository `json:"repositories"`
	RepositoryCount    int              `json:"repository_count"`
	Schedule           TaskSchedule     `json:"schedule"`
	LastRunState       string           `json:"last_run_state,omitempty"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

// SessionExecution is the worker-facing execution record for one Session.
type SessionExecution struct {
	ID                    string    `json:"id"`
	SessionID             string    `json:"session_id"`
	AssignedWorkerID      string    `json:"assigned_worker_id"`
	RequiredRuntime       string    `json:"required_runtime"`
	State                 string    `json:"state"`
	CancellationRequested bool      `json:"cancellation_requested"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// ClaimedSession contains the immutable input a Worker needs to execute a
// single repository session. It intentionally uses only the Tasks model.
type ClaimedSession struct {
	ID              string    `json:"id"`
	RunID           string    `json:"run_id"`
	TaskName        string    `json:"task_name"`
	Prompt          string    `json:"prompt"`
	WorkerID        string    `json:"worker_id"`
	RepositoryID    string    `json:"repository_id"`
	RequiredRuntime string    `json:"required_runtime"`
	TimeoutSeconds  int       `json:"timeout_seconds"`
	State           string    `json:"state"`
	AdmittedAt      time.Time `json:"admitted_at"`
}

type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type SaveTaskRequest struct {
	RequestKey         string       `json:"request_key,omitempty"`
	Name               string       `json:"name"`
	Prompt             string       `json:"prompt"`
	Runtime            string       `json:"runtime"`
	ExecutionProfileID string       `json:"execution_profile_id,omitempty"`
	TimeoutSeconds     int          `json:"timeout_seconds"`
	ConcurrencyLimit   int          `json:"concurrency_limit"`
	RepositoryIDs      []string     `json:"repository_ids"`
	Schedule           TaskSchedule `json:"schedule"`
	ExpectedGeneration int          `json:"expected_generation,omitempty"`
}

type SetTaskArchivedRequest struct {
	Archived           *bool `json:"archived"`
	ExpectedGeneration int   `json:"expected_generation"`
}

type RunTaskRequest struct {
	RequestKey         string `json:"request_key"`
	ExecutionProfileID string `json:"execution_profile_id,omitempty"`
}

type DiscardTaskOccurrenceRequest struct {
	PendingDueAt time.Time `json:"pending_due_at"`
}

type SessionState string

const (
	SessionBlocked   SessionState = "blocked"
	SessionQueued    SessionState = "queued"
	SessionPreparing SessionState = "preparing"
	SessionRunning   SessionState = "running"
	SessionSucceeded SessionState = "succeeded"
	SessionFailed    SessionState = "failed"
	SessionCancelled SessionState = "cancelled"
)

type RunState string

const (
	RunQueued    RunState = "queued"
	RunBlocked   RunState = "blocked"
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
	RunPartial   RunState = "partial"
	RunCancelled RunState = "cancelled"
)

type TaskSnapshot struct {
	ID                 string           `json:"id"`
	Name               string           `json:"name"`
	Prompt             string           `json:"prompt,omitempty"`
	Runtime            string           `json:"runtime"`
	ExecutionProfileID string           `json:"execution_profile_id,omitempty"`
	TimeoutSeconds     int              `json:"timeout_seconds,omitempty"`
	ConcurrencyLimit   int              `json:"concurrency_limit,omitempty"`
	Generation         int              `json:"generation"`
	Repositories       []TaskRepository `json:"repositories,omitempty"`
	ScheduleCron       string           `json:"cron,omitempty"`
	ScheduleTimezone   string           `json:"timezone,omitempty"`
}

const (
	PersistentAutoProfileID = "persistent-auto"
	BackendPersistent       = "persistent"
	BackendFakeCloudRun     = "fake_cloud_run"
	CommitResolvePerAttempt = "resolve_per_attempt"
	CommitFrozen            = "frozen_commit"
)

type ExecutionSnapshot struct {
	ProfileID              string `json:"profile_id"`
	ProfileVersion         int    `json:"profile_version"`
	Backend                string `json:"backend"`
	Runtime                string `json:"runtime"`
	Provider               string `json:"provider"`
	Model                  string `json:"model"`
	TimeoutSeconds         int    `json:"timeout_seconds"`
	ResourceClass          string `json:"resource_class"`
	CommitResolutionPolicy string `json:"commit_resolution_policy"`
}

type ExecutionProfile struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Kind              string    `json:"kind"`
	Version           int       `json:"version"`
	Runtime           string    `json:"runtime"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	TimeoutSeconds    int       `json:"timeout_seconds"`
	ResourceClass     string    `json:"resource_class"`
	MaxConcurrent     int       `json:"max_concurrent"`
	Enabled           bool      `json:"enabled"`
	Healthy           bool      `json:"healthy"`
	HealthReason      string    `json:"health_reason,omitempty"`
	FakeOutcome       string    `json:"fake_outcome,omitempty"`
	FakeResult        string    `json:"fake_result,omitempty"`
	FakeError         string    `json:"fake_error,omitempty"`
	SyntheticWorkerID string    `json:"synthetic_worker_id"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type SaveExecutionProfileRequest struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Runtime         string `json:"runtime"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	ResourceClass   string `json:"resource_class"`
	MaxConcurrent   int    `json:"max_concurrent"`
	Enabled         bool   `json:"enabled"`
	Healthy         bool   `json:"healthy"`
	HealthReason    string `json:"health_reason,omitempty"`
	FakeOutcome     string `json:"fake_outcome,omitempty"`
	FakeResult      string `json:"fake_result,omitempty"`
	FakeError       string `json:"fake_error,omitempty"`
	ExpectedVersion int    `json:"expected_version,omitempty"`
}

type ExecutionProfilePage struct {
	Profiles []ExecutionProfile `json:"profiles"`
}

type Session struct {
	ID                    string            `json:"id"`
	RunID                 string            `json:"run_id"`
	RepositoryID          string            `json:"repository_id"`
	RepositoryIdentity    string            `json:"repository_identity"`
	ResolvedPrompt        string            `json:"resolved_prompt,omitempty"`
	RequiredRuntime       string            `json:"required_runtime"`
	Execution             ExecutionSnapshot `json:"execution"`
	TimeoutSeconds        int               `json:"timeout_seconds"`
	State                 SessionState      `json:"state"`
	BlockedReason         string            `json:"blocked_reason,omitempty"`
	AssignedWorkerID      string            `json:"assigned_worker_id,omitempty"`
	CancellationRequested bool              `json:"cancellation_requested"`
	RetryMayRepeatEffects bool              `json:"retry_may_repeat_effects"`
	AdmittedAt            time.Time         `json:"admitted_at"`
	StartedAt             *time.Time        `json:"started_at,omitempty"`
	TerminalAt            *time.Time        `json:"terminal_at,omitempty"`
	Result                string            `json:"result,omitempty"`
	FailureReason         string            `json:"failure_reason,omitempty"`
	Attempts              []Attempt         `json:"attempts,omitempty"`
}

type Run struct {
	ID             string            `json:"id"`
	TaskID         string            `json:"task_id"`
	Task           TaskSnapshot      `json:"task"`
	Execution      ExecutionSnapshot `json:"execution"`
	Source         string            `json:"source"`
	ScheduledAt    *time.Time        `json:"scheduled_at,omitempty"`
	State          RunState          `json:"state"`
	NeedsAttention bool              `json:"needs_attention"`
	SessionCount   int               `json:"session_count"`
	SucceededCount int               `json:"succeeded_count"`
	FailedCount    int               `json:"failed_count"`
	CancelledCount int               `json:"cancelled_count"`
	ActiveCount    int               `json:"active_count"`
	AdmittedAt     time.Time         `json:"admitted_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	TerminalAt     *time.Time        `json:"terminal_at,omitempty"`
}

type RunDetail struct {
	Run              Run             `json:"run"`
	ProviderSnapshot json.RawMessage `json:"provider_snapshot,omitempty"`
	Sessions         []Session       `json:"sessions"`
}

type RunSummary struct {
	ID         string              `json:"id"`
	TaskName   string              `json:"task_name"`
	State      RunState            `json:"state"`
	Source     string              `json:"source"`
	AdmittedAt time.Time           `json:"admitted_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
	Sessions   []RunSessionSummary `json:"sessions"`
}

type RunSessionSummary struct {
	ID                 string       `json:"id"`
	RepositoryIdentity string       `json:"repository_identity"`
	State              SessionState `json:"state"`
	BlockedReason      string       `json:"blocked_reason,omitempty"`
	AssignedWorkerID   string       `json:"assigned_worker_id,omitempty"`
	AttemptCount       int          `json:"attempt_count"`
	Result             string       `json:"result,omitempty"`
	FailureReason      string       `json:"failure_reason,omitempty"`
}

type RunPage struct {
	Runs       []Run  `json:"runs"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type OverviewRunMetrics struct {
	Window                  string   `json:"window"`
	TotalRuns               int      `json:"total_runs"`
	CompletedRuns           int      `json:"completed_runs"`
	CompletionRate          *float64 `json:"completion_rate"`
	AverageQueueTimeSeconds *float64 `json:"average_queue_time_seconds"`
	AverageCycleTimeSeconds *float64 `json:"average_cycle_time_seconds"`
}

type Overview struct {
	ActiveRuns       int                `json:"active_runs"`
	NeedsAttention   int                `json:"needs_attention"`
	CompletedLast24H int                `json:"completed_last_24h"`
	WorkersOnline    int                `json:"workers_online"`
	WorkersTotal     int                `json:"workers_total"`
	RunMetrics       OverviewRunMetrics `json:"run_metrics"`
	RecentRuns       []Run              `json:"recent_runs"`
	UpcomingTasks    []Task             `json:"upcoming_tasks"`
	GeneratedAt      time.Time          `json:"generated_at"`
}
