package controlplane

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

func (s *Store) Claim(ctx context.Context, workerID string, input protocol.ClaimRequest) (*protocol.Claim, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" || len(input.RequestID) > 200 {
		return nil, invalid("invalid_request_id", "request_id is required")
	}
	if err := validateToken(input.LeaseToken); err != nil {
		return nil, err
	}
	digest := digestToken(input.LeaseToken)
	now := s.now()
	nowMillis := now.UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, unavailable(err)
	}
	defer tx.Rollback()
	var capacity, healthy, claimProtocolVersion, synthetic int
	var lastHeartbeat int64
	err = tx.QueryRowContext(ctx, `
		SELECT capacity, health = 'healthy', last_heartbeat, claim_protocol_version, synthetic
		FROM workers WHERE id = ?
	`, workerID).Scan(&capacity, &healthy, &lastHeartbeat, &claimProtocolVersion, &synthetic)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, unavailable(err)
	}
	if synthetic != 0 {
		return nil, conflict("synthetic_worker_isolated", "synthetic cloud Workers cannot use Worker claim routes")
	}
	if claimProtocolVersion != protocol.ClaimProtocolVersion {
		return nil, conflict(
			"worker_upgrade_required",
			"the Worker uses an incompatible claim protocol; upgrade it before claiming a Session",
		)
	}

	var storedDigest []byte
	var storedAttempt sql.NullString
	var claimCreated int64
	err = tx.QueryRowContext(ctx, `
		SELECT lease_digest, attempt_id, created_at FROM claim_requests
		WHERE worker_id = ? AND request_id = ?
	`, workerID, input.RequestID).Scan(&storedDigest, &storedAttempt, &claimCreated)
	if err == nil {
		if !equalDigest(storedDigest, digest) {
			return nil, conflict("claim_request_conflict", "request_id was already used with a different lease token")
		}
		if !storedAttempt.Valid {
			if now.Sub(fromMillis(claimCreated)) <= protocol.EmptyClaimTTL {
				if err := tx.Commit(); err != nil {
					return nil, unavailable(err)
				}
				return nil, nil
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM claim_requests WHERE worker_id = ? AND request_id = ?`, workerID, input.RequestID); err != nil {
				return nil, unavailable(err)
			}
		} else {
			var state string
			var expiry int64
			var attemptDigest []byte
			err := tx.QueryRowContext(ctx, `
				SELECT state, lease_expires_at, lease_digest FROM attempts WHERE id = ?
			`, storedAttempt.String).Scan(&state, &expiry, &attemptDigest)
			if err != nil {
				return nil, unavailable(err)
			}
			if !equalDigest(attemptDigest, digest) || !isActive(state) || expiry <= nowMillis {
				return nil, conflict("lease_not_owner", "the claim no longer owns an active lease")
			}
			if err := tx.Commit(); err != nil {
				return nil, unavailable(err)
			}
			claim, err := s.claimDetail(ctx, storedAttempt.String)
			return &claim, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, unavailable(err)
	}

	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM attempts
		WHERE worker_id = ? AND state IN ('preparing', 'running')
	`, workerID).Scan(&active); err != nil {
		return nil, unavailable(err)
	}
	if healthy == 0 || now.Sub(fromMillis(lastHeartbeat)) > protocol.WorkerOnlineWindow || active >= capacity {
		if err := insertEmptyClaim(ctx, tx, workerID, input.RequestID, digest, nowMillis); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, unavailable(err)
		}
		return nil, nil
	}
	if err := s.materializeBlockedSessionForWorker(ctx, tx, workerID, nowMillis); err != nil {
		return nil, err
	}
	if err := s.rerouteQueuedSessionForWorker(ctx, tx, workerID, nowMillis); err != nil {
		return nil, err
	}

	var executionID string
	err = tx.QueryRowContext(ctx, `
		SELECT e.id
		FROM executions e
		JOIN sessions session ON session.id = e.session_id
		JOIN runs run ON run.id = session.run_id
		JOIN repositories repository ON repository.id = session.repository_id
		JOIN worker_repositories wr
		  ON wr.worker_id = e.assigned_worker_id AND wr.repository_id = session.repository_id
		WHERE e.assigned_worker_id = ?
		  AND EXISTS (
		      SELECT 1
		      FROM workers claim_worker, json_each(claim_worker.capabilities_json) capability
		      WHERE claim_worker.id = ?
		        AND json_extract(capability.value, '$.kind') = 'runtime'
		        AND json_extract(capability.value, '$.name') = e.required_runtime
		        AND json_extract(capability.value, '$.status') = 'ready'
		      UNION ALL
		      SELECT 1
		      FROM workers legacy_worker
		      WHERE legacy_worker.id = ?
		        AND json_array_length(legacy_worker.capabilities_json) = 0
		        AND legacy_worker.runtime = e.required_runtime
		  )
		  AND e.state = 'queued'
		  AND (
		      SELECT COUNT(*)
		      FROM sessions sibling
		      WHERE sibling.run_id = session.run_id
		        AND sibling.state IN ('preparing', 'running')
		  ) < json_extract(run.task_snapshot, '$.concurrency_limit')
		  AND wr.advertised = 1
		  AND (repository.centrally_managed = 0 OR repository.enabled = 1)
		  AND wr.retained_count + (
		      SELECT COUNT(*)
		      FROM attempts active_attempt
		      JOIN executions active_execution ON active_execution.id = active_attempt.execution_id
		      JOIN sessions active_session ON active_session.id = active_execution.session_id
		      WHERE active_attempt.worker_id = e.assigned_worker_id
		        AND active_session.repository_id = session.repository_id
		        AND active_attempt.state IN ('preparing', 'running')
		  ) + (
		      SELECT COUNT(*)
		      FROM attempts terminal_attempt
		      JOIN executions terminal_execution ON terminal_execution.id = terminal_attempt.execution_id
		      JOIN sessions terminal_session ON terminal_session.id = terminal_execution.session_id
		      WHERE terminal_attempt.worker_id = e.assigned_worker_id
		        AND terminal_session.repository_id = session.repository_id
		        AND terminal_attempt.state IN ('succeeded', 'failed', 'cancelled', 'lost')
		        AND terminal_attempt.capacity_acknowledged = 0
		  ) < ?
		ORDER BY e.created_at, e.id
		LIMIT 1
	`, workerID, workerID, workerID, protocol.MaxRetainedPerRepo).Scan(&executionID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := insertEmptyClaim(ctx, tx, workerID, input.RequestID, digest, nowMillis); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, unavailable(err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, unavailable(err)
	}

	attemptID, err := newID()
	if err != nil {
		return nil, unavailable(err)
	}
	var attemptNumber int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(attempt_number), 0) + 1 FROM attempts WHERE execution_id = ?
	`, executionID).Scan(&attemptNumber); err != nil {
		return nil, unavailable(err)
	}
	expiry := now.Add(protocol.LeaseDuration).UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO attempts(id, execution_id, worker_id, attempt_number, state, lease_digest, lease_expires_at, created_at)
		VALUES (?, ?, ?, ?, 'preparing', ?, ?, ?)
	`, attemptID, executionID, workerID, attemptNumber, digest, expiry, nowMillis); err != nil {
		return nil, unavailable(err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE executions SET state = 'preparing', cancellation_requested = 0, updated_at = ?
		WHERE id = ? AND state = 'queued'
	`, nowMillis, executionID)
	if err != nil {
		return nil, unavailable(err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return nil, conflict("claim_conflict", "execution is no longer queued")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET state = 'preparing', assigned_worker_id = ?, blocked_reason = NULL,
		       waiting_reason = '', execution_owner = 'worker_attempt'
		WHERE id = (SELECT session_id FROM executions WHERE id = ?) AND state = 'queued'
	`, workerID, executionID); err != nil {
		return nil, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO claim_requests(worker_id, request_id, lease_digest, attempt_id, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, workerID, input.RequestID, digest, attemptID, nowMillis); err != nil {
		return nil, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, unavailable(err)
	}
	claim, err := s.claimDetail(ctx, attemptID)
	return &claim, err
}

func insertEmptyClaim(ctx context.Context, tx *sql.Tx, workerID, requestID string, digest []byte, now int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO claim_requests(worker_id, request_id, lease_digest, attempt_id, created_at)
		VALUES (?, ?, ?, NULL, ?)
	`, workerID, requestID, digest, now)
	if err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Store) claimDetail(ctx context.Context, attemptID string) (protocol.Claim, error) {
	var claim protocol.Claim
	row := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.execution_id, a.worker_id, a.attempt_number, a.state, a.lease_expires_at,
		       a.supervisor_pid, a.process_identity, a.process_group_id, a.result, a.error,
		       a.started_at, a.completed_at, a.created_at
		FROM attempts a WHERE a.id = ?
	`, attemptID)
	var err error
	claim.Attempt, err = scanAttempt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return claim, ErrNotFound
	}
	if err != nil {
		return claim, unavailable(err)
	}
	row = s.db.QueryRowContext(ctx, `
		SELECT id, session_id, assigned_worker_id, required_runtime, state,
		       cancellation_requested, created_at, updated_at
		FROM executions WHERE id = ?
	`, claim.Attempt.ExecutionID)
	var executionCreatedAt, executionUpdatedAt int64
	if err = row.Scan(&claim.Execution.ID, &claim.Execution.SessionID,
		&claim.Execution.AssignedWorkerID, &claim.Execution.RequiredRuntime,
		&claim.Execution.State, &claim.Execution.CancellationRequested,
		&executionCreatedAt, &executionUpdatedAt); err != nil {
		return claim, unavailable(err)
	}
	claim.Execution.CreatedAt = fromMillis(executionCreatedAt)
	claim.Execution.UpdatedAt = fromMillis(executionUpdatedAt)
	row = s.db.QueryRowContext(ctx, `
		SELECT session.id, session.run_id, json_extract(run.task_snapshot, '$.name'),
		       session.resolved_prompt, session.repository_id, session.timeout_seconds,
		       e.assigned_worker_id, e.required_runtime, e.state, session.admitted_at,
		       run.outcome_contract, session.target_position, session.target_key,
		       session.target_kind, session.repository_identity, session.source_kind,
		       session.source_key, session.source_reference, session.context_snapshot,
		       session.publish_branch
		FROM sessions session
		JOIN runs run ON run.id = session.run_id
		JOIN executions e ON e.session_id = session.id
		WHERE session.id = ?
	`, claim.Execution.SessionID)
	var admittedAt int64
	if err = row.Scan(&claim.Session.ID, &claim.Session.RunID, &claim.Session.TaskName,
		&claim.Session.Prompt, &claim.Session.RepositoryID, &claim.Session.TimeoutSeconds,
		&claim.Session.WorkerID, &claim.Session.RequiredRuntime, &claim.Session.State,
		&admittedAt, &claim.Session.OutcomeContract, &claim.Session.Target.Position,
		&claim.Session.Target.TargetKey, &claim.Session.Target.TargetKind,
		&claim.Session.Target.RepositoryIdentity, &claim.Session.Target.SourceKind,
		&claim.Session.Target.SourceKey, &claim.Session.Target.SourceReference,
		&claim.Session.Target.ContextSnapshot, &claim.Session.Target.PublishBranch); err != nil {
		return claim, unavailable(err)
	}
	claim.Session.AdmittedAt = fromMillis(admittedAt)
	claim.Session.Target.ID = claim.Session.ID
	claim.Session.Target.RepositoryID = claim.Session.RepositoryID
	err = s.db.QueryRowContext(ctx, `
		SELECT r.id, wr.display_key,
		       COALESCE(
		           NULLIF(session.repository_identity, ''),
		           NULLIF(wr.worker_remote_identity, ''),
		           r.remote_identity
		       ),
		       wr.retained_count
		FROM repositories r
		JOIN worker_repositories wr ON wr.repository_id = r.id
		JOIN sessions session ON session.id = ?
		WHERE r.id = ? AND wr.worker_id = ?
	`, claim.Session.ID, claim.Session.RepositoryID, claim.Attempt.WorkerID).Scan(
		&claim.Repository.ID, &claim.Repository.Key, &claim.Repository.RemoteIdentity, &claim.Repository.RetainedCount)
	if err != nil {
		return claim, unavailable(err)
	}
	return claim, nil
}

type leaseState struct {
	attemptState    string
	executionID     string
	executionState  string
	outcomeContract protocol.OutcomeContract
	digest          []byte
	expiry          int64
	cancel          bool
}

func loadLease(ctx context.Context, tx *sql.Tx, attemptID string) (leaseState, error) {
	var value leaseState
	var cancel int
	err := tx.QueryRowContext(ctx, `
		SELECT a.state, a.execution_id, e.state, run.outcome_contract,
		       a.lease_digest, a.lease_expires_at, e.cancellation_requested
		FROM attempts a
		JOIN executions e ON e.id = a.execution_id
		JOIN sessions session ON session.id = e.session_id
		JOIN runs run ON run.id = session.run_id
		WHERE a.id = ?
	`, attemptID).Scan(&value.attemptState, &value.executionID, &value.executionState,
		&value.outcomeContract, &value.digest, &value.expiry, &cancel)
	if errors.Is(err, sql.ErrNoRows) {
		return value, ErrNotFound
	}
	if err != nil {
		return value, unavailable(err)
	}
	value.cancel = cancel != 0
	return value, nil
}

func verifyActiveLease(value leaseState, token string, now int64) error {
	if err := validateToken(token); err != nil {
		return err
	}
	if !equalDigest(value.digest, digestToken(token)) || !isActive(value.attemptState) || value.expiry <= now {
		return conflict("lease_not_owner", "the lease token does not own an active attempt")
	}
	return nil
}

func isActive(state string) bool { return state == "preparing" || state == "running" }
func isTerminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled" || state == "lost"
}

func (s *Store) StartAttempt(ctx context.Context, attemptID string, input protocol.StartAttemptRequest) (protocol.Attempt, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Attempt{}, unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return protocol.Attempt{}, err
	}
	if err := verifyActiveLease(lease, input.LeaseToken, now); err != nil {
		return protocol.Attempt{}, err
	}
	if lease.attemptState == "preparing" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE attempts SET state = 'running', supervisor_pid = ?, process_identity = ?,
			       process_group_id = ?, started_at = ?
			WHERE id = ? AND state = 'preparing'
		`, input.SupervisorPID, nullString(input.ProcessIdentity), input.ProcessGroupID, now, attemptID); err != nil {
			return protocol.Attempt{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE executions SET state = 'running', updated_at = ? WHERE id = ? AND state = 'preparing'
		`, now, lease.executionID); err != nil {
			return protocol.Attempt{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sessions SET state = 'running', started_at = COALESCE(started_at, ?),
			       execution_owner = 'worker_attempt'
			WHERE id = (SELECT session_id FROM executions WHERE id = ?) AND state = 'preparing'
		`, now, lease.executionID); err != nil {
			return protocol.Attempt{}, unavailable(err)
		}
	} else if lease.attemptState != "running" {
		return protocol.Attempt{}, conflict("invalid_transition", "attempt cannot be started from its current state")
	}
	if err := tx.Commit(); err != nil {
		return protocol.Attempt{}, unavailable(err)
	}
	return s.Attempt(ctx, attemptID)
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) Heartbeat(ctx context.Context, attemptID, token string) (protocol.HeartbeatResponse, error) {
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.HeartbeatResponse{}, unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	if err := verifyActiveLease(lease, token, now.UnixMilli()); err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	expiry := now.Add(protocol.LeaseDuration)
	if _, err := tx.ExecContext(ctx, `UPDATE attempts SET lease_expires_at = ? WHERE id = ?`, expiry.UnixMilli(), attemptID); err != nil {
		return protocol.HeartbeatResponse{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.HeartbeatResponse{}, unavailable(err)
	}
	return protocol.HeartbeatResponse{LeaseExpiresAt: expiry.UTC(), CancellationRequested: lease.cancel}, nil
}

func (s *Store) AppendEvents(ctx context.Context, attemptID string, input protocol.EventBatchRequest) error {
	if len(input.Events) == 0 || len(input.Events) > protocol.MaxEventsPerBatch {
		return invalid("invalid_event_batch", "an event batch must contain 1 through 100 events")
	}
	encoded, _ := json.Marshal(input)
	if len(encoded) > protocol.MaxEventBatchBytes {
		return invalid("event_batch_too_large", "event batch exceeds 256 KiB")
	}
	var previous int64 = -1
	for _, event := range input.Events {
		if event.Sequence < 0 || event.Sequence <= previous || strings.TrimSpace(event.Kind) == "" || len(event.Kind) > 100 {
			return invalid("invalid_event", "event sequences must be non-negative and strictly increasing, with a kind")
		}
		if len(event.Payload) > protocol.MaxEventBytes {
			return invalid("event_too_large", "one event exceeds 64 KiB")
		}
		if !json.Valid(event.Payload) {
			return invalid("invalid_event", "event payload must be valid JSON")
		}
		previous = event.Sequence
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	if err := verifyActiveLease(lease, input.LeaseToken, now); err != nil {
		return err
	}
	var storedBytes int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(payload_bytes), 0) FROM attempt_events WHERE attempt_id = ?`, attemptID).Scan(&storedBytes); err != nil {
		return unavailable(err)
	}
	type pendingEvent struct {
		event protocol.AttemptEvent
	}
	var pending []pendingEvent
	var maxSequence int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), -1) FROM attempt_events WHERE attempt_id = ?
	`, attemptID).Scan(&maxSequence); err != nil {
		return unavailable(err)
	}
	for _, event := range input.Events {
		var kind string
		var payload []byte
		err := tx.QueryRowContext(ctx, `
			SELECT kind, payload FROM attempt_events WHERE attempt_id = ? AND sequence = ?
		`, attemptID, event.Sequence).Scan(&kind, &payload)
		if err == nil {
			if kind != event.Kind || !jsonEqual(payload, event.Payload) {
				return conflict("event_conflict", "an event sequence was replayed with different content")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return unavailable(err)
		}
		if event.Sequence <= maxSequence {
			return conflict("event_out_of_order", "a new event sequence must follow stored events")
		}
		storedBytes += len(event.Payload)
		pending = append(pending, pendingEvent{event: event})
		maxSequence = event.Sequence
	}
	if storedBytes > protocol.MaxAttemptEventBytes {
		return &ServiceError{Code: "event_budget_exceeded", Message: "attempt event storage exceeds 10 MiB", Status: 413}
	}
	for _, item := range pending {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO attempt_events(attempt_id, sequence, kind, payload, payload_bytes, server_time)
			VALUES (?, ?, ?, ?, ?, ?)
		`, attemptID, item.event.Sequence, item.event.Kind, []byte(item.event.Payload), len(item.event.Payload), now); err != nil {
			return unavailable(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	return nil
}

func jsonEqual(a, b []byte) bool {
	return bytes.Equal(a, b)
}

func (s *Store) Events(ctx context.Context, attemptID string, after int64, limit int) (protocol.AttemptEventPage, error) {
	if after < -1 {
		return protocol.AttemptEventPage{}, invalid("invalid_after", "after must be an integer of at least -1")
	}
	if limit < 1 || limit > protocol.MaxEventPageSize {
		return protocol.AttemptEventPage{}, invalid("invalid_limit", "limit must be between 1 and 500")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE id = ?`, attemptID).Scan(&exists); err != nil {
		return protocol.AttemptEventPage{}, unavailable(err)
	}
	if exists == 0 {
		return protocol.AttemptEventPage{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sequence, kind, payload, server_time
		FROM attempt_events WHERE attempt_id = ? AND sequence > ?
		ORDER BY sequence ASC
		LIMIT ?
	`, attemptID, after, limit+1)
	if err != nil {
		return protocol.AttemptEventPage{}, unavailable(err)
	}
	defer rows.Close()
	events := make([]protocol.AttemptEvent, 0, limit+1)
	for rows.Next() {
		var event protocol.AttemptEvent
		var payload []byte
		var serverTime int64
		if err := rows.Scan(&event.Sequence, &event.Kind, &payload, &serverTime); err != nil {
			return protocol.AttemptEventPage{}, unavailable(err)
		}
		event.Payload = append(event.Payload[:0], payload...)
		event.ServerTime = fromMillis(serverTime)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return protocol.AttemptEventPage{}, unavailable(err)
	}
	page := protocol.AttemptEventPage{Events: events, NextAfter: after}
	if len(events) > limit {
		page.Events = events[:limit]
		page.HasMore = true
	}
	if len(page.Events) > 0 {
		page.NextAfter = page.Events[len(page.Events)-1].Sequence
	}
	return page, nil
}

func (s *Store) CompleteAttempt(ctx context.Context, attemptID string, input protocol.CompleteAttemptRequest) (protocol.Attempt, error) {
	if input.State != "succeeded" && input.State != "failed" && input.State != "cancelled" {
		return protocol.Attempt{}, invalid("invalid_terminal_state", "state must be succeeded, failed, or cancelled")
	}
	if len([]byte(input.Result)) > protocol.MaxResultBytes || len([]byte(input.Error)) > protocol.MaxErrorBytes {
		return protocol.Attempt{}, &ServiceError{Code: "result_too_large", Message: "result or error exceeds its storage limit", Status: 413}
	}
	if err := validateToken(input.LeaseToken); err != nil {
		return protocol.Attempt{}, err
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Attempt{}, unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return protocol.Attempt{}, err
	}
	if isTerminal(lease.attemptState) {
		if lease.attemptState == "lost" || !equalDigest(lease.digest, digestToken(input.LeaseToken)) {
			return protocol.Attempt{}, conflict("lease_not_owner", "the lease token does not own this terminal attempt")
		}
		if err := tx.Commit(); err != nil {
			return protocol.Attempt{}, unavailable(err)
		}
		return s.Attempt(ctx, attemptID)
	}
	if err := verifyActiveLease(lease, input.LeaseToken, now); err != nil {
		return protocol.Attempt{}, err
	}
	terminalMessage := ""
	if lease.cancel {
		input.State, input.Result, input.Error = "cancelled", "", ""
		terminalMessage = "Cancelled by operator."
	} else if lease.outcomeContract == protocol.OutcomeAgentUpdate && input.State == "succeeded" {
		const missingOutcome = "Agent exited without reporting an outcome."
		input.State, input.Result, input.Error = "failed", "", missingOutcome
		terminalMessage = missingOutcome
	}
	if input.State == "succeeded" && lease.attemptState != "running" {
		return protocol.Attempt{}, conflict("invalid_transition", "only a running attempt can succeed")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE attempts SET state = ?, result = ?, error = ?, completed_at = ?
		WHERE id = ? AND state IN ('preparing', 'running')
	`, input.State, nullString(input.Result), nullString(input.Error), now, attemptID); err != nil {
		return protocol.Attempt{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE executions SET state = ?, updated_at = ?
		WHERE id = ? AND state IN ('preparing', 'running')
	`, input.State, now, lease.executionID); err != nil {
		return protocol.Attempt{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET state = ?, terminal_at = ?, result = ?, failure_reason = ?,
		       terminal_message = ?,
		       execution_owner = 'none'
		WHERE id = (SELECT session_id FROM executions WHERE id = ?)
		  AND state IN ('preparing', 'running')
	`, input.State, now, nullString(input.Result), nullString(input.Error), terminalMessage, lease.executionID); err != nil {
		return protocol.Attempt{}, unavailable(err)
	}
	if err := updateRunLifecycle(ctx, tx, lease.executionID, now); err != nil {
		return protocol.Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Attempt{}, unavailable(err)
	}
	return s.Attempt(ctx, attemptID)
}

func (s *Store) Attempt(ctx context.Context, id string) (protocol.Attempt, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, execution_id, worker_id, attempt_number, state, lease_expires_at,
		       supervisor_pid, process_identity, process_group_id, result, error,
		       started_at, completed_at, created_at
		FROM attempts WHERE id = ?
	`, id)
	value, err := scanAttempt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return value, ErrNotFound
	}
	if err != nil {
		return value, unavailable(err)
	}
	return value, nil
}

type ExpiredLease struct {
	AttemptID   string
	ExecutionID string
}

func (s *Store) SweepExpired(ctx context.Context) ([]ExpiredLease, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, unavailable(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM claim_requests
		WHERE attempt_id IS NULL AND created_at < ?
	`, now-protocol.EmptyClaimTTL.Milliseconds()); err != nil {
		return nil, unavailable(err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT attempt.id, attempt.execution_id
		FROM attempts attempt
		JOIN executions execution ON execution.id = attempt.execution_id
		JOIN sessions session ON session.id = execution.session_id
		WHERE attempt.state IN ('preparing', 'running')
		  AND attempt.lease_expires_at <= ?
		  AND session.execution_backend = ?
	`, now, protocol.BackendPersistent)
	if err != nil {
		return nil, unavailable(err)
	}
	var values []ExpiredLease
	for rows.Next() {
		var value ExpiredLease
		if err := rows.Scan(&value.AttemptID, &value.ExecutionID); err != nil {
			rows.Close()
			return nil, unavailable(err)
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return nil, unavailable(err)
	}
	for _, value := range values {
		result, err := tx.ExecContext(ctx, `
			UPDATE attempts SET state = 'lost', error = 'lease expired', completed_at = ?
			WHERE id = ? AND state IN ('preparing', 'running') AND lease_expires_at <= ?
			  AND execution_id IN (
			      SELECT execution.id
			      FROM executions execution
			      JOIN sessions session ON session.id = execution.session_id
			      WHERE session.execution_backend = ?
			  )
		`, now, value.AttemptID, now, protocol.BackendPersistent)
		if err != nil {
			return nil, unavailable(err)
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			if _, err := tx.ExecContext(ctx, `
				UPDATE executions SET state = 'failed', updated_at = ?
				WHERE id = ? AND state IN ('preparing', 'running')
				`, now, value.ExecutionID); err != nil {
				return nil, unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE sessions SET state = 'failed', terminal_at = ?, failure_reason = 'lease expired',
				       terminal_message = 'lease expired', execution_owner = 'none'
				WHERE id = (SELECT session_id FROM executions WHERE id = ?)
				  AND state IN ('preparing', 'running')
			`, now, value.ExecutionID); err != nil {
				return nil, unavailable(err)
			}
			if err := updateRunLifecycle(ctx, tx, value.ExecutionID, now); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, unavailable(err)
	}
	return values, nil
}
