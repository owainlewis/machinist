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
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	_ "modernc.org/sqlite"
)

var (
	ErrLeaseConflict                   = errors.New("run lease does not match")
	ErrRunState                        = errors.New("run is not active")
	ErrInvalidCompletion               = errors.New("invalid run completion")
	ErrJobActive                       = errors.New("active job cannot be deleted")
	ErrTriggerMissing                  = errors.New("trigger state does not exist")
	ErrTriggerStale                    = errors.New("trigger state configuration changed")
	ErrTriggerPreviousGenerationActive = errors.New("previous trigger configuration still has active work")
)

const leaseDuration = 30 * time.Second
const maxTriggerErrorLength = 2000

// reclaimExpiredLeasesSQL returns running runs whose lease lapsed to the queue.
const reclaimExpiredLeasesSQL = `UPDATE runs SET state='queued',worker_instance=NULL,worker_name='',lease_token=NULL,lease_expires_at=NULL,started_at=NULL WHERE state='running' AND (lease_expires_at IS NULL OR lease_expires_at<=?)`

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Job struct {
	ID               string    `json:"id"`
	Prompt           string    `json:"prompt"`
	Repository       string    `json:"repository"`
	GitHubIssueTitle string    `json:"github_issue_title,omitempty"`
	Command          string    `json:"command"`
	ScheduleName     string    `json:"schedule_name,omitempty"`
	TriggerID        string    `json:"trigger_id,omitempty"`
	OccurrenceKey    string    `json:"occurrence_key,omitempty"`
	TriggerSubject   string    `json:"trigger_subject,omitempty"`
	State            string    `json:"state"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Runs             []Run     `json:"runs"`
}

type Run struct {
	ID             string    `json:"id"`
	Command        string    `json:"command"`
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
	Connected    bool      `json:"connected"`
}

type Snapshot struct {
	Jobs     []Job           `json:"jobs"`
	Workers  []Worker        `json:"workers"`
	Triggers []TriggerStatus `json:"triggers"`
}

// TriggerDefinition is the durable identity and schedule state for one resolved trigger.
// A changed signature resets scheduling from NextDueAt; an unchanged signature preserves
// its existing due time across restarts.
type TriggerDefinition struct {
	Identity        string
	Family          string
	ConfigSignature string
	NextDueAt       time.Time
}

// TriggerAdmission contains an already resolved job. Trigger scheduling code remains
// responsible for validating configuration and rendering the prompt before admission.
type TriggerAdmission struct {
	Identity          string
	Family            string
	ConfigSignature   string
	ConfigGeneration  string
	OccurrenceKey     string
	Subject           string
	ScheduledAt       time.Time
	NextDueAt         time.Time
	Prompt            string
	Repository        string
	SelectionName     string
	Command           config.ResolvedCommand
	GitHubRepository  string
	GitHubIssueNumber int
	GitHubIssueTitle  string
	RequestActor      string
	RequestLabel      string
}

type TriggerStatus struct {
	Identity            string     `json:"identity"`
	Family              string     `json:"family"`
	ConfigSignature     string     `json:"config_signature,omitempty"`
	ConfigGeneration    string     `json:"-"`
	NextDueAt           *time.Time `json:"next_due,omitempty"`
	PendingOccurrenceAt *time.Time `json:"-"`
	LastAttemptAt       *time.Time `json:"last_attempt,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success,omitempty"`
	ActiveJobID         string     `json:"active_job,omitempty"`
	CandidateCount      int64      `json:"candidate_count,omitempty"`
	AdmissionCount      int64      `json:"admission_count,omitempty"`
	CoalescedCount      int64      `json:"coalesced_count,omitempty"`
	Health              string     `json:"health"`
	LatestError         string     `json:"error,omitempty"`
}

type GitHubTriggerRequest struct {
	TriggerIdentity  string
	OccurrenceKey    string
	ConfigGeneration string
	Repository       string
	IssueNumber      int
	Subject          string
	Actor            string
	RequestLabel     string
	RequestedAt      time.Time
	State            string
	JobID            string
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
	const schemaVersion = 1
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read database schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version < schemaVersion {
		if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF;
DROP TABLE IF EXISTS github_trigger_requests; DROP TABLE IF EXISTS trigger_state; DROP TABLE IF EXISTS schedule_state;
DROP TABLE IF EXISTS known_repositories; DROP TABLE IF EXISTS worker_repositories; DROP TABLE IF EXISTS workers;
DROP TABLE IF EXISTS runs; DROP TABLE IF EXISTS jobs; PRAGMA foreign_keys=ON;`); err != nil {
			return fmt.Errorf("replace legacy database schema: %w", err)
		}
	}
	const schema = `
PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS jobs (
 id TEXT PRIMARY KEY, prompt TEXT NOT NULL, repository TEXT NOT NULL, command TEXT NOT NULL,
 schedule_name TEXT NOT NULL DEFAULT '', has_shepherd INTEGER NOT NULL DEFAULT 0,
 trigger_identity TEXT NOT NULL DEFAULT '', trigger_config_signature TEXT NOT NULL DEFAULT '',
 trigger_generation_id TEXT NOT NULL DEFAULT '', occurrence_key TEXT NOT NULL DEFAULT '',
 trigger_subject TEXT NOT NULL DEFAULT '', github_issue_title TEXT NOT NULL DEFAULT '',
 fixed_trigger INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS runs (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id), command TEXT NOT NULL, command_hash TEXT NOT NULL,
 executor TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL, rendered_prompt TEXT NOT NULL,
 timeout_ms INTEGER NOT NULL, state TEXT NOT NULL, worker_instance TEXT, worker_name TEXT NOT NULL DEFAULT '',
 lease_token TEXT, lease_expires_at INTEGER, exit_code INTEGER, error TEXT, result TEXT, events TEXT,
 started_at TEXT, completed_at TEXT, duration_millis INTEGER, token_usage INTEGER);
CREATE INDEX IF NOT EXISTS runs_dispatch ON runs(state, job_id);
CREATE TABLE IF NOT EXISTS workers (instance_id TEXT PRIMARY KEY, name TEXT NOT NULL, last_seen_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS worker_repositories (worker_instance TEXT NOT NULL REFERENCES workers(instance_id) ON DELETE CASCADE, repository TEXT NOT NULL, PRIMARY KEY(worker_instance,repository));
CREATE TABLE IF NOT EXISTS known_repositories (repository TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS schedule_state (name TEXT PRIMARY KEY,next_run_at TEXT NOT NULL,repository TEXT NOT NULL DEFAULT '',every_ms INTEGER NOT NULL DEFAULT 0,execution_signature TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS trigger_state (identity TEXT PRIMARY KEY,family TEXT NOT NULL,config_signature TEXT NOT NULL,generation_id TEXT NOT NULL,next_due_at TEXT,pending_occurrence_at TEXT,last_attempt_at TEXT,last_success_at TEXT,last_job_state TEXT NOT NULL DEFAULT '',last_job_error TEXT NOT NULL DEFAULT '',health TEXT NOT NULL DEFAULT 'healthy',latest_error TEXT NOT NULL DEFAULT '',candidate_count INTEGER NOT NULL DEFAULT 0,admission_count INTEGER NOT NULL DEFAULT 0,coalesced_count INTEGER NOT NULL DEFAULT 0,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS github_trigger_requests (trigger_identity TEXT NOT NULL,occurrence_key TEXT NOT NULL,config_generation TEXT NOT NULL,repository TEXT NOT NULL,issue_number INTEGER NOT NULL,subject TEXT NOT NULL,actor TEXT NOT NULL,request_label TEXT NOT NULL,requested_at TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('pending','admitted','rejected')),job_id TEXT,needs_reconciliation INTEGER NOT NULL DEFAULT 1,updated_at TEXT NOT NULL,PRIMARY KEY(trigger_identity,occurrence_key));
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_shepherd_repository ON jobs(repository) WHERE has_shepherd=1 AND state IN ('queued','running');
CREATE UNIQUE INDEX IF NOT EXISTS jobs_trigger_occurrence ON jobs(trigger_identity,occurrence_key) WHERE trigger_identity<>'' AND occurrence_key<>'';
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_fixed_trigger ON jobs(trigger_identity) WHERE fixed_trigger=1 AND state IN ('queued','running');
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_trigger_subject ON jobs(trigger_subject) WHERE trigger_subject<>'' AND state IN ('queued','running');
CREATE INDEX IF NOT EXISTS github_trigger_requests_reconciliation ON github_trigger_requests(trigger_identity,needs_reconciliation,requested_at);
PRAGMA user_version=1;`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	return nil
}

func (s *Store) CreateJob(ctx context.Context, prompt, repository, name string, command config.ResolvedCommand) (string, error) {
	if command.Name == "" {
		return "", errors.New("job must contain one command")
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,prompt,repository,command,has_shepherd,state,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?)`, jobID, prompt, repository, name, command.Name == "shepherd", now, now); err != nil {
		return "", fmt.Errorf("insert job: %w", err)
	}
	runID, err := randomID("run", 12)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,state) VALUES(?,?,?,?,?,?,?,?,?,'queued')`, runID, jobID, command.Name, command.Hash, command.Executor, command.Model, repository, command.Prompt, command.Timeout.Milliseconds()); err != nil {
		return "", fmt.Errorf("insert run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit job: %w", err)
	}
	return jobID, nil
}

// SyncTriggers makes the durable trigger set match the resolved configuration. Existing
// state is preserved only when both family and configuration signature are unchanged.
func (s *Store) SyncTriggers(ctx context.Context, definitions []TriggerDefinition) error {
	seen := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		if definition.Identity == "" || definition.Family == "" || definition.ConfigSignature == "" {
			return errors.New("trigger identity, family, and configuration signature are required")
		}
		if seen[definition.Identity] {
			return fmt.Errorf("duplicate trigger identity %q", definition.Identity)
		}
		seen[definition.Identity] = true
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	placeholders := make([]string, 0, len(definitions))
	identities := make([]any, 0, len(definitions))
	for _, definition := range definitions {
		placeholders = append(placeholders, "?")
		identities = append(identities, definition.Identity)
		generationID, err := randomID("trigger", 12)
		if err != nil {
			return fmt.Errorf("generate trigger %q configuration generation: %w", definition.Identity, err)
		}
		// A changed family or signature discards every piece of durable state, so the
		// row is recreated from scratch. An unchanged trigger keeps its schedule and
		// generation, gaining a generation only if it never had one.
		if _, err := tx.ExecContext(ctx, `DELETE FROM trigger_state WHERE identity=? AND (family<>? OR config_signature<>?)`, definition.Identity, definition.Family, definition.ConfigSignature); err != nil {
			return fmt.Errorf("sync trigger %q: %w", definition.Identity, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO trigger_state(identity,family,config_signature,generation_id,next_due_at,health,updated_at)
VALUES(?,?,?,?,?,'healthy',?)
ON CONFLICT(identity) DO UPDATE SET
  generation_id=CASE WHEN trigger_state.generation_id='' THEN excluded.generation_id ELSE trigger_state.generation_id END,
  updated_at=excluded.updated_at`, definition.Identity, definition.Family, definition.ConfigSignature, generationID, nullableTimeText(definition.NextDueAt), now); err != nil {
			return fmt.Errorf("sync trigger %q: %w", definition.Identity, err)
		}
	}
	removeStale := `DELETE FROM trigger_state`
	if len(identities) > 0 {
		removeStale += ` WHERE identity NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	if _, err := tx.ExecContext(ctx, removeStale, identities...); err != nil {
		return fmt.Errorf("remove stale triggers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit trigger sync: %w", err)
	}
	return nil
}

// CreateTriggeredJob durably admits an occurrence and its job in one transaction.
// Duplicate occurrences are idempotent. Fixed schedules coalesce a new occurrence while
// active work exists; subject-based triggers wait so a later retry can admit the event.
func (s *Store) CreateTriggeredJob(ctx context.Context, admission TriggerAdmission) (string, bool, error) {
	if admission.Identity == "" || admission.Family == "" {
		return "", false, errors.New("trigger identity and family are required")
	}
	if admission.ConfigSignature == "" {
		return "", false, errors.New("trigger config signature is required")
	}
	if admission.ConfigGeneration == "" {
		return "", false, errors.New("trigger config generation is required")
	}
	if admission.Command.Name == "" {
		return "", false, errors.New("triggered job must contain one command")
	}
	fixed := fixedTriggerFamily(admission.Family)
	if admission.OccurrenceKey == "" && fixed && !admission.ScheduledAt.IsZero() {
		admission.OccurrenceKey = admission.ScheduledAt.UTC().Format(time.RFC3339Nano)
	}
	if admission.OccurrenceKey == "" {
		return "", false, errors.New("trigger occurrence key is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var family, configSignature, configGeneration string
	if err := tx.QueryRowContext(ctx, `SELECT family,config_signature,generation_id FROM trigger_state WHERE identity=?`, admission.Identity).Scan(&family, &configSignature, &configGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("%w: %s", ErrTriggerMissing, admission.Identity)
		}
		return "", false, fmt.Errorf("read trigger %q: %w", admission.Identity, err)
	}
	if family != admission.Family {
		return "", false, fmt.Errorf("trigger %q family is %q, not %q", admission.Identity, family, admission.Family)
	}
	if configSignature != admission.ConfigSignature {
		return "", false, fmt.Errorf("%w: trigger %q configuration signature changed before admission", ErrTriggerStale, admission.Identity)
	}
	if configGeneration != admission.ConfigGeneration {
		return "", false, fmt.Errorf("%w: %s", ErrTriggerStale, admission.Identity)
	}
	if admission.Family == "github" {
		if admission.GitHubRepository == "" || admission.GitHubIssueNumber <= 0 || admission.Subject == "" || admission.RequestActor == "" || admission.RequestLabel == "" || admission.ScheduledAt.IsZero() {
			return "", false, errors.New("github trigger request metadata is required")
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO github_trigger_requests(trigger_identity,occurrence_key,config_generation,repository,issue_number,subject,actor,request_label,requested_at,state,needs_reconciliation,updated_at)
VALUES(?,?,?,?,?,?,?,?,?, 'pending',1,?)
ON CONFLICT(trigger_identity,occurrence_key) DO UPDATE SET
  config_generation=excluded.config_generation,
  repository=excluded.repository,
  issue_number=excluded.issue_number,
  subject=excluded.subject,
  actor=excluded.actor,
  request_label=excluded.request_label,
  requested_at=excluded.requested_at,
  needs_reconciliation=1,
  updated_at=excluded.updated_at
WHERE github_trigger_requests.state='pending'`, admission.Identity, admission.OccurrenceKey, admission.ConfigGeneration, admission.GitHubRepository, admission.GitHubIssueNumber, admission.Subject, admission.RequestActor, admission.RequestLabel, admission.ScheduledAt.UTC().Format(time.RFC3339Nano), now); err != nil {
			return "", false, fmt.Errorf("persist github trigger request: %w", err)
		}
	}

	var existingJob string
	err = tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE trigger_identity=? AND occurrence_key=?`, admission.Identity, admission.OccurrenceKey).Scan(&existingJob)
	if err == nil {
		if admission.Family == "github" {
			if admission.GitHubIssueTitle != "" {
				if _, updateErr := tx.ExecContext(ctx, `UPDATE jobs SET github_issue_title=? WHERE id=? AND github_issue_title=''`, admission.GitHubIssueTitle, existingJob); updateErr != nil {
					return "", false, fmt.Errorf("update existing github job title: %w", updateErr)
				}
			}
			if _, updateErr := tx.ExecContext(ctx, `UPDATE github_trigger_requests SET state='admitted',job_id=?,config_generation=?,needs_reconciliation=1,updated_at=? WHERE trigger_identity=? AND occurrence_key=?`, existingJob, admission.ConfigGeneration, s.now().UTC().Format(time.RFC3339Nano), admission.Identity, admission.OccurrenceKey); updateErr != nil {
				return "", false, fmt.Errorf("repair github trigger request: %w", updateErr)
			}
		}
		if fixed {
			if _, updateErr := tx.ExecContext(ctx, `UPDATE trigger_state SET pending_occurrence_at=NULL,next_due_at=COALESCE(?,next_due_at),updated_at=? WHERE identity=?`, nullableTimeText(admission.NextDueAt), s.now().UTC().Format(time.RFC3339Nano), admission.Identity); updateErr != nil {
				return "", false, fmt.Errorf("finish duplicate trigger occurrence: %w", updateErr)
			}
		}
		return existingJob, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("read trigger occurrence: %w", err)
	}

	if fixed {
		var activeGeneration string
		err = tx.QueryRowContext(ctx, `SELECT id,trigger_generation_id FROM jobs WHERE trigger_identity=? AND fixed_trigger=1 AND state IN ('queued','running')`, admission.Identity).Scan(&existingJob, &activeGeneration)
		if err == nil {
			if activeGeneration != admission.ConfigGeneration {
				return existingJob, false, fmt.Errorf("%w: %s", ErrTriggerPreviousGenerationActive, admission.Identity)
			}
			now := s.now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `UPDATE trigger_state SET next_due_at=COALESCE(?,next_due_at),pending_occurrence_at=NULL,last_attempt_at=?,health='coalesced',latest_error='',coalesced_count=coalesced_count+1,updated_at=? WHERE identity=?`, nullableTimeText(admission.NextDueAt), now, now, admission.Identity); err != nil {
				return "", false, fmt.Errorf("coalesce trigger %q: %w", admission.Identity, err)
			}
			return existingJob, false, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("read active trigger job: %w", err)
		}
	}
	if admission.Subject != "" {
		err = tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE trigger_subject=? AND state IN ('queued','running')`, admission.Subject).Scan(&existingJob)
		if err == nil {
			if admission.Family == "github" && admission.GitHubIssueTitle != "" {
				if _, updateErr := tx.ExecContext(ctx, `UPDATE jobs SET github_issue_title=? WHERE id=? AND github_issue_title=''`, admission.GitHubIssueTitle, existingJob); updateErr != nil {
					return "", false, fmt.Errorf("update existing github job title: %w", updateErr)
				}
			}
			return existingJob, false, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("read active trigger subject: %w", err)
		}
	}

	jobID, err := randomID("job", 12)
	if err != nil {
		return "", false, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,prompt,repository,command,has_shepherd,trigger_identity,trigger_config_signature,trigger_generation_id,occurrence_key,trigger_subject,github_issue_title,fixed_trigger,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'queued',?,?)`, jobID, admission.Prompt, admission.Repository, admission.SelectionName, admission.Command.Name == "shepherd", admission.Identity, admission.ConfigSignature, admission.ConfigGeneration, admission.OccurrenceKey, admission.Subject, admission.GitHubIssueTitle, fixed, now, now); err != nil {
		return "", false, fmt.Errorf("insert triggered job: %w", err)
	}
	if admission.Family == "github" {
		if _, err := tx.ExecContext(ctx, `UPDATE github_trigger_requests SET state='admitted',job_id=?,needs_reconciliation=1,updated_at=? WHERE trigger_identity=? AND occurrence_key=?`, jobID, now, admission.Identity, admission.OccurrenceKey); err != nil {
			return "", false, fmt.Errorf("admit github trigger request: %w", err)
		}
	}
	runID, err := randomID("run", 12)
	if err != nil {
		return "", false, err
	}
	command := admission.Command
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,state) VALUES(?,?,?,?,?,?,?,?,?,'queued')`, runID, jobID, command.Name, command.Hash, command.Executor, command.Model, admission.Repository, command.Prompt, command.Timeout.Milliseconds()); err != nil {
		return "", false, fmt.Errorf("insert triggered run: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE trigger_state SET next_due_at=COALESCE(?,next_due_at),pending_occurrence_at=NULL,last_attempt_at=?,health='active',latest_error='',admission_count=admission_count+1,updated_at=? WHERE identity=?`, nullableTimeText(admission.NextDueAt), now, now, admission.Identity)
	if err != nil {
		return "", false, fmt.Errorf("update trigger admission: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if changed != 1 {
		return "", false, fmt.Errorf("%w: %s", ErrTriggerMissing, admission.Identity)
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit triggered job: %w", err)
	}
	return jobID, true, nil
}

// RecordTriggerAttempt records one poll or scheduling attempt. Candidate counts are
// cumulative. Failed attempts do not advance the occurrence or next due time.
func (s *Store) RecordTriggerAttempt(ctx context.Context, identity, configGeneration string, candidates int, attemptErr error) error {
	if candidates < 0 {
		return errors.New("trigger candidate count cannot be negative")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	health := "healthy"
	latestError := ""
	if attemptErr != nil {
		health = "failed"
		latestError = boundedTriggerError(attemptErr.Error())
	}
	result, err := s.db.ExecContext(ctx, `UPDATE trigger_state SET
  last_attempt_at=?,
  candidate_count=candidate_count+?,
  health=CASE
    WHEN ?='healthy' AND health='coalesced' THEN 'coalesced'
	WHEN ?='healthy' AND EXISTS (SELECT 1 FROM jobs WHERE jobs.trigger_identity=trigger_state.identity AND jobs.trigger_generation_id=trigger_state.generation_id AND jobs.state IN ('queued','running')) THEN 'active'
	WHEN ?='healthy' AND last_job_state='failed' THEN 'failed'
    ELSE ?
  END,
  latest_error=CASE
	WHEN ?='healthy' AND last_job_state='failed' THEN last_job_error
    ELSE ?
  END,
  updated_at=?
WHERE identity=? AND generation_id=?`, now, candidates, health, health, health, health, health, latestError, now, identity, configGeneration)
	if err != nil {
		return fmt.Errorf("record trigger %q attempt: %w", identity, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
}

func (s *Store) SetTriggerNextDue(ctx context.Context, identity, configGeneration string, nextDue time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE trigger_state SET next_due_at=?,updated_at=? WHERE identity=? AND generation_id=?`, nullableTimeText(nextDue), s.now().UTC().Format(time.RFC3339Nano), identity, configGeneration)
	if err != nil {
		return fmt.Errorf("set trigger %q next due time: %w", identity, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
}

func (s *Store) SetTriggerPendingOccurrence(ctx context.Context, identity, configGeneration string, occurrence time.Time) error {
	if occurrence.IsZero() {
		return errors.New("trigger pending occurrence is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE trigger_state SET pending_occurrence_at=?,updated_at=? WHERE identity=? AND generation_id=?`, nullableTimeText(occurrence), s.now().UTC().Format(time.RFC3339Nano), identity, configGeneration)
	if err != nil {
		return fmt.Errorf("set trigger %q pending occurrence: %w", identity, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
}

// TriggerOccurrenceExists distinguishes an idempotent admission from an occurrence that
// is still waiting behind active work for the same subject.
func (s *Store) TriggerOccurrenceExists(ctx context.Context, identity, occurrenceKey string) (bool, error) {
	if identity == "" || occurrenceKey == "" {
		return false, errors.New("trigger identity and occurrence key are required")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE trigger_identity=? AND occurrence_key=?)`, identity, occurrenceKey).Scan(&exists); err != nil {
		return false, fmt.Errorf("read trigger occurrence: %w", err)
	}
	return exists == 1, nil
}

// RejectGitHubTriggerRequest durably consumes an unauthorized request before
// its label is removed. Reconciliation remains pending until GitHub confirms
// that no newer request event was hidden by the label transition.
func (s *Store) RejectGitHubTriggerRequest(ctx context.Context, request GitHubTriggerRequest) error {
	if request.TriggerIdentity == "" || request.OccurrenceKey == "" || request.ConfigGeneration == "" || request.Repository == "" || request.IssueNumber <= 0 || request.Subject == "" || request.Actor == "" || request.RequestLabel == "" || request.RequestedAt.IsZero() {
		return errors.New("complete github trigger request metadata is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation string
	if err := tx.QueryRowContext(ctx, `SELECT generation_id FROM trigger_state WHERE identity=?`, request.TriggerIdentity).Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrTriggerMissing, request.TriggerIdentity)
		}
		return err
	}
	if generation != request.ConfigGeneration {
		return fmt.Errorf("%w: %s", ErrTriggerStale, request.TriggerIdentity)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_trigger_requests(trigger_identity,occurrence_key,config_generation,repository,issue_number,subject,actor,request_label,requested_at,state,needs_reconciliation,updated_at)
VALUES(?,?,?,?,?,?,?,?,?, 'rejected',1,?)
ON CONFLICT(trigger_identity,occurrence_key) DO UPDATE SET
  state=CASE WHEN github_trigger_requests.state='admitted' THEN 'admitted' ELSE 'rejected' END,
  config_generation=excluded.config_generation,
  request_label=excluded.request_label,
  needs_reconciliation=1,
  updated_at=excluded.updated_at`, request.TriggerIdentity, request.OccurrenceKey, request.ConfigGeneration, request.Repository, request.IssueNumber, request.Subject, request.Actor, request.RequestLabel, request.RequestedAt.UTC().Format(time.RFC3339Nano), now); err != nil {
		return fmt.Errorf("persist rejected github trigger request: %w", err)
	}
	return tx.Commit()
}

func (s *Store) GitHubTriggerReconciliations(ctx context.Context, identity string) ([]GitHubTriggerRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT trigger_identity,occurrence_key,config_generation,repository,issue_number,subject,actor,request_label,requested_at,state,COALESCE(job_id,'')
FROM github_trigger_requests
WHERE trigger_identity=? AND needs_reconciliation=1
ORDER BY requested_at, occurrence_key`, identity)
	if err != nil {
		return nil, fmt.Errorf("read github trigger reconciliations: %w", err)
	}
	defer rows.Close()
	var requests []GitHubTriggerRequest
	for rows.Next() {
		var request GitHubTriggerRequest
		var requestedAt string
		if err := rows.Scan(&request.TriggerIdentity, &request.OccurrenceKey, &request.ConfigGeneration, &request.Repository, &request.IssueNumber, &request.Subject, &request.Actor, &request.RequestLabel, &requestedAt, &request.State, &request.JobID); err != nil {
			return nil, fmt.Errorf("read github trigger reconciliation: %w", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, requestedAt)
		if err != nil {
			return nil, fmt.Errorf("parse github trigger request time: %w", err)
		}
		request.RequestedAt = parsed
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (s *Store) CompleteGitHubTriggerReconciliation(ctx context.Context, identity, occurrenceKey, generation string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE github_trigger_requests SET needs_reconciliation=0,updated_at=? WHERE trigger_identity=? AND occurrence_key=? AND config_generation=?`, s.now().UTC().Format(time.RFC3339Nano), identity, occurrenceKey, generation)
	if err != nil {
		return fmt.Errorf("complete github trigger reconciliation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
}

func (s *Store) AddTriggerCoalesced(ctx context.Context, identity, configGeneration string, count int64) error {
	if count < 0 {
		return errors.New("trigger coalesced count cannot be negative")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE trigger_state SET coalesced_count=coalesced_count+?,health=CASE WHEN ?>0 THEN 'coalesced' ELSE health END,updated_at=? WHERE identity=? AND generation_id=?`, count, count, now, identity, configGeneration)
	if err != nil {
		return fmt.Errorf("record trigger %q coalesced occurrences: %w", identity, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: %s", ErrTriggerStale, identity)
	}
	return nil
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
	signature, err := shepherdScheduleSignature(schedule)
	if err != nil {
		return "", false, err
	}
	var nextRun, repository, executionSignature string
	var everyMillis int64
	err = tx.QueryRowContext(ctx, `SELECT next_run_at,repository,every_ms,execution_signature FROM schedule_state WHERE name=?`, schedule.Name).Scan(&nextRun, &repository, &everyMillis, &executionSignature)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("read shepherd schedule %q: %w", schedule.Name, err)
	}
	if err == nil {
		next, parseErr := time.Parse(time.RFC3339Nano, nextRun)
		if parseErr != nil {
			return "", false, fmt.Errorf("parse shepherd schedule %q next run: %w", schedule.Name, parseErr)
		}
		unchanged := repository == schedule.Repository && everyMillis == schedule.Every.Milliseconds() && executionSignature == signature
		if unchanged && next.After(nowTime) {
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
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO jobs(id,prompt,repository,command,schedule_name,has_shepherd,state,created_at,updated_at) VALUES(?,?,?,'shepherd',?,1,'queued',?,?)`, jobID, schedule.Prompt, schedule.Repository, schedule.Name, now, now)
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
	command := schedule.Command
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,state) VALUES(?,?,?,?,?,?,?,?,?,?)`, runID, jobID, command.Name, command.Hash, command.Executor, command.Model, schedule.Repository, command.Prompt, command.Timeout.Milliseconds(), "queued"); err != nil {
		return "", false, fmt.Errorf("insert scheduled run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_state(name,next_run_at,repository,every_ms,execution_signature) VALUES(?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET next_run_at=excluded.next_run_at,repository=excluded.repository,every_ms=excluded.every_ms,execution_signature=excluded.execution_signature`, schedule.Name, nowTime.Add(schedule.Every).Format(time.RFC3339Nano), schedule.Repository, schedule.Every.Milliseconds(), signature); err != nil {
		return "", false, fmt.Errorf("advance shepherd schedule %q: %w", schedule.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return jobID, true, nil
}

func shepherdScheduleSignature(schedule config.ResolvedShepherdSchedule) (string, error) {
	body, err := json.Marshal(struct {
		MaxActions int                    `json:"max_actions"`
		Prompt     string                 `json:"prompt"`
		Command    config.ResolvedCommand `json:"command"`
	}{schedule.MaxActions, schedule.Prompt, schedule.Command})
	if err != nil {
		return "", fmt.Errorf("encode shepherd schedule %q execution settings: %w", schedule.Name, err)
	}
	return string(body), nil
}

func (s *Store) Poll(ctx context.Context, request protocol.PollRequest) (*protocol.RunSpec, error) {
	return s.poll(ctx, request, 0)
}

// poll allows at most maxConcurrentJobs running jobs. Zero leaves concurrency
// unlimited. Expired leases remain eligible so interrupted work can make progress.
func (s *Store) poll(ctx context.Context, request protocol.PollRequest, maxConcurrentJobs int) (*protocol.RunSpec, error) {
	if maxConcurrentJobs < 0 {
		return nil, errors.New("max concurrent jobs cannot be negative")
	}
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
	if _, err := tx.ExecContext(ctx, reclaimExpiredLeasesSQL, nowTime.UnixNano()); err != nil {
		return nil, fmt.Errorf("reclaim expired leases: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM worker_repositories WHERE worker_instance=?`, request.InstanceID); err != nil {
		return nil, fmt.Errorf("clear worker repositories: %w", err)
	}
	repositories := stringSet(request.Repositories)
	for repository := range repositories {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO known_repositories(repository) VALUES(?)`, repository); err != nil {
			return nil, fmt.Errorf("store known repository: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_repositories(worker_instance,repository) VALUES(?,?)`, request.InstanceID, repository); err != nil {
			return nil, fmt.Errorf("store worker repository: %w", err)
		}
	}
	active, err := scanRunSpec(tx.QueryRowContext(ctx, `SELECT id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,lease_token FROM runs WHERE worker_instance=? AND state='running' LIMIT 1`, request.InstanceID))
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &active, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	atCapacity := false
	if maxConcurrentJobs > 0 {
		var runningJobs int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE state='running'`).Scan(&runningJobs); err != nil {
			return nil, fmt.Errorf("count running jobs: %w", err)
		}
		atCapacity = runningJobs >= maxConcurrentJobs
	}

	executors := stringSet(request.Executors)
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.job_id,r.command,r.command_hash,r.executor,r.model,r.repository,r.rendered_prompt,r.timeout_ms,j.state FROM runs r JOIN jobs j ON j.id=r.job_id WHERE r.state='queued' ORDER BY r.rowid`)
	if err != nil {
		return nil, err
	}
	var selected protocol.RunSpec
	for rows.Next() {
		var candidate protocol.RunSpec
		var jobState string
		if err := rows.Scan(&candidate.ID, &candidate.JobID, &candidate.Command, &candidate.CommandHash, &candidate.Executor, &candidate.Model, &candidate.Repository, &candidate.RenderedPrompt, &candidate.TimeoutMillis, &jobState); err != nil {
			rows.Close()
			return nil, err
		}
		if atCapacity && jobState != "running" {
			continue
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
	result, err := tx.ExecContext(ctx, `UPDATE runs SET state='running',worker_instance=?,worker_name=?,lease_token=?,lease_expires_at=?,started_at=? WHERE id=? AND state='queued'`, request.InstanceID, request.Name, selected.LeaseToken, expiresAt, now, selected.ID)
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
	var jobID, state, instanceID, leaseToken, startedAt, triggerIdentity, triggerGeneration string
	var leaseExpiresAt sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT r.job_id,r.state,COALESCE(r.worker_instance,''),COALESCE(r.lease_token,''),r.lease_expires_at,COALESCE(r.started_at,''),COALESCE(j.trigger_identity,''),COALESCE(j.trigger_generation_id,'') FROM runs r JOIN jobs j ON j.id=r.job_id WHERE r.id=?`, runID).Scan(&jobID, &state, &instanceID, &leaseToken, &leaseExpiresAt, &startedAt, &triggerIdentity, &triggerGeneration); err != nil {
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
	if !terminalRunState(completion.State) {
		return fmt.Errorf("%w: invalid terminal run state %q", ErrInvalidCompletion, completion.State)
	}
	if err := validateOutcome(completion.State, completion.ExitCode); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCompletion, err)
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
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='succeeded',updated_at=? WHERE id=?`, now, jobID); err != nil {
			return err
		}
		if triggerIdentity != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE trigger_state SET last_success_at=?,last_job_state='succeeded',last_job_error='',health='healthy',latest_error='',updated_at=? WHERE identity=? AND generation_id=?`, now, now, triggerIdentity, triggerGeneration); err != nil {
				return fmt.Errorf("record trigger success: %w", err)
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='failed',updated_at=? WHERE id=?`, now, jobID); err != nil {
			return err
		}
		if triggerIdentity != "" {
			latestError := completion.Error
			if latestError == "" {
				latestError = "triggered job " + completion.State
			}
			latestError = boundedTriggerError(latestError)
			if _, err := tx.ExecContext(ctx, `UPDATE trigger_state SET last_job_state='failed',last_job_error=?,health='failed',latest_error=?,updated_at=? WHERE identity=? AND generation_id=?`, latestError, latestError, now, triggerIdentity, triggerGeneration); err != nil {
				return fmt.Errorf("record trigger failure: %w", err)
			}
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
	if _, err := tx.ExecContext(ctx, `UPDATE workers SET last_seen_at=? WHERE instance_id=?`, now.Format(time.RFC3339Nano), heartbeat.InstanceID); err != nil {
		return fmt.Errorf("update worker heartbeat: %w", err)
	}
	return tx.Commit()
}

func (s *Store) DeleteJob(ctx context.Context, jobID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM jobs WHERE id=?`, jobID).Scan(&state); err != nil {
		return err
	}
	if !terminalJobState(state) {
		return ErrJobActive
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE github_trigger_requests SET job_id=NULL,updated_at=? WHERE job_id=?`, now, jobID); err != nil {
		return fmt.Errorf("clear deleted job trigger links: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE job_id=?`, jobID); err != nil {
		return fmt.Errorf("delete job runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM jobs WHERE id=?`, jobID); err != nil {
		return fmt.Errorf("delete job: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ReclaimExpiredLeases(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, reclaimExpiredLeasesSQL, s.now().UTC().UnixNano())
	if err != nil {
		return 0, fmt.Errorf("reclaim expired leases: %w", err)
	}
	return result.RowsAffected()
}

func (s *Store) PruneSupersededWorkers(ctx context.Context, seenAfter time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM workers AS old
WHERE julianday(old.last_seen_at) < julianday(?)
  AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.worker_instance=old.instance_id AND r.state='running')
  AND EXISTS (
    SELECT 1 FROM workers newer
    WHERE newer.name=old.name
      AND (julianday(newer.last_seen_at) > julianday(old.last_seen_at)
        OR (newer.last_seen_at=old.last_seen_at AND newer.instance_id>old.instance_id))
  )`, seenAfter.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("prune superseded workers: %w", err)
	}
	return result.RowsAffected()
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
	triggers, err := s.TriggerSnapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Jobs: jobs, Workers: workers, Triggers: triggers}, nil
}

func (s *Store) TriggerSnapshot(ctx context.Context) ([]TriggerStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.identity,t.family,t.config_signature,t.generation_id,COALESCE(t.next_due_at,''),COALESCE(t.pending_occurrence_at,''),COALESCE(t.last_attempt_at,''),COALESCE(t.last_success_at,''),COALESCE((SELECT j.id FROM jobs j WHERE j.trigger_identity=t.identity AND j.trigger_generation_id=t.generation_id AND j.state IN ('queued','running') ORDER BY j.created_at LIMIT 1),''),t.candidate_count,t.admission_count,t.coalesced_count,t.health,t.latest_error FROM trigger_state t ORDER BY t.identity`)
	if err != nil {
		return nil, fmt.Errorf("read trigger snapshot: %w", err)
	}
	defer rows.Close()
	statuses := []TriggerStatus{}
	for rows.Next() {
		var status TriggerStatus
		var nextDue, pendingOccurrence, lastAttempt, lastSuccess string
		if err := rows.Scan(&status.Identity, &status.Family, &status.ConfigSignature, &status.ConfigGeneration, &nextDue, &pendingOccurrence, &lastAttempt, &lastSuccess, &status.ActiveJobID, &status.CandidateCount, &status.AdmissionCount, &status.CoalescedCount, &status.Health, &status.LatestError); err != nil {
			return nil, fmt.Errorf("read trigger snapshot: %w", err)
		}
		status.NextDueAt = parseOptionalTime(nextDue)
		status.PendingOccurrenceAt = parseOptionalTime(pendingOccurrence)
		status.LastAttemptAt = parseOptionalTime(lastAttempt)
		status.LastSuccessAt = parseOptionalTime(lastSuccess)
		if status.Health == "healthy" && status.ActiveJobID == "" && status.PendingOccurrenceAt == nil && status.NextDueAt != nil && status.NextDueAt.Before(s.now().UTC()) {
			status.Health = "stale"
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read trigger snapshot: %w", err)
	}
	return statuses, nil
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
	rows, err := s.db.QueryContext(ctx, `SELECT repository FROM known_repositories ORDER BY repository`)
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
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,j.prompt,j.repository,j.github_issue_title,j.command,j.schedule_name,j.trigger_identity,j.occurrence_key,j.trigger_subject,j.state,j.created_at,j.updated_at,
COALESCE(r.id,''),COALESCE(r.command,''),COALESCE(r.executor,''),COALESCE(r.model,''),COALESCE(r.state,''),COALESCE(NULLIF(r.worker_name,''),w.name,''),r.exit_code,COALESCE(r.error,''),COALESCE(r.started_at,''),COALESCE(r.completed_at,''),r.duration_millis,r.token_usage
FROM jobs j LEFT JOIN runs r ON r.job_id=j.id LEFT JOIN workers w ON w.instance_id=r.worker_instance ORDER BY j.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job := Job{Runs: []Run{}}
		var run Run
		var created, updated, started, completed string
		if err := rows.Scan(&job.ID, &job.Prompt, &job.Repository, &job.GitHubIssueTitle, &job.Command, &job.ScheduleName, &job.TriggerID, &job.OccurrenceKey, &job.TriggerSubject, &job.State, &created, &updated,
			&run.ID, &run.Command, &run.Executor, &run.Model, &run.State, &run.WorkerName, &run.ExitCode, &run.Error, &started, &completed, &run.DurationMillis, &run.TokenUsage); err != nil {
			return nil, err
		}
		job.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		job.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		if run.ID != "" {
			run.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
			run.CompletedAt, _ = time.Parse(time.RFC3339Nano, completed)
			job.Runs = append(job.Runs, run)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
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
		worker := Worker{Repositories: []string{}}
		var lastSeen string
		if err := rows.Scan(&worker.InstanceID, &worker.Name, &lastSeen); err != nil {
			rows.Close()
			return nil, err
		}
		worker.LastSeenAt, _ = time.Parse(time.RFC3339Nano, lastSeen)
		workers = append(workers, worker)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	repositories, err := s.db.QueryContext(ctx, `SELECT worker_instance,repository FROM worker_repositories ORDER BY worker_instance,repository`)
	if err != nil {
		return nil, err
	}
	defer repositories.Close()
	byInstance := make(map[string]int, len(workers))
	for index, worker := range workers {
		byInstance[worker.InstanceID] = index
	}
	for repositories.Next() {
		var instanceID, repository string
		if err := repositories.Scan(&instanceID, &repository); err != nil {
			return nil, err
		}
		if index, ok := byInstance[instanceID]; ok {
			workers[index].Repositories = append(workers[index].Repositories, repository)
		}
	}
	return workers, repositories.Err()
}

func scanRunSpec(row *sql.Row) (protocol.RunSpec, error) {
	var run protocol.RunSpec
	err := row.Scan(&run.ID, &run.JobID, &run.Command, &run.CommandHash, &run.Executor, &run.Model, &run.Repository, &run.RenderedPrompt, &run.TimeoutMillis, &run.LeaseToken)
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

func fixedTriggerFamily(family string) bool {
	return family == "interval" || family == "cron"
}

func nullableTimeText(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseOptionalTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}

func boundedTriggerError(message string) string {
	runes := []rune(message)
	if len(runes) > maxTriggerErrorLength {
		runes = runes[:maxTriggerErrorLength]
	}
	return string(runes)
}

func terminalRunState(state string) bool {
	return state == "succeeded" || state == "failed" || state == "timed_out" || state == "cancelled"
}

func terminalJobState(state string) bool {
	return state == "succeeded" || state == "failed"
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
