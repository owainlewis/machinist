package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	fakeCloudPollInterval  = 100 * time.Millisecond
	fakeCloudTimeoutReason = "Execution timed out."
)

// RunFakeCloudDispatcher drives the deterministic fake cloud backend through
// the same durable lifecycle tables as persistent Workers.
func (s *Store) RunFakeCloudDispatcher(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(fakeCloudPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.DispatchFakeCloud(ctx, 20); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("fake_cloud_dispatch_failed", "error_class", "storage_unavailable")
			}
		}
	}
}

// DispatchFakeCloud performs a bounded, deterministic reconciliation pass. It
// is exported so store tests can advance the fake without timing assumptions.
func (s *Store) DispatchFakeCloud(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, invalid("invalid_fake_cloud_batch", "fake cloud batch must be between 1 and 100")
	}
	processed := 0
	for processed < limit {
		changed, err := s.dispatchOneFakeCloud(ctx)
		if err != nil {
			return processed, err
		}
		if !changed {
			break
		}
		processed++
	}
	return processed, nil
}

func (s *Store) dispatchOneFakeCloud(ctx context.Context) (bool, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, unavailable(err)
	}
	defer tx.Rollback()

	// Cancellation is a Factory decision. The fake provider only supplies the
	// terminal evidence needed to apply it to the ordinary Attempt lifecycle.
	var cancelledAttempt, cancelledExecution string
	err = tx.QueryRowContext(ctx, `
		SELECT attempt.id, execution.id
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN sessions session ON session.id = execution.session_id
		WHERE session.execution_backend = 'fake_cloud_run'
		  AND attempt.state IN ('preparing', 'running')
		  AND (session.cancellation_requested = 1 OR execution.cancellation_requested = 1)
		ORDER BY attempt.created_at, attempt.id LIMIT 1
	`).Scan(&cancelledAttempt, &cancelledExecution)
	if err == nil {
		if err := completeFakeCloudAttempt(ctx, tx, cancelledAttempt, cancelledExecution,
			"cancelled", "", "Cancelled by operator.", "Cancelled by operator.", now); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, unavailable(err)
		}
		return true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, unavailable(err)
	}

	// The frozen Session timeout bounds the fake execution just as it will bound
	// the real cloud wrapper. Resolve timeout before extending the lease.
	var timedOutAttempt, timedOutExecution string
	err = tx.QueryRowContext(ctx, `
		SELECT attempt.id, execution.id
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN sessions session ON session.id = execution.session_id
		WHERE session.execution_backend = 'fake_cloud_run'
		  AND attempt.state IN ('preparing', 'running')
		  AND attempt.started_at + (session.timeout_seconds * 1000) <= ?
		ORDER BY attempt.started_at, attempt.id LIMIT 1
	`, now).Scan(&timedOutAttempt, &timedOutExecution)
	if err == nil {
		if err := completeFakeCloudAttempt(ctx, tx, timedOutAttempt, timedOutExecution,
			"failed", "", fakeCloudTimeoutReason, fakeCloudTimeoutReason, now); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, unavailable(err)
		}
		return true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, unavailable(err)
	}

	// Keep deliberately running fake Attempts leased until Factory cancels them.
	if _, err := tx.ExecContext(ctx, `
		UPDATE attempts SET lease_expires_at = ?
		WHERE state IN ('preparing', 'running')
		  AND execution_id IN (
		      SELECT execution.id FROM executions execution
		      JOIN sessions session ON session.id = execution.session_id
		      WHERE session.execution_backend = 'fake_cloud_run'
		  )
	`, now+protocol.LeaseDuration.Milliseconds()); err != nil {
		return false, unavailable(err)
	}

	// Release the next eligible blocked Session into its frozen pool. This also
	// reconsiders Run admitted while its profile was disabled or unhealthy.
	var blockedSession, blockedProfile, blockedRuntime string
	err = tx.QueryRowContext(ctx, `
		SELECT session.id, session.execution_profile_id, session.required_runtime
		FROM sessions session
		JOIN runs run ON run.id = session.run_id
		JOIN repositories repository ON repository.id = session.repository_id
		JOIN execution_profiles profile ON profile.id = session.execution_profile_id
		JOIN execution_profile_versions version
		  ON version.profile_id = session.execution_profile_id
		 AND version.version = session.execution_profile_version
		WHERE session.execution_backend = 'fake_cloud_run'
		  AND session.state = 'blocked'
		  AND (repository.centrally_managed = 0 OR repository.enabled = 1)
		  AND profile.enabled = 1 AND profile.healthy = 1
		  AND version.runtime = session.required_runtime
		  AND (
		      SELECT COUNT(*) FROM sessions active
		      WHERE active.run_id = session.run_id AND active.state IN ('queued','preparing','running')
		  ) < json_extract(run.task_snapshot, '$.concurrency_limit')
		  AND (
		      SELECT COUNT(*) FROM attempts active_attempt
		      WHERE active_attempt.worker_id = 'cloud-run-' || session.execution_profile_id
		        AND active_attempt.state IN ('preparing','running')
		  ) < version.max_concurrent
		ORDER BY (
			SELECT COUNT(*)
			FROM attempts previous_attempt
			JOIN executions previous_execution ON previous_execution.id = previous_attempt.execution_id
			JOIN sessions previous_session ON previous_session.id = previous_execution.session_id
			WHERE previous_session.run_id = session.run_id
		), run.admitted_at, session.admitted_at, session.id LIMIT 1
	`).Scan(&blockedSession, &blockedProfile, &blockedRuntime)
	if err == nil {
		executionID, idErr := newID()
		if idErr != nil {
			return false, unavailable(idErr)
		}
		workerID := syntheticWorkerID(blockedProfile)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO executions(id, session_id, assigned_worker_id, required_runtime, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'queued', ?, ?)
		`, executionID, blockedSession, workerID, blockedRuntime, now, now); err != nil {
			return false, unavailable(err)
		}
		released, err := tx.ExecContext(ctx, `
				UPDATE sessions SET state = 'queued', blocked_reason = NULL, assigned_worker_id = ?,
				       waiting_reason = '', execution_owner = 'none' WHERE id = ?
			  AND state = 'blocked'
		`, workerID, blockedSession)
		if err != nil {
			return false, unavailable(err)
		}
		changed, err := released.RowsAffected()
		if err != nil {
			return false, unavailable(err)
		}
		if changed != 1 {
			return false, conflict("session_route_conflict", "Session routing state changed before assignment")
		}
		if err := tx.Commit(); err != nil {
			return false, unavailable(err)
		}
		return true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, unavailable(err)
	}

	var executionID, sessionID, workerID, outcome, result, failure string
	err = tx.QueryRowContext(ctx, `
		SELECT execution.id, session.id, execution.assigned_worker_id,
		       version.fake_outcome, version.fake_result, version.fake_error
		FROM executions execution
		JOIN sessions session ON session.id = execution.session_id
		JOIN runs run ON run.id = session.run_id
		JOIN execution_profiles profile ON profile.id = session.execution_profile_id
		JOIN execution_profile_versions version
		  ON version.profile_id = session.execution_profile_id
		 AND version.version = session.execution_profile_version
		WHERE session.execution_backend = 'fake_cloud_run'
		  AND session.state = 'queued' AND execution.state = 'queued'
		  AND profile.enabled = 1 AND profile.healthy = 1
		  AND (
		      SELECT COUNT(*) FROM attempts active_attempt
		      WHERE active_attempt.worker_id = execution.assigned_worker_id
		        AND active_attempt.state IN ('preparing','running')
		  ) < version.max_concurrent
		ORDER BY (
			SELECT COUNT(*)
			FROM attempts previous_attempt
			JOIN executions previous_execution ON previous_execution.id = previous_attempt.execution_id
			JOIN sessions previous_session ON previous_session.id = previous_execution.session_id
			WHERE previous_session.run_id = session.run_id
		), run.admitted_at, execution.created_at, execution.id LIMIT 1
	`).Scan(&executionID, &sessionID, &workerID, &outcome, &result, &failure)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return false, unavailable(err)
		}
		return false, nil
	}
	if err != nil {
		return false, unavailable(err)
	}
	attemptID, err := newID()
	if err != nil {
		return false, unavailable(err)
	}
	leaseToken, err := randomWorkerSecret("factory_fake_cloud_")
	if err != nil {
		return false, unavailable(err)
	}
	var attemptNumber int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_number), 0) + 1 FROM attempts WHERE execution_id = ?`, executionID).Scan(&attemptNumber); err != nil {
		return false, unavailable(err)
	}
	expires := s.now().Add(protocol.LeaseDuration).UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO attempts(
			id, execution_id, worker_id, attempt_number, state, lease_digest, lease_expires_at,
			process_identity, started_at, created_at
		) VALUES (?, ?, ?, ?, 'running', ?, ?, 'fake-cloud-run', ?, ?)
	`, attemptID, executionID, workerID, attemptNumber, digestToken(leaseToken), expires, now, now); err != nil {
		return false, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE executions SET state = 'running', updated_at = ? WHERE id = ? AND state = 'queued'`, now, executionID); err != nil {
		return false, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET state = 'running', started_at = COALESCE(started_at, ?),
		       execution_owner = 'worker_attempt'
		WHERE id = ? AND state = 'queued'
	`, now, sessionID); err != nil {
		return false, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = 'running', started_at = COALESCE(started_at, ?)
		WHERE session_id = ? AND position = 0 AND state = 'pending'
	`, now, sessionID); err != nil {
		return false, unavailable(err)
	}
	if outcome == "succeeded" || outcome == "failed" {
		if err := completeFakeCloudAttempt(ctx, tx, attemptID, executionID, outcome, result, failure, "", now); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workers SET last_heartbeat = ?, active_count = (
			SELECT COUNT(*) FROM attempts WHERE worker_id = ? AND state IN ('preparing','running')
		) WHERE id = ? AND synthetic = 1
	`, now, workerID, workerID); err != nil {
		return false, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return false, unavailable(err)
	}
	return true, nil
}

func completeFakeCloudAttempt(
	ctx context.Context,
	tx *sql.Tx,
	attemptID, executionID, state, result, failure, terminalMessage string,
	now int64,
) error {
	stageState := state
	if stageState == "cancelled" {
		stageState = string(protocol.StageCancelled)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = ?, result = ?, error = ?, completed_at = ?
		WHERE session_id = (SELECT session_id FROM executions WHERE id = ?) AND state = 'running'
	`, stageState, result, failure, now, executionID); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE attempts SET state = ?, result = ?, error = ?, completed_at = ?
		WHERE id = ? AND state IN ('preparing','running')
	`, state, nullString(result), nullString(failure), now, attemptID); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE executions SET state = ?, cancellation_requested = CASE WHEN ? = 'cancelled' THEN 1 ELSE cancellation_requested END,
		       updated_at = ? WHERE id = ? AND state IN ('preparing','running')
	`, state, state, now, executionID); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET state = ?, cancellation_requested = CASE WHEN ? = 'cancelled' THEN 1 ELSE cancellation_requested END,
		       terminal_at = ?, result = ?, failure_reason = ?,
		       terminal_message = ?, execution_owner = 'none'
		WHERE id = (SELECT session_id FROM executions WHERE id = ?)
		  AND state IN ('preparing','running')
	`, state, state, now, nullString(result), nullString(failure), terminalMessage, executionID); err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workers SET active_count = (
			SELECT COUNT(*) FROM attempts active
			WHERE active.worker_id = workers.id AND active.state IN ('preparing','running')
		)
		WHERE id = (SELECT worker_id FROM attempts WHERE id = ?) AND synthetic = 1
	`, attemptID); err != nil {
		return unavailable(err)
	}
	return updateRunLifecycle(ctx, tx, executionID, now)
}
