package controlplane

import (
	"context"
	"database/sql"
	"errors"

	"github.com/owainlewis/factory/internal/protocol"
)

type rowScanner interface {
	Scan(...any) error
}

func scanStageRun(row rowScanner) (protocol.StageRun, error) {
	var stage protocol.StageRun
	var result, failure string
	var started, completed sql.NullInt64
	err := row.Scan(&stage.Position, &stage.Name, &stage.Prompt, &stage.State, &result, &failure, &started, &completed)
	if err != nil {
		return stage, err
	}
	stage.Result, stage.Error = result, failure
	if started.Valid {
		value := fromMillis(started.Int64)
		stage.StartedAt = &value
	}
	if completed.Valid {
		value := fromMillis(completed.Int64)
		stage.CompletedAt = &value
	}
	return stage, nil
}

func (s *Store) StartStage(ctx context.Context, attemptID string, position int, input protocol.StartStageRequest) (protocol.StageRun, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return protocol.StageRun{}, err
	}
	if err := verifyActiveLease(lease, input.LeaseToken, now); err != nil {
		return protocol.StageRun{}, err
	}
	if lease.attemptState != "running" || lease.executionState != "running" {
		return protocol.StageRun{}, conflict("attempt_not_running", "Pipeline stages require a running Attempt")
	}
	if lease.cancel {
		return protocol.StageRun{}, conflict("cancellation_requested", "the Attempt is being cancelled")
	}
	var sessionID, state string
	err = tx.QueryRowContext(ctx, `
		SELECT execution.session_id, stage.state
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN session_stages stage ON stage.session_id = execution.session_id AND stage.position = ?
		WHERE attempt.id = ?
	`, position, attemptID).Scan(&sessionID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.StageRun{}, ErrNotFound
	}
	if err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if state != string(protocol.StagePending) && state != string(protocol.StageRunning) {
		return protocol.StageRun{}, conflict("stage_not_pending", "the Pipeline stage cannot be started from its current state")
	}
	var incomplete int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_stages WHERE session_id = ? AND position < ? AND state != 'succeeded'`, sessionID, position).Scan(&incomplete); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if incomplete != 0 {
		return protocol.StageRun{}, conflict("stage_predecessor_incomplete", "the previous Pipeline stage has not succeeded")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = 'running', started_at = COALESCE(started_at, ?)
		WHERE session_id = ? AND position = ?
	`, now, sessionID, position); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE attempts SET supervisor_pid = ?, process_identity = ?, process_group_id = ? WHERE id = ?
	`, input.SupervisorPID, nullString(input.ProcessIdentity), input.ProcessGroupID, attemptID); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	return s.stageRun(ctx, sessionID, position)
}

func (s *Store) CompleteStage(ctx context.Context, attemptID string, position int, input protocol.CompleteStageRequest) (protocol.StageRun, error) {
	if input.State != protocol.StageSucceeded && input.State != protocol.StageFailed && input.State != protocol.StageCancelled {
		return protocol.StageRun{}, invalid("invalid_stage_state", "state must be succeeded, failed, or cancelled")
	}
	if len([]byte(input.Result)) > protocol.MaxResultBytes || len([]byte(input.Error)) > protocol.MaxErrorBytes {
		return protocol.StageRun{}, invalid("stage_result_too_large", "stage result or error exceeds its storage limit")
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return protocol.StageRun{}, err
	}
	if err := verifyActiveLease(lease, input.LeaseToken, now); err != nil {
		return protocol.StageRun{}, err
	}
	if lease.attemptState != "running" || lease.executionState != "running" {
		return protocol.StageRun{}, conflict("attempt_not_running", "Pipeline stages require a running Attempt")
	}
	var sessionID, state, result, failure string
	err = tx.QueryRowContext(ctx, `
		SELECT execution.session_id, stage.state, stage.result, stage.error
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN session_stages stage ON stage.session_id = execution.session_id AND stage.position = ?
		WHERE attempt.id = ?
	`, position, attemptID).Scan(&sessionID, &state, &result, &failure)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.StageRun{}, ErrNotFound
	}
	if err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if state != string(protocol.StageRunning) {
		if state == string(input.State) && result == input.Result && failure == input.Error {
			if err := tx.Commit(); err != nil {
				return protocol.StageRun{}, unavailable(err)
			}
			return s.stageRun(ctx, sessionID, position)
		}
		return protocol.StageRun{}, conflict("stage_not_running", "the Pipeline stage cannot complete from its current state")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = ?, result = ?, error = ?, completed_at = ?
		WHERE session_id = ? AND position = ? AND state = 'running'
	`, input.State, input.Result, input.Error, now, sessionID, position); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	if input.State != protocol.StageSucceeded {
		if _, err := tx.ExecContext(ctx, `UPDATE session_stages SET state = 'cancelled', completed_at = ? WHERE session_id = ? AND position > ? AND state = 'pending'`, now, sessionID, position); err != nil {
			return protocol.StageRun{}, unavailable(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.StageRun{}, unavailable(err)
	}
	return s.stageRun(ctx, sessionID, position)
}

func (s *Store) stageRun(ctx context.Context, sessionID string, position int) (protocol.StageRun, error) {
	var stage protocol.StageRun
	var started, completed sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT position, name, state, started_at, completed_at
		FROM session_stages WHERE session_id = ? AND position = ?
	`, sessionID, position).Scan(&stage.Position, &stage.Name, &stage.State, &started, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return stage, ErrNotFound
	}
	if err != nil {
		return stage, unavailable(err)
	}
	if started.Valid {
		value := fromMillis(started.Int64)
		stage.StartedAt = &value
	}
	if completed.Valid {
		value := fromMillis(completed.Int64)
		stage.CompletedAt = &value
	}
	return stage, nil
}
