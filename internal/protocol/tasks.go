package protocol

import (
	"encoding/json"
	"time"
)

const (
	MaxTasks                = 500
	MaxPipelines            = 200
	MaxPipelineStages       = 20
	MaxTaskRepositories     = 100
	MaxTaskPromptBytes      = 64 * 1024
	MaxWorkTargets          = 100
	MaxProgressBytes        = 2 * 1024
	MaxOutcomeBytes         = 8 * 1024
	MaxWaitingReasonBytes   = 2 * 1024
	MaxTerminalMessageBytes = 8 * 1024
	MaxQuestionBytes        = 8 * 1024
	MaxAnswerBytes          = 8 * 1024
	MaxUpdatesPerAttempt    = 200
	MaxProgressPerAttempt   = 199
)

const DefaultPipelineID = "00000000-0000-0000-0000-000000000001"

type PipelineStage struct {
	Position int    `json:"position"`
	Name     string `json:"name"`
	Prompt   string `json:"prompt"`
}

type Pipeline struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Generation int             `json:"generation"`
	Stages     []PipelineStage `json:"stages"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type PipelinePage struct {
	Pipelines []Pipeline `json:"pipelines"`
}

type SavePipelineRequest struct {
	Name               string          `json:"name"`
	Stages             []PipelineStage `json:"stages"`
	ExpectedGeneration int             `json:"expected_generation,omitempty"`
}

type PipelineSnapshot struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Generation int             `json:"generation"`
	Stages     []PipelineStage `json:"stages"`
}

type StageRunState string

const (
	StagePending   StageRunState = "pending"
	StageRunning   StageRunState = "running"
	StageSucceeded StageRunState = "succeeded"
	StageFailed    StageRunState = "failed"
	StageCancelled StageRunState = "cancelled"
)

type StageRun struct {
	Position    int           `json:"position"`
	Name        string        `json:"name"`
	Prompt      string        `json:"prompt,omitempty"`
	State       StageRunState `json:"state"`
	Result      string        `json:"result,omitempty"`
	Error       string        `json:"error,omitempty"`
	StartedAt   *time.Time    `json:"started_at,omitempty"`
	CompletedAt *time.Time    `json:"completed_at,omitempty"`
}

type StartStageRequest struct {
	LeaseToken      string `json:"lease_token"`
	SupervisorPID   *int64 `json:"supervisor_pid,omitempty"`
	ProcessIdentity string `json:"process_identity,omitempty"`
	ProcessGroupID  *int64 `json:"process_group_id,omitempty"`
}

type CompleteStageRequest struct {
	LeaseToken string        `json:"lease_token"`
	State      StageRunState `json:"state"`
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
}

type OutcomeContract string

const (
	OutcomeProcessExit OutcomeContract = "process_exit"
	OutcomeAgentUpdate OutcomeContract = "agent_update"
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
	OutcomeContract    OutcomeContract  `json:"outcome_contract"`
	PipelineID         string           `json:"pipeline_id"`
	PipelineName       string           `json:"pipeline_name"`
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
	ID                    string          `json:"id"`
	RunID                 string          `json:"run_id"`
	TaskName              string          `json:"task_name"`
	Prompt                string          `json:"prompt"`
	Stages                []StageRun      `json:"stages"`
	WorkerID              string          `json:"worker_id"`
	RepositoryID          string          `json:"repository_id"`
	RequiredRuntime       string          `json:"required_runtime"`
	TimeoutSeconds        int             `json:"timeout_seconds"`
	OutcomeContract       OutcomeContract `json:"outcome_contract"`
	Target                WorkTarget      `json:"target"`
	CheckpointSHA         string          `json:"checkpoint_sha,omitempty"`
	PendingResumeSHA      string          `json:"pending_resume_sha,omitempty"`
	CheckpointPublished   bool            `json:"checkpoint_published,omitempty"`
	PullRequestURL        string          `json:"pull_request_url,omitempty"`
	PullRequestHeadBranch string          `json:"pull_request_head_branch,omitempty"`
	PullRequestHeadSHA    string          `json:"pull_request_head_sha,omitempty"`
	State                 string          `json:"state"`
	AdmittedAt            time.Time       `json:"admitted_at"`
}

type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type SaveTaskRequest struct {
	RequestKey         string          `json:"request_key,omitempty"`
	Name               string          `json:"name"`
	Prompt             string          `json:"prompt"`
	Runtime            string          `json:"runtime"`
	ExecutionProfileID string          `json:"execution_profile_id,omitempty"`
	TimeoutSeconds     int             `json:"timeout_seconds"`
	ConcurrencyLimit   int             `json:"concurrency_limit"`
	RepositoryIDs      []string        `json:"repository_ids"`
	Schedule           TaskSchedule    `json:"schedule"`
	ExpectedGeneration int             `json:"expected_generation,omitempty"`
	OutcomeContract    OutcomeContract `json:"outcome_contract,omitempty"`
	PipelineID         string          `json:"pipeline_id,omitempty"`
}

type SetTaskArchivedRequest struct {
	Archived           *bool `json:"archived"`
	ExpectedGeneration int   `json:"expected_generation"`
}

type SetTaskOutcomeContractRequest struct {
	OutcomeContract    OutcomeContract `json:"outcome_contract"`
	ExpectedGeneration int             `json:"expected_generation"`
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
	SessionBlocked    SessionState = "blocked"
	SessionQueued     SessionState = "queued"
	SessionPreparing  SessionState = "preparing"
	SessionRunning    SessionState = "running"
	SessionNeedsInput SessionState = "needs-input"
	SessionReady      SessionState = "ready"
	SessionSucceeded  SessionState = "succeeded"
	SessionFailed     SessionState = "failed"
	SessionNoChange   SessionState = "no-change"
	SessionCancelled  SessionState = "cancelled"
)

type WorkState = SessionState

const (
	WorkQueued     WorkState = SessionQueued
	WorkRunning    WorkState = SessionRunning
	WorkNeedsInput WorkState = SessionNeedsInput
	WorkReady      WorkState = SessionReady
	WorkSucceeded  WorkState = SessionSucceeded
	WorkFailed     WorkState = SessionFailed
	WorkNoChange   WorkState = SessionNoChange
	WorkCancelled  WorkState = SessionCancelled
)

type ExecutionOwner string

const (
	ExecutionOwnerNone          ExecutionOwner = "none"
	ExecutionOwnerWorkerAttempt ExecutionOwner = "worker_attempt"
	ExecutionOwnerOperator      ExecutionOwner = "operator"
)

type WorkUpdateStatus string

const (
	WorkUpdateRunning    WorkUpdateStatus = "running"
	WorkUpdateReady      WorkUpdateStatus = "ready"
	WorkUpdateNeedsInput WorkUpdateStatus = "needs-input"
	WorkUpdateFailed     WorkUpdateStatus = "failed"
	WorkUpdateNoChange   WorkUpdateStatus = "no-change"
)

func SupportedWorkUpdateStatus(status WorkUpdateStatus) bool {
	return status == WorkUpdateRunning || status == WorkUpdateReady ||
		status == WorkUpdateNeedsInput || status == WorkUpdateFailed || status == WorkUpdateNoChange
}

type WorkUpdateActor string

const (
	WorkUpdateActorAgent    WorkUpdateActor = "agent"
	WorkUpdateActorOperator WorkUpdateActor = "operator"
	WorkUpdateActorSystem   WorkUpdateActor = "system"
)

func SupportedWorkUpdateActor(actor WorkUpdateActor) bool {
	return actor == WorkUpdateActorAgent || actor == WorkUpdateActorOperator || actor == WorkUpdateActorSystem
}

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
	OutcomeContract    OutcomeContract  `json:"outcome_contract"`
	Pipeline           PipelineSnapshot `json:"pipeline"`
	Repositories       []TaskRepository `json:"repositories,omitempty"`
	ScheduleCron       string           `json:"cron,omitempty"`
	ScheduleTimezone   string           `json:"timezone,omitempty"`
}

func (snapshot *TaskSnapshot) UnmarshalJSON(data []byte) error {
	type taskSnapshot TaskSnapshot
	var decoded taskSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Pipeline.ID == "" {
		decoded.Pipeline = PipelineSnapshot{
			ID:         DefaultPipelineID,
			Name:       "Single agent",
			Generation: 1,
			Stages: []PipelineStage{{
				Position: 0,
				Name:     "Do the task",
				Prompt:   decoded.Prompt,
			}},
		}
	}
	*snapshot = TaskSnapshot(decoded)
	return nil
}

// ProcedureSnapshot is the product name for the immutable Task snapshot.
// The alias keeps existing Task and scheduled Run clients source compatible.
type ProcedureSnapshot = TaskSnapshot

type WorkTarget struct {
	ID                 string `json:"id"`
	Position           int    `json:"position"`
	TargetKey          string `json:"target_key"`
	TargetKind         string `json:"target_kind"`
	RepositoryID       string `json:"repository_id"`
	RepositoryIdentity string `json:"repository_identity"`
	SourceKind         string `json:"source_kind"`
	SourceKey          string `json:"source_key"`
	SourceReference    string `json:"source_reference"`
	ContextSnapshot    string `json:"context_snapshot,omitempty"`
	PublishBranch      string `json:"publish_branch"`
}

type WorkUpdate struct {
	ID                    string           `json:"id"`
	WorkID                string           `json:"work_id"`
	AttemptID             string           `json:"attempt_id,omitempty"`
	RequestID             string           `json:"request_id"`
	Sequence              int              `json:"sequence"`
	Status                WorkUpdateStatus `json:"status"`
	Message               string           `json:"message"`
	PullRequestURL        string           `json:"pull_request_url,omitempty"`
	PullRequestHeadBranch string           `json:"pull_request_head_branch,omitempty"`
	PullRequestHeadSHA    string           `json:"pull_request_head_sha,omitempty"`
	CheckpointSHA         string           `json:"checkpoint_sha,omitempty"`
	CheckpointPublished   bool             `json:"checkpoint_published,omitempty"`
	Actor                 WorkUpdateActor  `json:"actor"`
	AcceptedAt            time.Time        `json:"accepted_at"`
}

// AgentUpdateRequest is the credential-free request sent from the injected
// factory helper to the Worker-local socket. The token scopes the request to
// one Attempt; Worker and lease credentials are deliberately absent.
type AgentUpdateRequest struct {
	WorkID         string           `json:"work_id"`
	AttemptID      string           `json:"attempt_id"`
	UpdateToken    string           `json:"update_token"`
	RequestID      string           `json:"request_id"`
	Status         WorkUpdateStatus `json:"status"`
	Message        string           `json:"message"`
	PullRequestURL string           `json:"pull_request_url,omitempty"`
}

// AttemptUpdateRequest is the Worker-to-control-plane form of an agent
// update. Delivery evidence has already been checked by the Worker for new
// requests. ReplayOnly asks for a durable exact-request lookup before mutable
// delivery state is checked again. The lease token never crosses the
// Worker-local update protocol.
type AttemptUpdateRequest struct {
	LeaseToken            string           `json:"lease_token"`
	ReplayOnly            bool             `json:"replay_only,omitempty"`
	RequestID             string           `json:"request_id"`
	Status                WorkUpdateStatus `json:"status"`
	Message               string           `json:"message"`
	PullRequestURL        string           `json:"pull_request_url,omitempty"`
	PullRequestHeadBranch string           `json:"pull_request_head_branch,omitempty"`
	PullRequestHeadSHA    string           `json:"pull_request_head_sha,omitempty"`
	CheckpointSHA         string           `json:"checkpoint_sha,omitempty"`
	CheckpointPublished   bool             `json:"checkpoint_published,omitempty"`
}

type WorkAnswerRequest struct {
	RequestID string `json:"request_id"`
	Message   string `json:"message"`
}

type WorkAnswer struct {
	ID               string    `json:"id"`
	WorkID           string    `json:"work_id"`
	QuestionUpdateID string    `json:"question_update_id"`
	RequestID        string    `json:"request_id"`
	Message          string    `json:"message"`
	AcceptedAt       time.Time `json:"accepted_at"`
}

type ReplaceWorkRequest struct {
	RequestKey string `json:"request_key"`
	WorkID     string `json:"work_id"`
}

type WorkReplacement struct {
	Result     AdmissionResult `json:"result"`
	RequestKey string          `json:"request_key"`
	Run        RunDetail       `json:"run"`
}

type WorkUpdatePage struct {
	Updates   []WorkUpdate `json:"updates"`
	NextAfter int          `json:"next_after,omitempty"`
	HasMore   bool         `json:"has_more"`
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
	Target                WorkTarget        `json:"target"`
	PredecessorWorkID     string            `json:"predecessor_work_id,omitempty"`
	ExecutionOwner        ExecutionOwner    `json:"execution_owner"`
	WaitingReason         string            `json:"waiting_reason,omitempty"`
	LatestProgress        string            `json:"latest_progress,omitempty"`
	Question              string            `json:"question,omitempty"`
	CheckpointSHA         string            `json:"checkpoint_sha,omitempty"`
	PendingResumeSHA      string            `json:"pending_resume_sha,omitempty"`
	CheckpointPublished   bool              `json:"checkpoint_published,omitempty"`
	Answer                string            `json:"answer,omitempty"`
	PullRequestURL        string            `json:"pull_request_url,omitempty"`
	PullRequestHeadBranch string            `json:"pull_request_head_branch,omitempty"`
	PullRequestHeadSHA    string            `json:"pull_request_head_sha,omitempty"`
	TerminalMessage       string            `json:"terminal_message,omitempty"`
	Stages                []StageRun        `json:"stages,omitempty"`
	Updates               []WorkUpdate      `json:"updates,omitempty"`
	Attempts              []Attempt         `json:"attempts,omitempty"`
}

// Work is the product-facing name for the durable Session-backed lifecycle.
type Work = Session

type Run struct {
	ID              string            `json:"id"`
	TaskID          string            `json:"task_id"`
	Task            TaskSnapshot      `json:"task"`
	Execution       ExecutionSnapshot `json:"execution"`
	OutcomeContract OutcomeContract   `json:"outcome_contract"`
	Targets         []WorkTarget      `json:"targets"`
	Source          string            `json:"source"`
	ScheduledAt     *time.Time        `json:"scheduled_at,omitempty"`
	State           RunState          `json:"state"`
	NeedsAttention  bool              `json:"needs_attention"`
	SessionCount    int               `json:"session_count"`
	SucceededCount  int               `json:"succeeded_count"`
	ReadyCount      int               `json:"ready_count"`
	NeedsInputCount int               `json:"needs_input_count"`
	NoChangeCount   int               `json:"no_change_count"`
	FailedCount     int               `json:"failed_count"`
	CancelledCount  int               `json:"cancelled_count"`
	ActiveCount     int               `json:"active_count"`
	AdmittedAt      time.Time         `json:"admitted_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	TerminalAt      *time.Time        `json:"terminal_at,omitempty"`
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

type RunListSummary struct {
	ID              string     `json:"id"`
	TaskName        string     `json:"task_name"`
	State           RunState   `json:"state"`
	Source          string     `json:"source"`
	NeedsAttention  bool       `json:"needs_attention"`
	SessionCount    int        `json:"session_count"`
	SucceededCount  int        `json:"succeeded_count"`
	ReadyCount      int        `json:"ready_count"`
	NeedsInputCount int        `json:"needs_input_count"`
	NoChangeCount   int        `json:"no_change_count"`
	FailedCount     int        `json:"failed_count"`
	CancelledCount  int        `json:"cancelled_count"`
	ActiveCount     int        `json:"active_count"`
	AdmittedAt      time.Time  `json:"admitted_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	TerminalAt      *time.Time `json:"terminal_at,omitempty"`
}

type RunListPage struct {
	Runs       []RunListSummary `json:"runs"`
	NextCursor string           `json:"next_cursor,omitempty"`
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
