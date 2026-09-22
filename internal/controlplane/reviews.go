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

const reviewSchema = `
CREATE TABLE IF NOT EXISTS review_gates(run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE, reviewed_run_id TEXT NOT NULL REFERENCES runs(id), decision TEXT NOT NULL DEFAULT '', feedback TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS execution_reviews(run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE, context TEXT NOT NULL);
INSERT OR IGNORE INTO review_gates(run_id,reviewed_run_id)
 SELECT r.id,(SELECT p.id FROM runs p JOIN workflow_attempts pa ON pa.run_id=p.id WHERE p.job_id=r.job_id AND pa.step=a.step-1 AND pa.outcome='complete' ORDER BY p.rowid DESC LIMIT 1)
 FROM runs r JOIN workflow_attempts a ON a.run_id=r.id WHERE r.state='awaiting_approval' AND a.step>0
 AND EXISTS(SELECT 1 FROM runs p JOIN workflow_attempts pa ON pa.run_id=p.id WHERE p.job_id=r.job_id AND pa.step=a.step-1 AND pa.outcome='complete');
`

func bindReviewGate(ctx context.Context, tx *sql.Tx, job, run string, index int) error {
	if index == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO review_gates(run_id,reviewed_run_id) SELECT ?,r.id FROM runs r JOIN workflow_attempts a ON a.run_id=r.id WHERE r.job_id=? AND a.step=? AND a.outcome='complete' ORDER BY r.rowid DESC LIMIT 1`, run, job, index-1)
	return err
}

func requestChanges(ctx context.Context, tx *sql.Tx, job, gate, repository, plan, feedback, now string, index int) error {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" || len(feedback) > 16000 {
		return fmt.Errorf("%w: feedback must contain 1–16000 bytes", ErrWorkflowAction)
	}
	var revision protocol.Revision
	if err := tx.QueryRowContext(ctx, `SELECT g.reviewed_run_id,a.summary FROM review_gates g JOIN workflow_attempts a ON a.run_id=g.reviewed_run_id WHERE g.run_id=? AND g.decision=''`, gate).Scan(&revision.PreviousRunID, &revision.PreviousSummary); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no completed stage to revise", ErrWorkflowAction)
		}
		return err
	}
	revision.Feedback = feedback
	var previousContext string
	err := tx.QueryRowContext(ctx, "SELECT context FROM execution_reviews WHERE run_id=?", revision.PreviousRunID).Scan(&previousContext)
	if err == nil {
		var previous protocol.Revision
		if err = json.Unmarshal([]byte(previousContext), &previous); err != nil {
			return err
		}
		revision.PriorFeedback = append(previous.PriorFeedback, previous.Feedback)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(revision.PriorFeedback) >= 100 {
		return fmt.Errorf("%w: revision limit reached", ErrWorkflowAction)
	}
	revision.Artifacts = map[string]protocol.Artifact{}
	rows, err := tx.QueryContext(ctx, "SELECT "+artifactColumns+" FROM artifacts WHERE run_id=?", revision.PreviousRunID)
	if err != nil {
		return err
	}
	for rows.Next() {
		a, _, e := scanArtifact(rows)
		if e != nil {
			rows.Close()
			return e
		}
		if a.ExpiredAt != nil {
			rows.Close()
			return fmt.Errorf("%w: previous output %s has expired", ErrWorkflowAction, a.Path)
		}
		revision.Artifacts["__review_"+a.ID] = a
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var steps []config.WorkflowStep
	if err = json.Unmarshal([]byte(plan), &steps); err != nil {
		return err
	}
	if index <= 0 || index >= len(steps) {
		return ErrWorkflowAction
	}
	// Supersede this gate. It can never start with the rejected version's inputs.
	if _, err = tx.ExecContext(ctx, "UPDATE review_gates SET decision='changes_requested',feedback=? WHERE run_id=?", feedback, gate); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE runs SET state='cancelled',completed_at=? WHERE id=?", now, gate); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE workflow_attempts SET outcome='changes_requested',summary=? WHERE run_id=?", feedback, gate); err != nil {
		return err
	}
	// The producing stage was already authorized; don't repeat its entry gate.
	if err = enqueueStep(ctx, tx, job, repository, index-1, steps[index-1], true, now); err != nil {
		return err
	}
	var next string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM runs WHERE job_id=? ORDER BY rowid DESC LIMIT 1", job).Scan(&next); err != nil {
		return err
	}
	raw, err := json.Marshal(revision)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO execution_reviews(run_id,context) VALUES(?,?)", next, string(raw))
	return err
}

func enrichReview(ctx context.Context, tx *sql.Tx, run *protocol.RunSpec) error {
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT context FROM execution_reviews WHERE run_id=?", run.ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(raw), &run.Revision); err != nil {
		return err
	}
	if run.Inputs == nil {
		run.Inputs = map[string]protocol.Artifact{}
	}
	for alias, a := range run.Revision.Artifacts {
		if _, exists := run.Inputs[alias]; exists {
			return errors.New("revision input alias collision")
		}
		run.Inputs[alias] = a
	}
	return nil
}

func (s *Store) loadReviews(ctx context.Context, jobs []Job) error {
	byID := map[string]*Run{}
	for i := range jobs {
		for j := range jobs[i].Runs {
			r := &jobs[i].Runs[j]
			byID[r.ID] = r
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,COALESCE(e.context,''),COALESCE(g.reviewed_run_id,'') FROM runs r LEFT JOIN execution_reviews e ON e.run_id=r.id LEFT JOIN review_gates g ON g.run_id=r.id WHERE e.run_id IS NOT NULL OR g.run_id IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw, reviewed string
		if err = rows.Scan(&id, &raw, &reviewed); err != nil {
			return err
		}
		if r := byID[id]; r != nil {
			r.ReviewedRunID = reviewed
			if raw != "" {
				if err = json.Unmarshal([]byte(raw), &r.Revision); err != nil {
					return err
				}
			}
		}
	}
	return rows.Err()
}
