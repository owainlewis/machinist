package controlplane

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) materializeBlockedSessionForWorker(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	now int64,
) error {
	type candidate struct {
		id, repositoryID, identity, runtime string
		fairCount, runAdmitted, admittedAt  int64
	}
	var cursorFair, cursorRunAdmitted, cursorAdmitted int64
	var cursorID string
	for {
		rows, err := tx.QueryContext(ctx, `
			SELECT candidate.id, candidate.repository_id, candidate.repository_identity,
			       candidate.required_runtime, candidate.fair_count,
			       candidate.run_admitted_at, candidate.admitted_at
			FROM (
				SELECT session.id, session.repository_id, session.repository_identity,
				       session.required_runtime, session.admitted_at,
				       run.admitted_at AS run_admitted_at,
				       (
						SELECT COUNT(*)
						FROM attempts previous_attempt
						JOIN executions previous_execution ON previous_execution.id = previous_attempt.execution_id
						JOIN sessions previous_session ON previous_session.id = previous_execution.session_id
						WHERE previous_session.run_id = session.run_id
				       ) AS fair_count
				FROM sessions session
				JOIN runs run ON run.id = session.run_id
				JOIN repositories repository ON repository.id = session.repository_id
				WHERE session.state = 'blocked'
				  AND session.execution_backend = 'persistent'
				  AND (repository.centrally_managed = 0 OR repository.enabled = 1)
				  AND (
				      SELECT COUNT(*) FROM sessions active
				      WHERE active.run_id = session.run_id AND active.state IN ('queued', 'preparing', 'running')
				  ) < json_extract(run.task_snapshot, '$.concurrency_limit')
			) candidate
			WHERE (? = '' OR candidate.fair_count > ?
			  OR (candidate.fair_count = ? AND (candidate.run_admitted_at > ?
			      OR (candidate.run_admitted_at = ? AND (candidate.admitted_at > ?
			          OR (candidate.admitted_at = ? AND candidate.id > ?))))))
			ORDER BY candidate.fair_count, candidate.run_admitted_at, candidate.admitted_at, candidate.id LIMIT 50
		`, cursorID, cursorFair, cursorFair, cursorRunAdmitted, cursorRunAdmitted,
			cursorAdmitted, cursorAdmitted, cursorID)
		if err != nil {
			return unavailable(err)
		}
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(
				&value.id, &value.repositoryID, &value.identity, &value.runtime,
				&value.fairCount, &value.runAdmitted, &value.admittedAt,
			); err != nil {
				rows.Close()
				return unavailable(err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return unavailable(err)
		}
		if err := rows.Close(); err != nil {
			return unavailable(err)
		}
		for _, value := range candidates {
			selection, err := s.selectSessionRoute(
				ctx, tx, value.repositoryID, value.identity, now, workerID, value.runtime,
			)
			if err != nil {
				if serviceErrorCode(err, "no_eligible_worker") || serviceErrorCode(err, "repository_not_managed") {
					continue
				}
				return err
			}
			executionID, err := newID()
			if err != nil {
				return unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO executions(id, session_id, assigned_worker_id, required_runtime, state, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'queued', ?, ?)
			`, executionID, value.id, selection.workerID, value.runtime, now, now); err != nil {
				return unavailable(err)
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE sessions SET state = 'queued', blocked_reason = NULL, assigned_worker_id = ?,
				       waiting_reason = '', execution_owner = 'none'
				WHERE id = ? AND state = 'blocked'
			`, selection.workerID, value.id)
			if err != nil {
				return unavailable(err)
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return conflict("session_route_conflict", "Session routing state changed before assignment")
			}
			return nil
		}
		if len(candidates) < 50 {
			return nil
		}
		last := candidates[len(candidates)-1]
		cursorFair, cursorRunAdmitted, cursorAdmitted, cursorID =
			last.fairCount, last.runAdmitted, last.admittedAt, last.id
	}
}

func (s *Store) rerouteQueuedSessionForWorker(
	ctx context.Context,
	tx *sql.Tx,
	workerID string,
	now int64,
) error {
	type candidate struct {
		sessionID, executionID, repositoryID, identity, runtime, assigned string
		admittedAt                                                        int64
	}
	var cursorAdmitted int64
	var cursorID string
	for {
		rows, err := tx.QueryContext(ctx, `
			SELECT session.id, execution.id, session.repository_id, session.repository_identity,
			       session.required_runtime, execution.assigned_worker_id, session.admitted_at
			FROM sessions session
			JOIN executions execution ON execution.session_id = session.id
			WHERE session.state = 'queued' AND execution.state = 'queued'
			  AND session.execution_backend = 'persistent'
			  AND execution.assigned_worker_id != ?
			  AND (? = '' OR session.admitted_at > ? OR (session.admitted_at = ? AND session.id > ?))
			ORDER BY session.admitted_at, session.id LIMIT 50
		`, workerID, cursorID, cursorAdmitted, cursorAdmitted, cursorID)
		if err != nil {
			return unavailable(err)
		}
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(&value.sessionID, &value.executionID, &value.repositoryID, &value.identity,
				&value.runtime, &value.assigned, &value.admittedAt); err != nil {
				rows.Close()
				return unavailable(err)
			}
			candidates = append(candidates, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return unavailable(err)
		}
		if err := rows.Close(); err != nil {
			return unavailable(err)
		}
		for _, value := range candidates {
			if _, err := s.selectSessionRoute(ctx, tx, value.repositoryID, value.identity, now, value.assigned, value.runtime); err == nil {
				continue
			} else if !serviceErrorCode(err, "no_eligible_worker") && !serviceErrorCode(err, "repository_not_managed") {
				return err
			}
			selection, err := s.selectSessionRoute(ctx, tx, value.repositoryID, value.identity, now, workerID, value.runtime)
			if err != nil {
				var service *ServiceError
				if errors.As(err, &service) && (service.Code == "no_eligible_worker" || service.Code == "repository_not_managed") {
					continue
				}
				return err
			}
			result, err := tx.ExecContext(ctx, `
				UPDATE executions SET assigned_worker_id = ?, updated_at = ?
				WHERE id = ? AND state = 'queued' AND assigned_worker_id = ?
			`, selection.workerID, now, value.executionID, value.assigned)
			if err != nil {
				return unavailable(err)
			}
			if changed, _ := result.RowsAffected(); changed == 1 {
				if _, err := tx.ExecContext(ctx, `UPDATE sessions SET assigned_worker_id = ? WHERE id = ?`, selection.workerID, value.sessionID); err != nil {
					return unavailable(err)
				}
				return nil
			}
		}
		if len(candidates) < 50 {
			return nil
		}
		last := candidates[len(candidates)-1]
		cursorAdmitted, cursorID = last.admittedAt, last.sessionID
	}
}

func updateRunLifecycle(ctx context.Context, tx *sql.Tx, executionID string, now int64) error {
	var runID string
	err := tx.QueryRowContext(ctx, `
		SELECT session.run_id
		FROM executions execution
		JOIN sessions session ON session.id = execution.session_id
		WHERE execution.id = ?
	`, executionID).Scan(&runID)
	if err != nil {
		return unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET updated_at = ?, terminal_at = CASE
			WHEN NOT EXISTS (
				SELECT 1 FROM sessions session
				WHERE session.run_id = runs.id
				  AND session.state IN ('blocked','queued','preparing','running','needs-input')
			) THEN ? ELSE NULL END
		WHERE id = ?
	`, now, now, runID); err != nil {
		return unavailable(err)
	}
	return nil
}
