package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	_ "modernc.org/sqlite"
)

var (
	ErrLeaseConflict = errors.New("run lease does not match")
	ErrRunState      = errors.New("run is not active")
)

const leaseDuration = 30 * time.Second

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Job struct {
	ID            string    `json:"id"`
	Prompt        string    `json:"prompt"`
	Repository    string    `json:"repository"`
	SelectionKind string    `json:"selection_kind"`
	SelectionName string    `json:"selection_name"`
	ScheduleName  string    `json:"schedule_name,omitempty"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Runs          []Run     `json:"runs"`
}

type Run struct {
	ID             string    `json:"id"`
	Step           int       `json:"step"`
	Agent          string    `json:"agent"`
	Executor       string    `json:"executor"`
	Model          string    `json:"model,omitempty"`
	State          string    `json:"state"`
	WorkerName     string    `json:"worker_name,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	Error          string    `json:"error,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	DurationMillis *int64    `json:"duration_millis,omitempty"`
	TokenUsage     *int64    `json:"token_usage,omitempty,string"`
}

type Worker struct {
	InstanceID   string    `json:"instance_id"`
	Name         string    `json:"name"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	Repositories []string  `json:"repositories"`
}

type Snapshot struct {
	Jobs    []Job    `json:"jobs"`
	Workers []Worker `json:"workers"`
}

type RunOutput struct {
	Result string
	Events string
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: time.Now}
	if err := store.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize(ctx context.Context) error {
	const schema = `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  prompt TEXT NOT NULL,
  repository TEXT NOT NULL,
  selection_kind TEXT NOT NULL,
  selection_name TEXT NOT NULL,
  schedule_name TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES jobs(id),
  step INTEGER NOT NULL,
  agent TEXT NOT NULL,
  agent_hash TEXT NOT NULL,
  executor TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '',
  repository TEXT NOT NULL,
  rendered_prompt TEXT NOT NULL,
  timeout_ms INTEGER NOT NULL,
  state TEXT NOT NULL,
  worker_instance TEXT,
  lease_token TEXT,
  lease_expires_at INTEGER,
  exit_code INTEGER,
  error TEXT,
  result TEXT,
  events TEXT,
  started_at TEXT,
  completed_at TEXT,
  duration_millis INTEGER,
  token_usage INTEGER,
  UNIQUE(job_id, step)
);
CREATE INDEX IF NOT EXISTS runs_dispatch ON runs(state, job_id, step);
CREATE TABLE IF NOT EXISTS workers (
  instance_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS worker_repositories (
  worker_instance TEXT NOT NULL REFERENCES workers(instance_id) ON DELETE CASCADE,
  repository TEXT NOT NULL,
  PRIMARY KEY(worker_instance, repository)
);
CREATE TABLE IF NOT EXISTS schedule_state (
  name TEXT PRIMARY KEY,
  next_run_at TEXT NOT NULL
);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	if err := s.addColumnIfMissing(ctx, "runs", "lease_expires_at", "INTEGER"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "runs", "model", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "runs", "duration_millis", "INTEGER"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "runs", "token_usage", "INTEGER"); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "jobs", "schedule_name", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_shepherd_repository_v2 ON jobs(repository) WHERE selection_kind='agent' AND selection_name='shepherd' AND state IN ('queued','running')`); err != nil {
		return fmt.Errorf("create scheduled job overlap guard: %w", err)
	}
	return s.migrateRunMetrics(ctx)
}

func (s *Store) addColumnIfMissing(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return fmt.Errorf("inspect %s schema: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("inspect %s schema: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect %s schema: %w", table, err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect %s schema: %w", table, err)
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition); err != nil {
		return fmt.Errorf("migrate %s schema: %w", table, err)
	}
	return nil
}

func (s *Store) migrateRunMetrics(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(result,''),COALESCE(started_at,''),COALESCE(completed_at,'') FROM runs WHERE (duration_millis IS NULL AND completed_at IS NOT NULL) OR (token_usage IS NULL AND result LIKE '%"token_usage"%')`)
	if err != nil {
		return fmt.Errorf("read persisted run metrics: %w", err)
	}
	type persistedResult struct {
		id        string
		result    string
		startedAt string
		endedAt   string
	}
	var results []persistedResult
	for rows.Next() {
		var result persistedResult
		if err := rows.Scan(&result.id, &result.result, &result.startedAt, &result.endedAt); err != nil {
			rows.Close()
			return fmt.Errorf("read persisted run metrics: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("read persisted run metrics: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read persisted run metrics: %w", err)
	}
	for _, result := range results {
		duration, tokenUsage := resultMetrics([]byte(result.result))
		if duration == nil {
			duration = elapsedMillis(result.startedAt, result.endedAt)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE runs SET duration_millis=COALESCE(duration_millis,?),token_usage=COALESCE(token_usage,?) WHERE id=?`, duration, tokenUsage, result.id); err != nil {
			return fmt.Errorf("migrate persisted run metrics: %w", err)
		}
	}
	return nil
}

func (s *Store) CreateJob(ctx context.Context, prompt, repository, kind, name string, agents []config.ResolvedAgent) (string, error) {
	if len(agents) == 0 {
		return "", errors.New("job must contain at least one agent")
	}
	jobID, err := randomID("job", 12)
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,prompt,repository,selection_kind,selection_name,state,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?)`, jobID, prompt, repository, kind, name, now, now); err != nil {
		return "", fmt.Errorf("insert job: %w", err)
	}
	for index, agent := range agents {
		runID, err := randomID("run", 12)
		if err != nil {
			return "", err
		}
		state := "pending"
		if index == 0 {
			state = "queued"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,step,agent,agent_hash,executor,model,repository,rendered_prompt,timeout_ms,state) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, runID, jobID, index, agent.Name, agent.Hash, agent.Executor, agent.Model, repository, agent.Prompt, agent.Timeout.Milliseconds(), state); err != nil {
			return "", fmt.Errorf("insert run: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit job: %w", err)
	}
	return jobID, nil
}

// CreateScheduledJob queues one due Shepherd run. It persists the next due time and uses
// a partial unique index so separate server processes and manual submissions cannot
// overlap Shepherd runs for a repository.
func (s *Store) CreateScheduledJob(ctx context.Context, schedule config.ResolvedShepherdSchedule) (string, bool, error) {
	nowTime := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var nextRun string
	err = tx.QueryRowContext(ctx, `SELECT next_run_at FROM schedule_state WHERE name=?`, schedule.Name).Scan(&nextRun)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("read shepherd schedule %q: %w", schedule.Name, err)
	}
	if err == nil {
		next, parseErr := time.Parse(time.RFC3339Nano, nextRun)
		if parseErr != nil {
			return "", false, fmt.Errorf("parse shepherd schedule %q next run: %w", schedule.Name, parseErr)
		}
		if next.After(nowTime) {
			return "", false, tx.Commit()
		}
	}
	jobID, err := randomID("job", 12)
	if err != nil {
		return "", false, err
	}
	runID, err := randomID("run", 12)
	if err != nil {
		return "", false, err
	}
	now := nowTime.Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO jobs(id,prompt,repository,selection_kind,selection_name,schedule_name,state,created_at,updated_at) VALUES(?,?,?,'agent','shepherd',?,'queued',?,?)`, jobID, schedule.Prompt, schedule.Repository, schedule.Name, now, now)
	if err != nil {
		return "", false, fmt.Errorf("insert scheduled job: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if changed == 0 {
		return "", false, tx.Commit()
	}
	agent := schedule.Agent
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,step,agent,agent_hash,executor,model,repository,rendered_prompt,timeout_ms,state) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, runID, jobID, 0, agent.Name, agent.Hash, agent.Executor, agent.Model, schedule.Repository, agent.Prompt, agent.Timeout.Milliseconds(), "queued"); err != nil {
		return "", false, fmt.Errorf("insert scheduled run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_state(name,next_run_at) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET next_run_at=excluded.next_run_at`, schedule.Name, nowTime.Add(schedule.Every).Format(time.RFC3339Nano)); err != nil {
		return "", false, fmt.Errorf("advance shepherd schedule %q: %w", schedule.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return jobID, true, nil
}

func (s *Store) Poll(ctx context.Context, request protocol.PollRequest) (*protocol.RunSpec, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nowTime := s.now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO workers(instance_id,name,last_seen_at) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET name=excluded.name,last_seen_at=excluded.last_seen_at`, request.InstanceID, request.Name, now); err != nil {
		return nil, fmt.Errorf("update worker: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state='queued',worker_instance=NULL,lease_token=NULL,lease_expires_at=NULL,started_at=NULL WHERE state='running' AND (lease_expires_at IS NULL OR lease_expires_at<=?)`, nowTime.UnixNano()); err != nil {
		return nil, fmt.Errorf("reclaim expired leases: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worker_repositories WHERE worker_instance=?`, request.InstanceID); err != nil {
		return nil, fmt.Errorf("clear worker repositories: %w", err)
	}
	for repository := range stringSet(request.Repositories) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_repositories(worker_instance,repository) VALUES(?,?)`, request.InstanceID, repository); err != nil {
			return nil, fmt.Errorf("store worker repository: %w", err)
		}
	}
	active, err := scanRunSpec(tx.QueryRowContext(ctx, `SELECT id,job_id,agent,agent_hash,executor,model,repository,rendered_prompt,timeout_ms,lease_token FROM runs WHERE worker_instance=? AND state='running' LIMIT 1`, request.InstanceID))
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &active, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	executors := stringSet(request.Executors)
	repositories := stringSet(request.Repositories)
	rows, err := tx.QueryContext(ctx, `SELECT id,job_id,agent,agent_hash,executor,model,repository,rendered_prompt,timeout_ms FROM runs WHERE state='queued' ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	var selected protocol.RunSpec
	for rows.Next() {
		var candidate protocol.RunSpec
		if err := rows.Scan(&candidate.ID, &candidate.JobID, &candidate.Agent, &candidate.AgentHash, &candidate.Executor, &candidate.Model, &candidate.Repository, &candidate.RenderedPrompt, &candidate.TimeoutMillis); err != nil {
			rows.Close()
			return nil, err
		}
		if executors[candidate.Executor] && repositories[candidate.Repository] && supportsModel(request.Models, candidate.Executor, candidate.Model) {
			selected = candidate
			break
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if selected.ID == "" {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	selected.LeaseToken, err = randomID("lease", 24)
	if err != nil {
		return nil, err
	}
	expiresAt := nowTime.Add(leaseDuration).UnixNano()
	result, err := tx.ExecContext(ctx, `UPDATE runs SET state='running',worker_instance=?,lease_token=?,lease_expires_at=?,started_at=? WHERE id=? AND state='queued'`, request.InstanceID, selected.LeaseToken, expiresAt, now, selected.ID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, fmt.Errorf("lease run: concurrent state change")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='running',updated_at=? WHERE id=? AND state='queued'`, now, selected.JobID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &selected, nil
}

func (s *Store) Complete(ctx context.Context, runID string, completion protocol.Completion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var jobID, state, instanceID, leaseToken, startedAt string
	var leaseExpiresAt sql.NullInt64
	var step int
	if err := tx.QueryRowContext(ctx, `SELECT job_id,step,state,COALESCE(worker_instance,''),COALESCE(lease_token,''),lease_expires_at,COALESCE(started_at,'') FROM runs WHERE id=?`, runID).Scan(&jobID, &step, &state, &instanceID, &leaseToken, &leaseExpiresAt, &startedAt); err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(instanceID), []byte(completion.InstanceID)) != 1 || subtle.ConstantTimeCompare([]byte(leaseToken), []byte(completion.LeaseToken)) != 1 {
		return ErrLeaseConflict
	}
	if terminalRunState(state) {
		return tx.Commit()
	}
	if state != "running" {
		return ErrRunState
	}
	if !leaseExpiresAt.Valid || leaseExpiresAt.Int64 <= s.now().UTC().UnixNano() {
		return ErrLeaseConflict
	}
	if !terminalRunState(completion.State) || completion.State == "skipped" {
		return fmt.Errorf("invalid terminal run state %q", completion.State)
	}
	if err := validateOutcome(completion.State, completion.ExitCode); err != nil {
		return err
	}
	durationMillis, tokenUsage := resultMetrics(completion.Result)
	completedAt := s.now().UTC()
	now := completedAt.Format(time.RFC3339Nano)
	if durationMillis == nil {
		durationMillis = elapsedMillis(startedAt, now)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET state=?,exit_code=?,error=?,result=?,events=?,lease_expires_at=NULL,completed_at=?,duration_millis=?,token_usage=? WHERE id=?`, completion.State, completion.ExitCode, completion.Error, string(completion.Result), completion.Events, now, durationMillis, tokenUsage, runID); err != nil {
		return err
	}
	if completion.State == "succeeded" {
		result, err := tx.ExecContext(ctx, `UPDATE runs SET state='queued' WHERE job_id=? AND step=? AND state='pending'`, jobID, step+1)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='succeeded',updated_at=? WHERE id=?`, now, jobID); err != nil {
				return err
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET state='skipped' WHERE job_id=? AND state='pending'`, jobID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='failed',updated_at=? WHERE id=?`, now, jobID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Heartbeat(ctx context.Context, runID string, heartbeat protocol.Heartbeat) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, instanceID, leaseToken string
	var leaseExpiresAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT state,COALESCE(worker_instance,''),COALESCE(lease_token,''),lease_expires_at FROM runs WHERE id=?`, runID).Scan(&state, &instanceID, &leaseToken, &leaseExpiresAt); err != nil {
		return err
	}
	if state != "running" {
		return ErrRunState
	}
	if subtle.ConstantTimeCompare([]byte(instanceID), []byte(heartbeat.InstanceID)) != 1 || subtle.ConstantTimeCompare([]byte(leaseToken), []byte(heartbeat.LeaseToken)) != 1 {
		return ErrLeaseConflict
	}
	now := s.now().UTC()
	if !leaseExpiresAt.Valid || leaseExpiresAt.Int64 <= now.UnixNano() {
		return ErrLeaseConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET lease_expires_at=? WHERE id=? AND state='running' AND worker_instance=? AND lease_token=?`, now.Add(leaseDuration).UnixNano(), runID, heartbeat.InstanceID, heartbeat.LeaseToken)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrLeaseConflict
	}
	return tx.Commit()
}

func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	jobs, err := s.listJobs(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	workers, err := s.listWorkers(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Jobs: jobs, Workers: workers}, nil
}

func (s *Store) AvailableRepositories(ctx context.Context, seenAfter time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT wr.repository
FROM worker_repositories wr
JOIN workers w ON w.instance_id=wr.worker_instance
WHERE julianday(w.last_seen_at) >= julianday(?)
   OR EXISTS (SELECT 1 FROM runs r WHERE r.worker_instance=w.instance_id AND r.state='running')
ORDER BY wr.repository`, seenAfter.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repositories := []string{}
	for rows.Next() {
		var repository string
		if err := rows.Scan(&repository); err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func (s *Store) KnownRepositories(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT repository FROM worker_repositories ORDER BY repository`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repositories := []string{}
	for rows.Next() {
		var repository string
		if err := rows.Scan(&repository); err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func (s *Store) RunOutput(ctx context.Context, runID string) (RunOutput, error) {
	var output RunOutput
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(result,''),COALESCE(events,'') FROM runs WHERE id=?`, runID).Scan(&output.Result, &output.Events)
	return output, err
}

func (s *Store) listJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,prompt,repository,selection_kind,selection_name,schedule_name,state,created_at,updated_at FROM jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	jobs := []Job{}
	for rows.Next() {
		var job Job
		var created, updated string
		if err := rows.Scan(&job.ID, &job.Prompt, &job.Repository, &job.SelectionKind, &job.SelectionName, &job.ScheduleName, &job.State, &created, &updated); err != nil {
			return nil, err
		}
		job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range jobs {
		jobs[index].Runs, err = s.listRuns(ctx, jobs[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

func (s *Store) listRuns(ctx context.Context, jobID string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.step,r.agent,r.executor,r.model,r.state,COALESCE(w.name,''),r.exit_code,COALESCE(r.error,''),COALESCE(r.started_at,''),COALESCE(r.completed_at,''),r.duration_millis,r.token_usage FROM runs r LEFT JOIN workers w ON w.instance_id=r.worker_instance WHERE r.job_id=? ORDER BY r.step`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		var run Run
		var started, completed string
		if err := rows.Scan(&run.ID, &run.Step, &run.Agent, &run.Executor, &run.Model, &run.State, &run.WorkerName, &run.ExitCode, &run.Error, &started, &completed, &run.DurationMillis, &run.TokenUsage); err != nil {
			return nil, err
		}
		run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		run.CompletedAt, _ = time.Parse(time.RFC3339Nano, completed)
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func resultMetrics(result []byte) (*int64, *int64) {
	var fields map[string]json.RawMessage
	if len(result) == 0 || json.Unmarshal(result, &fields) != nil {
		return nil, nil
	}
	return nonNegativeInteger(fields["duration_millis"]), nonNegativeInteger(fields["token_usage"])
}

func nonNegativeInteger(raw json.RawMessage) *int64 {
	var value int64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value < 0 {
		return nil
	}
	return &value
}

func elapsedMillis(startedAt, completedAt string) *int64 {
	started, startErr := time.Parse(time.RFC3339Nano, startedAt)
	completed, completionErr := time.Parse(time.RFC3339Nano, completedAt)
	if startErr != nil || completionErr != nil {
		return nil
	}
	duration := completed.Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	return &duration
}

func (s *Store) listWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id,name,last_seen_at FROM workers ORDER BY name,instance_id`)
	if err != nil {
		return nil, err
	}
	workers := []Worker{}
	for rows.Next() {
		var worker Worker
		var lastSeen string
		if err := rows.Scan(&worker.InstanceID, &worker.Name, &lastSeen); err != nil {
			return nil, err
		}
		worker.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
		workers = append(workers, worker)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range workers {
		workers[index].Repositories, err = s.listWorkerRepositories(ctx, workers[index].InstanceID)
		if err != nil {
			return nil, err
		}
	}
	return workers, nil
}

func (s *Store) listWorkerRepositories(ctx context.Context, instanceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository FROM worker_repositories WHERE worker_instance=? ORDER BY repository`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repositories := []string{}
	for rows.Next() {
		var repository string
		if err := rows.Scan(&repository); err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, rows.Err()
}

func scanRunSpec(row *sql.Row) (protocol.RunSpec, error) {
	var run protocol.RunSpec
	err := row.Scan(&run.ID, &run.JobID, &run.Agent, &run.AgentHash, &run.Executor, &run.Model, &run.Repository, &run.RenderedPrompt, &run.TimeoutMillis, &run.LeaseToken)
	return run, err
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func supportsModel(capabilities map[string][]string, executor, model string) bool {
	if model == "" {
		return true
	}
	models, ok := capabilities[executor]
	if !ok {
		return false
	}
	if len(models) == 0 {
		return true
	}
	return stringSet(models)[model]
}

func terminalRunState(state string) bool {
	return state == "succeeded" || state == "failed" || state == "timed_out" || state == "cancelled" || state == "skipped"
}

func validateOutcome(state string, exitCode int) error {
	switch state {
	case "succeeded":
		if exitCode != 0 {
			return errors.New("succeeded run must have exit code 0")
		}
	case "failed":
		if exitCode == 0 {
			return errors.New("failed run must have a non-zero exit code")
		}
	case "timed_out":
		if exitCode != 124 {
			return errors.New("timed out run must have exit code 124")
		}
	case "cancelled":
		if exitCode != 130 {
			return errors.New("cancelled run must have exit code 130")
		}
	}
	return nil
}

func randomID(prefix string, byteCount int) (string, error) {
	body := make([]byte, byteCount)
	if _, err := rand.Read(body); err != nil {
		return "", fmt.Errorf("generate %s ID: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(body), nil
}
