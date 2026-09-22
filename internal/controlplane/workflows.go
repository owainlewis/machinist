package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

var ErrWorkflowAction = errors.New("workflow changed or action is not available")

// Version 3 removes the one-run constraint while preserving every existing row.
func (s *Store) upgradeWorkflows(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The v2 runs table has no inbound foreign keys.
	if _, err = tx.ExecContext(ctx, `ALTER TABLE runs RENAME TO runs_v2; DROP INDEX IF EXISTS runs_dispatch;
 CREATE TABLE runs (
 id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id), command TEXT NOT NULL, command_hash TEXT NOT NULL,
 executor TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', repository TEXT NOT NULL, rendered_prompt TEXT NOT NULL,
 timeout_ms INTEGER NOT NULL, state TEXT NOT NULL, worker_instance TEXT, worker_name TEXT NOT NULL DEFAULT '',
 lease_token TEXT, lease_expires_at INTEGER, exit_code INTEGER, error TEXT, result TEXT, events TEXT,
 started_at TEXT, completed_at TEXT, duration_millis INTEGER, token_usage INTEGER);
 INSERT INTO runs SELECT * FROM runs_v2;
 DROP TABLE runs_v2;
 CREATE INDEX runs_dispatch ON runs(state,job_id);
 PRAGMA user_version=3;`); err != nil {
		return err
	}
	return tx.Commit()
}

const workflowSchema = `
 CREATE TABLE IF NOT EXISTS workflow_jobs (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
 name TEXT NOT NULL, plan TEXT NOT NULL, current_step INTEGER NOT NULL DEFAULT 0,
 worker_name TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS workflow_attempts (
 run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 step INTEGER NOT NULL, outcome TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '');`

type WorkflowProgress struct {
	Name        string   `json:"name"`
	Steps       []string `json:"steps"`
	CurrentStep int      `json:"current_step"`
}

func (s *Store) CreateWorkflowJob(ctx context.Context, prompt, repository, name string, steps []config.WorkflowStep) (string, error) {
	return s.createWorkflowJob(ctx, prompt, repository, name, steps, nil)
}

func (s *Store) createWorkflowJob(ctx context.Context, prompt, repository, name string, steps []config.WorkflowStep, task *protocol.Task) (string, error) {
	if len(steps) == 0 {
		return "", errors.New("workflow requires steps")
	}
	plan, err := json.Marshal(steps)
	if err != nil {
		return "", err
	}
	id, err := randomID("job", 12)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,prompt,repository,command,state,created_at,updated_at) VALUES(?,?,?,?,'queued',?,?)`, id, prompt, repository, name, now, now); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workflow_jobs(job_id,name,plan) VALUES(?,?,?)`, id, name, string(plan)); err != nil {
		return "", err
	}
	if task != nil {
		raw, _ := json.Marshal(task)
		if _, err = tx.ExecContext(ctx, "INSERT INTO task_inputs(job_id,task) VALUES(?,?)", id, string(raw)); err != nil {
			return "", err
		}
	}
	if err = enqueueStep(ctx, tx, id, repository, 0, steps[0], false, now); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func enqueueStep(ctx context.Context, tx *sql.Tx, job, repository string, index int, step config.WorkflowStep, approved bool, now string) error {
	id, err := randomID("run", 12)
	if err != nil {
		return err
	}
	state := "queued"
	if step.Approval && !approved {
		state = "awaiting_approval"
	}
	c := step.Command
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,command,command_hash,executor,model,repository,rendered_prompt,timeout_ms,state) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, job, c.Name, c.Hash, c.Executor, c.Model, repository, c.Prompt, c.Timeout.Milliseconds(), state); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workflow_attempts(run_id,step) VALUES(?,?)`, id, index); err != nil {
		return err
	}
	if state == "awaiting_approval" {
		if err = bindReviewGate(ctx, tx, job, id, index); err != nil {
			return err
		}
	}
	problem, err := bindTaskInputs(ctx, tx, job, id, step)
	if err != nil {
		return err
	}
	if problem != "" {
		state = "blocked"
		if _, err = tx.ExecContext(ctx, "UPDATE runs SET state='failed',error=? WHERE id=?", problem, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE workflow_attempts SET outcome='blocked',summary=? WHERE run_id=?", problem, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_jobs SET current_step=? WHERE job_id=?`, index, job); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET state=?,updated_at=? WHERE id=?`, state, now, job)
	return err
}

// completeWorkflow runs in the same transaction as the process completion.
func completeWorkflow(ctx context.Context, tx *sql.Tx, job, run string, c protocol.Completion, now string) (bool, error) {
	var plan, repository string
	var index int
	err := tx.QueryRowContext(ctx, `SELECT w.plan,w.current_step,j.repository FROM workflow_jobs w JOIN jobs j ON j.id=w.job_id WHERE w.job_id=?`, job).Scan(&plan, &index, &repository)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	state, summary := "failed", c.Error
	approvalRequired := false
	var result struct {
		StepResult json.RawMessage `json:"step_result"`
	}
	if c.State == "succeeded" {
		if err = json.Unmarshal(c.Result, &result); err == nil {
			var step *protocol.StepResult
			step, err = protocol.ParseStepResult(result.StepResult)
			if err == nil {
				state, summary = step.Outcome, step.Summary
				approvalRequired = step.ApprovalRequired
			}
		}
		if err != nil {
			summary = "Missing or invalid workflow result: " + err.Error()
		}
	}
	problem, e := validateOutputs(ctx, tx, run, c)
	if e != nil {
		return true, e
	}
	if problem != "" {
		state, summary = "blocked", problem
	}
	if summary == "" {
		summary = "Execution " + c.State
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workflow_attempts SET outcome=?,summary=? WHERE run_id=?`, state, summary, run); err != nil {
		return true, err
	}
	if state == "complete" {
		var steps []config.WorkflowStep
		if err = json.Unmarshal([]byte(plan), &steps); err != nil {
			return true, err
		}
		if index+1 < len(steps) {
			next := steps[index+1]
			next.Approval = next.Approval || approvalRequired
			if approvalRequired {
				steps[index+1] = next
				updated, err := json.Marshal(steps)
				if err != nil {
					return true, err
				}
				if _, err = tx.ExecContext(ctx, "UPDATE workflow_jobs SET plan=? WHERE job_id=?", string(updated), job); err != nil {
					return true, err
				}
			}
			return true, enqueueStep(ctx, tx, job, repository, index+1, next, false, now)
		}
		state = "succeeded"
	}
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET state=?,updated_at=? WHERE id=?`, state, now, job)
	return true, err
}

// WorkflowAction uses the expected latest run as a compare-and-swap token so a
// double click or stale browser cannot approve/retry a newer execution.
func (s *Store) WorkflowAction(ctx context.Context, job, run, action string, stopped bool, feedback ...string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, latest, plan, repository string
	var index int
	err = tx.QueryRowContext(ctx, `SELECT j.state,w.plan,w.current_step,j.repository,(SELECT id FROM runs WHERE job_id=j.id ORDER BY rowid DESC LIMIT 1) FROM jobs j JOIN workflow_jobs w ON w.job_id=j.id WHERE j.id=?`, job).Scan(&state, &plan, &index, &repository, &latest)
	if err != nil {
		return err
	}
	if latest != run {
		return ErrWorkflowAction
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	switch action {
	case "request_changes":
		if state != "awaiting_approval" || len(feedback) != 1 {
			return ErrWorkflowAction
		}
		err = requestChanges(ctx, tx, job, run, repository, plan, feedback[0], now, index)
	case "cancel":
		if state == "succeeded" || state == "cancelled" {
			return ErrWorkflowAction
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET state='cancelled',exit_code=130,error='Cancelled by operator',completed_at=? WHERE id=? AND state IN ('queued','running','awaiting_approval')`, now, run); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state='cancelled',updated_at=? WHERE id=?`, now, job)
	case "approve":
		if state != "awaiting_approval" {
			return ErrWorkflowAction
		}
		if _, err = tx.ExecContext(ctx, "UPDATE review_gates SET decision='approved' WHERE run_id=? AND decision=''", run); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET state='queued' WHERE id=? AND state='awaiting_approval'`, run); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state='queued',updated_at=? WHERE id=?`, now, job)
	case "retry":
		if state != "failed" && state != "blocked" && state != "interrupted" && state != "cancelled" {
			return ErrWorkflowAction
		}
		if (state == "interrupted" || state == "cancelled") && !stopped {
			return fmt.Errorf("%w: confirm the previous process has stopped before retrying", ErrWorkflowAction)
		}
		var steps []config.WorkflowStep
		if err = json.Unmarshal([]byte(plan), &steps); err != nil {
			return err
		}
		// Approval applies to each attempt, including retries.
		err = enqueueStep(ctx, tx, job, repository, index, steps[index], false, now)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO execution_reviews(run_id,context) SELECT (SELECT id FROM runs WHERE job_id=? ORDER BY rowid DESC LIMIT 1),context FROM execution_reviews WHERE run_id=?`, job, run)
		}
	default:
		return ErrWorkflowAction
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func reclaimLeases(ctx context.Context, tx *sql.Tx, now time.Time) (int64, error) {
	// A workflow may have performed external effects. Never silently replay it.
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET state='interrupted',updated_at=? WHERE id IN (SELECT job_id FROM runs WHERE state='running' AND (lease_expires_at IS NULL OR lease_expires_at<=?) AND job_id IN (SELECT job_id FROM workflow_jobs))`, now.Format(time.RFC3339Nano), now.UnixNano()); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET state='interrupted',error='Worker lease expired; verify the previous process stopped before retrying',completed_at=? WHERE state='running' AND (lease_expires_at IS NULL OR lease_expires_at<=?) AND job_id IN (SELECT job_id FROM workflow_jobs)`, now.Format(time.RFC3339Nano), now.UnixNano())
	if err != nil {
		return 0, err
	}
	interrupted, _ := res.RowsAffected()
	res, err = tx.ExecContext(ctx, reclaimExpiredLeasesSQL, now.UnixNano())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n + interrupted, err
}

func (s *Store) loadWorkflowProgress(ctx context.Context, jobs []Job) error {
	rows, err := s.db.QueryContext(ctx, `SELECT job_id,name,plan,current_step FROM workflow_jobs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[string]*Job{}
	for i := range jobs {
		byID[jobs[i].ID] = &jobs[i]
	}
	for rows.Next() {
		var id, plan string
		var progress WorkflowProgress
		if err = rows.Scan(&id, &progress.Name, &plan, &progress.CurrentStep); err != nil {
			return err
		}
		var steps []config.WorkflowStep
		if err = json.Unmarshal([]byte(plan), &steps); err != nil {
			return err
		}
		for _, step := range steps {
			progress.Steps = append(progress.Steps, step.Command.Name)
		}
		if job := byID[id]; job != nil {
			job.Workflow = &progress
		}
	}
	return rows.Err()
}
