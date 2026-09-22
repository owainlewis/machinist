package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

const artifactSchema = `
CREATE TABLE IF NOT EXISTS task_inputs(job_id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE, task TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS execution_inputs(run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE, task TEXT NOT NULL, inputs TEXT NOT NULL, required TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS artifacts(id TEXT PRIMARY KEY, job_id TEXT NOT NULL, run_id TEXT NOT NULL, path TEXT NOT NULL, content_type TEXT NOT NULL, size INTEGER NOT NULL, checksum TEXT NOT NULL, created_at TEXT NOT NULL, expired_at TEXT, storage_key TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, UNIQUE(run_id,path));
CREATE INDEX IF NOT EXISTS artifacts_job ON artifacts(job_id);
`

func (s *Store) CreateTaskJob(ctx context.Context, task protocol.Task, repository, name string, steps []config.WorkflowStep) (string, error) {
	if err := task.Validate(); err != nil {
		return "", err
	}
	return s.createWorkflowJob(ctx, task.Brief(), repository, name, steps, &task)
}

// Snapshots both the brief and exact upstream artifact identities before dispatch.
func bindTaskInputs(ctx context.Context, tx *sql.Tx, job, run string, step config.WorkflowStep) (string, error) {
	var task, plan string
	err := tx.QueryRowContext(ctx, "SELECT task FROM task_inputs WHERE job_id=?", job).Scan(&task)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err = tx.QueryRowContext(ctx, "SELECT plan FROM workflow_jobs WHERE job_id=?", job).Scan(&plan); err != nil {
		return "", err
	}
	var steps []config.WorkflowStep
	if err = json.Unmarshal([]byte(plan), &steps); err != nil {
		return "", err
	}
	inputs := map[string]protocol.Artifact{}
	problem := ""
	for alias, ref := range step.Inputs {
		parts := strings.SplitN(ref, "/", 2)
		index := -1
		for i, s := range steps {
			if s.ID == parts[0] {
				index = i
				break
			}
		}
		if len(parts) != 2 || index < 0 {
			return "", fmt.Errorf("invalid input %q", ref)
		}
		// Most recent successful attempt, never a file from a blocked attempt.
		a, _, e := scanArtifact(tx.QueryRowContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE path=? AND run_id=(SELECT r.id FROM runs r JOIN workflow_attempts w ON w.run_id=r.id WHERE r.job_id=? AND w.step=? AND w.outcome='complete' ORDER BY r.rowid DESC LIMIT 1)`, parts[1], job, index))
		if errors.Is(e, sql.ErrNoRows) {
			problem = "Missing input " + ref
			continue
		}
		if e != nil {
			return "", e
		}
		if a.ExpiredAt != nil {
			problem = "Expired input " + ref
			continue
		}
		inputs[alias] = a
	}
	if step.SharedOutputs {
		rows, err := tx.QueryContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE run_id=(SELECT r.id FROM runs r JOIN workflow_attempts w ON w.run_id=r.id WHERE r.job_id=? AND w.outcome='complete' ORDER BY r.rowid DESC LIMIT 1)`, job)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			a, _, err := scanArtifact(rows)
			if err != nil {
				rows.Close()
				return "", err
			}
			if a.ExpiredAt != nil {
				problem = "Expired task file " + a.Path
			}
			inputs["__workspace__/"+a.Path] = a
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}

	encoded, _ := json.Marshal(inputs)
	required, _ := json.Marshal(step.RequiredOutputs)
	_, err = tx.ExecContext(ctx, "INSERT INTO execution_inputs(run_id,task,inputs,required) VALUES(?,?,?,?)", run, task, string(encoded), string(required))
	return problem, err
}
func (s *Store) enrichRun(ctx context.Context, tx *sql.Tx, run *protocol.RunSpec) error {
	var task, inputs, required string
	err := tx.QueryRowContext(ctx, "SELECT task,inputs,required FROM execution_inputs WHERE run_id=?", run.ID).Scan(&task, &inputs, &required)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(task), &run.Task); err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(inputs), &run.Inputs); err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(required), &run.RequiredOutputs); err != nil {
		return err
	}
	run.ArtifactLimits = &protocol.ArtifactLimits{MaxFileBytes: s.storageConfig.MaxFileBytes, MaxRunBytes: s.storageConfig.MaxRunBytes}
	return nil
}
func (s *Store) loadTasks(ctx context.Context, jobs []Job) error {
	rows, err := s.db.QueryContext(ctx, "SELECT job_id,task FROM task_inputs")
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[string]*Job{}
	for i := range jobs {
		byID[jobs[i].ID] = &jobs[i]
	}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			return err
		}
		if j := byID[id]; j != nil {
			if err = json.Unmarshal([]byte(raw), &j.Task); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}
func validateOutputs(ctx context.Context, tx *sql.Tx, run string, c protocol.Completion) (string, error) {
	var required, inputs string
	err := tx.QueryRowContext(ctx, "SELECT required,inputs FROM execution_inputs WHERE run_id=?", run).Scan(&required, &inputs)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if c.PublicationError != "" {
		return "Output publication failed: " + c.PublicationError, nil
	}
	paths := map[string]bool{}
	for _, id := range c.Artifacts {
		var p string
		err = tx.QueryRowContext(ctx, "SELECT path FROM artifacts WHERE id=? AND run_id=? AND expired_at IS NULL", id, run).Scan(&p)
		if errors.Is(err, sql.ErrNoRows) {
			return "Output manifest contains an unavailable artifact", nil
		}
		if err != nil {
			return "", err
		}
		paths[p] = true
	}

	// A completed stage is a full snapshot, not a subset of uploaded files.
	var published int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM artifacts WHERE run_id=? AND expired_at IS NULL", run).Scan(&published); err != nil {
		return "", err
	}
	if published != len(paths) {
		return "Output manifest must include every published file", nil
	}
	var expected []string
	if err = json.Unmarshal([]byte(required), &expected); err != nil {
		return "", err
	}
	for _, p := range expected {
		if !paths[p] {
			return "Missing required output: " + p, nil
		}
	}
	return "", nil
}
