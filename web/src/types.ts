export type Runtime = "pi" | "codex" | "claude-code";
export type ExecutionBackend = "persistent" | "fake_cloud_run";
export type SessionState = "blocked" | "queued" | "preparing" | "running" | "needs-input" | "ready" | "succeeded" | "failed" | "no-change" | "cancelled";
export type RunState = "blocked" | "queued" | "running" | "succeeded" | "failed" | "partial" | "cancelled";

export interface ExecutionProfile {
  id: string;
  name: string;
  kind: ExecutionBackend;
  version: number;
  runtime: Runtime | "";
  provider: string;
  model: string;
  timeout_seconds: number;
  resource_class: string;
  max_concurrent: number;
  enabled: boolean;
  healthy: boolean;
  health_reason?: string;
  synthetic_worker_id: string;
}

export interface ExecutionSnapshot {
  profile_id: string;
  profile_version: number;
  backend: ExecutionBackend;
  runtime: Runtime;
  provider: string;
  model: string;
  timeout_seconds: number;
  resource_class: string;
  commit_resolution_policy: "resolve_per_attempt" | "frozen_commit";
}

export interface TaskRepository {
  id: string;
  remote_identity: string;
}

export interface TaskSchedule {
  enabled: boolean;
  cron?: string;
  timezone?: string;
  next_due_at?: string;
  pending_due_at?: string;
  health_status: "disabled" | "healthy" | "blocked" | "error";
  health_code?: string;
  health_message?: string;
}

export interface Task {
  id: string;
  name: string;
  prompt: string;
  prompt_preview?: string;
  runtime: Runtime;
  execution_profile_id?: string;
  timeout_seconds: number;
  concurrency_limit: number;
  generation: number;
  pipeline_id?: string;
  pipeline_name?: string;
  archived: boolean;
  read_only: boolean;
  repositories: TaskRepository[] | null;
  repository_count: number;
  schedule: TaskSchedule;
  last_run_state?: RunState;
  created_at: string;
  updated_at: string;
}

export interface SaveTaskInput {
  name: string;
  prompt: string;
  runtime: Runtime;
  execution_profile_id?: string;
  timeout_seconds: number;
  concurrency_limit: number;
  repository_ids: string[];
  schedule: { enabled: boolean; cron?: string; timezone?: string };
  expected_generation?: number;
  pipeline_id?: string;
}

export interface PipelineStage {
  position: number;
  name: string;
  prompt: string;
}

export interface Pipeline {
  id: string;
  name: string;
  generation: number;
  stages: PipelineStage[];
  created_at: string;
  updated_at: string;
}

export interface SavePipelineInput {
  name: string;
  stages: Array<{ name: string; prompt: string }>;
  expected_generation?: number;
}

export interface StageRun extends PipelineStage {
  state: "pending" | "running" | "succeeded" | "failed" | "cancelled";
  result?: string;
  error?: string;
  started_at?: string;
  completed_at?: string;
}

export interface TaskSnapshot {
  id: string;
  name: string;
  prompt: string;
  runtime: Runtime;
  execution_profile_id?: string;
  timeout_seconds: number;
  concurrency_limit: number;
  generation: number;
  repositories: TaskRepository[] | null;
  cron?: string;
  timezone?: string;
  pipeline?: {
    id: string;
    name: string;
    generation: number;
    stages: PipelineStage[];
  };
}

export interface Attempt {
  id: string;
  execution_id: string;
  worker_id: string;
  attempt_number: number;
  state: "preparing" | "running" | "succeeded" | "failed" | "cancelled" | "lost";
  lease_expires_at: string;
  result?: string;
  error?: string;
  started_at?: string;
  completed_at?: string;
  created_at: string;
}

export interface Session {
  id: string;
  run_id: string;
  repository_id: string;
  repository_identity: string;
  resolved_prompt?: string;
  required_runtime: Runtime;
  execution: ExecutionSnapshot;
  timeout_seconds: number;
  state: SessionState;
  blocked_reason?: string;
  assigned_worker_id?: string;
  cancellation_requested: boolean;
  retry_may_repeat_effects: boolean;
  admitted_at: string;
  started_at?: string;
  terminal_at?: string;
  result?: string;
  failure_reason?: string;
  stages?: StageRun[] | null;
  attempts?: Attempt[] | null;
}

export interface Run {
  id: string;
  task_id: string;
  task: TaskSnapshot;
  execution: ExecutionSnapshot;
  source: "manual" | "schedule" | "provider_history";
  scheduled_at?: string;
  state: RunState;
  needs_attention: boolean;
  session_count: number;
  succeeded_count: number;
  ready_count: number;
  needs_input_count: number;
  no_change_count: number;
  failed_count: number;
  cancelled_count: number;
  active_count: number;
  admitted_at: string;
  updated_at: string;
  terminal_at?: string;
}

export interface RunDetail {
  run: Run;
  sessions: Session[] | null;
}

export interface RunPage {
  runs: Run[] | null;
  next_cursor?: string;
}

export interface Overview {
  active_runs: number;
  needs_attention: number;
  completed_last_24h: number;
  workers_online: number;
  workers_total: number;
  run_metrics: {
    window: "24h";
    total_runs: number;
    completed_runs: number;
    completion_rate: number | null;
    average_queue_time_seconds: number | null;
    average_cycle_time_seconds: number | null;
  };
  recent_runs: Run[] | null;
  upcoming_tasks: Task[] | null;
  generated_at: string;
}

export interface Capability {
  kind: "tool" | "runtime";
  name: string;
  status: "ready" | "missing" | "unauthenticated" | "unhealthy";
  version?: string;
  message?: string;
}

export interface Repository {
  id: string;
  key: string;
  remote_identity: string;
  retained_count: number;
}

export interface RetainedWorktree {
  attempt_id: string;
  repository_id: string;
  path: string;
  reason: string;
  cleanup_command: string;
}

export interface Worker {
  id: string;
  name: string;
  labels?: Record<string, string>;
  worker_version: string;
  runtime: Runtime;
  runtime_version: string;
  capabilities?: Capability[];
  capacity: number;
  active_count: number;
  health: "healthy" | "unhealthy";
  online: boolean;
  repositories: Repository[];
  source_access?: Array<{ provider: string; hostname: string }>;
  accepts_managed_repositories?: boolean;
  repository_cache_count?: number;
  retained_worktrees: RetainedWorktree[];
  registered_at: string;
  last_heartbeat: string;
  current_run_title?: string;
}

export interface ManagedRepository {
  id: string;
  remote_identity: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface ManagedRepositoryWorkerReadiness {
  id: string;
  name: string;
  cached: boolean;
  advertised: boolean;
  ready: boolean;
  reason: string;
}

export interface ManagedRepositoryReadiness {
  routing_ready: boolean;
  workers: ManagedRepositoryWorkerReadiness[];
}

export interface AttemptEvent {
  sequence: number;
  kind: string;
  payload: unknown;
  server_time: string;
}

export interface AttemptEventPage {
  events: AttemptEvent[];
  next_after: number;
  has_more: boolean;
}

export interface APIErrorBody {
  error: { code: string; message: string };
}
