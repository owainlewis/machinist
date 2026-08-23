package protocol

import (
	"encoding/json"
	"time"
)

const (
	CapabilityKindTool        = "tool"
	CapabilityKindRuntime     = "runtime"
	CapabilityReady           = "ready"
	CapabilityMissing         = "missing"
	CapabilityUnauthenticated = "unauthenticated"
	CapabilityUnhealthy       = "unhealthy"
	RuntimePi                 = "pi"
	RuntimeCodex              = "codex"
	RuntimeClaudeCode         = "claude-code"
	MaxBodyBytes              = 1 << 20
	// MaxClaimStageBytes leaves half of a claim response available for the
	// Attempt, execution, Session, and repository metadata around its stages.
	MaxClaimStageBytes        = MaxBodyBytes / 2
	MaxEventBatchBytes        = 256 << 10
	MaxEventBytes             = 64 << 10
	MaxEventsPerBatch         = 100
	MaxAttemptEventBytes      = 10 << 20
	MaxResultBytes            = 256 << 10
	MaxErrorBytes             = 64 << 10
	DefaultTimeout            = 2 * time.Hour
	MaxTimeout                = 8 * time.Hour
	LeaseDuration             = 30 * time.Second
	EmptyClaimTTL             = 5 * time.Minute
	WorkerOnlineWindow        = 30 * time.Second
	MaxRetainedPerRepo        = 10
	MaxManagedRepositories    = 1000
	MaxRepositoryCacheEntries = 100
	DefaultEventPageSize      = 100
	MaxEventPageSize          = 500
	MinWorkerCapacity         = 1
	MaxWorkerCapacity         = 100
	// ClaimProtocolVersion moved to 4 when claims began carrying a frozen
	// sequence of Pipeline stages. Mixed server and Worker versions are not
	// supported, including for compatibility process-exit Work.
	ClaimProtocolVersion = 4
)

func SupportedRuntime(value string) bool {
	return value == RuntimePi || value == RuntimeCodex || value == RuntimeClaudeCode
}

func SupportedRuntimes() []string {
	return []string{RuntimePi, RuntimeCodex, RuntimeClaudeCode}
}

type Capability struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
}

type RepositoryRegistration struct {
	Key            string `json:"key"`
	RemoteIdentity string `json:"remote_identity"`
	RetainedCount  int    `json:"retained_count"`
}

type RetainedWorktree struct {
	AttemptID      string `json:"attempt_id"`
	RepositoryID   string `json:"repository_id"`
	Path           string `json:"path"`
	Reason         string `json:"reason"`
	CleanupCommand string `json:"cleanup_command"`
}

type SourceAccess struct {
	Provider string `json:"provider"`
	Hostname string `json:"hostname"`
}

type WeeklyLimit struct {
	UsedPercent int       `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at"`
}

type WorkerRegistration struct {
	Name                       string                   `json:"name"`
	Labels                     map[string]string        `json:"labels,omitempty"`
	WorkerVersion              string                   `json:"worker_version"`
	ClaimProtocolVersion       int                      `json:"claim_protocol_version,omitempty"`
	Runtime                    string                   `json:"runtime"`
	RuntimeVersion             string                   `json:"runtime_version"`
	Capabilities               []Capability             `json:"capabilities,omitempty"`
	Capacity                   int                      `json:"capacity"`
	ActiveCount                int                      `json:"active_count"`
	Health                     string                   `json:"health"`
	Repositories               []RepositoryRegistration `json:"repositories"`
	SourceAccess               []SourceAccess           `json:"source_access,omitempty"`
	AcceptsManagedRepositories bool                     `json:"accepts_managed_repositories,omitempty"`
	ManagedRepositoryIDs       []string                 `json:"managed_repository_ids,omitempty"`
	RetainedWorktrees          []RetainedWorktree       `json:"retained_worktrees"`
	CapacityHandoffVersion     int                      `json:"capacity_handoff_version,omitempty"`
	DisposedAttemptIDs         []string                 `json:"disposed_attempt_ids,omitempty"`
	WeeklyLimit                *WeeklyLimit             `json:"weekly_limit,omitempty"`
}

type Repository struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	RemoteIdentity string `json:"remote_identity"`
	RetainedCount  int    `json:"retained_count"`
}

type ManagedRepository struct {
	ID             string    `json:"id"`
	RemoteIdentity string    `json:"remote_identity"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ManagedRepositoryReadiness struct {
	RoutingReady bool                               `json:"routing_ready"`
	Workers      []ManagedRepositoryWorkerReadiness `json:"workers"`
}

type ManagedRepositoryWorkerReadiness struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Cached     bool   `json:"cached"`
	Advertised bool   `json:"advertised"`
	Ready      bool   `json:"ready"`
	Reason     string `json:"reason"`
}

type WorkerRepositoryOption struct {
	ID             string `json:"id"`
	Key            string `json:"key,omitempty"`
	RemoteIdentity string `json:"remote_identity"`
	Enabled        bool   `json:"enabled"`
	Cached         bool   `json:"cached"`
	Advertised     bool   `json:"advertised"`
	Ready          bool   `json:"ready"`
	Reason         string `json:"reason"`
}

type CreateManagedRepositoryRequest struct {
	RemoteIdentity string `json:"remote_identity"`
}

type SetManagedRepositoryEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

type Worker struct {
	ID                         string             `json:"id"`
	Name                       string             `json:"name"`
	Labels                     map[string]string  `json:"labels,omitempty"`
	WorkerVersion              string             `json:"worker_version"`
	Runtime                    string             `json:"runtime"`
	RuntimeVersion             string             `json:"runtime_version"`
	Capabilities               []Capability       `json:"capabilities,omitempty"`
	Capacity                   int                `json:"capacity"`
	ActiveCount                int                `json:"active_count"`
	Health                     string             `json:"health"`
	Online                     bool               `json:"online"`
	Synthetic                  bool               `json:"synthetic"`
	Repositories               []Repository       `json:"repositories"`
	SourceAccess               []SourceAccess     `json:"source_access,omitempty"`
	AcceptsManagedRepositories bool               `json:"accepts_managed_repositories,omitempty"`
	RepositoryCacheCount       int                `json:"repository_cache_count,omitempty"`
	RetainedWorktrees          []RetainedWorktree `json:"retained_worktrees"`
	CurrentRunTitle            string             `json:"current_run_title,omitempty"`
	RegisteredAt               time.Time          `json:"registered_at"`
	LastHeartbeat              time.Time          `json:"last_heartbeat"`
}

type WorkerSummary struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Runtime       string    `json:"runtime"`
	Capacity      int       `json:"capacity"`
	ActiveCount   int       `json:"active_count"`
	Health        string    `json:"health"`
	Online        bool      `json:"online"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

type WorkerSummaryPage struct {
	Workers []WorkerSummary `json:"workers"`
}

type WorkerEnrollment struct {
	WorkerID        string    `json:"worker_id"`
	EnrollmentToken string    `json:"enrollment_token"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type CreateWorkerEnrollmentRequest struct {
	WorkerID string `json:"worker_id"`
}

type ExchangeWorkerEnrollmentRequest struct {
	WorkerID        string `json:"worker_id"`
	EnrollmentToken string `json:"enrollment_token"`
	Credential      string `json:"credential"`
}

type WorkerCredential struct {
	Credential string `json:"credential"`
}

type Attempt struct {
	ID              string     `json:"id"`
	ExecutionID     string     `json:"execution_id"`
	WorkerID        string     `json:"worker_id"`
	AttemptNumber   int        `json:"attempt_number"`
	State           string     `json:"state"`
	LeaseExpiresAt  time.Time  `json:"lease_expires_at"`
	SupervisorPID   *int64     `json:"supervisor_pid,omitempty"`
	ProcessIdentity string     `json:"process_identity,omitempty"`
	ProcessGroupID  *int64     `json:"process_group_id,omitempty"`
	Result          string     `json:"result,omitempty"`
	Error           string     `json:"error,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type ClaimRequest struct {
	RequestID  string `json:"request_id"`
	LeaseToken string `json:"lease_token"`
}

type Claim struct {
	Attempt    Attempt          `json:"attempt"`
	Execution  SessionExecution `json:"execution"`
	Session    ClaimedSession   `json:"session"`
	Repository Repository       `json:"repository"`
}

type LeaseRequest struct {
	LeaseToken string `json:"lease_token"`
}

type StartAttemptRequest struct {
	LeaseToken      string `json:"lease_token"`
	SupervisorPID   *int64 `json:"supervisor_pid,omitempty"`
	ProcessIdentity string `json:"process_identity,omitempty"`
	ProcessGroupID  *int64 `json:"process_group_id,omitempty"`
}

type HeartbeatResponse struct {
	LeaseExpiresAt        time.Time `json:"lease_expires_at"`
	CancellationRequested bool      `json:"cancellation_requested"`
}

type AttemptEvent struct {
	Sequence   int64           `json:"sequence"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
	ServerTime time.Time       `json:"server_time,omitempty"`
}

type AttemptEventPage struct {
	Events    []AttemptEvent
	NextAfter int64
	HasMore   bool
}

type EventBatchRequest struct {
	LeaseToken string         `json:"lease_token"`
	Events     []AttemptEvent `json:"events"`
}

type CompleteAttemptRequest struct {
	LeaseToken string `json:"lease_token"`
	State      string `json:"state"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
}

type ErrorBody struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code            string          `json:"code"`
	Message         string          `json:"message"`
	AdmissionResult AdmissionResult `json:"admission_result,omitempty"`
	RequestKey      string          `json:"request_key,omitempty"`
}
