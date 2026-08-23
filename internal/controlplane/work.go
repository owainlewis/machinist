package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	defaultWorkUpdatePageSize = 100
	maxWorkUpdatePageSize     = 200
)

func (s *Store) AppendAgentUpdate(
	ctx context.Context,
	attemptID string,
	input protocol.AttemptUpdateRequest,
) (protocol.WorkUpdate, error) {
	if !validUUID(attemptID) || !validUUID(input.RequestID) {
		return protocol.WorkUpdate{}, invalid("invalid_update_identity", "attempt_id and request_id must be UUIDs")
	}
	if err := validateToken(input.LeaseToken); err != nil {
		return protocol.WorkUpdate{}, err
	}
	if !protocol.SupportedWorkUpdateStatus(input.Status) {
		return protocol.WorkUpdate{}, invalid(
			"invalid_update_status",
			"status must be running, ready, needs-input, failed, or no-change",
		)
	}
	if !utf8.ValidString(input.Message) || strings.TrimSpace(input.Message) == "" {
		return protocol.WorkUpdate{}, invalid("invalid_update_message", "message must be non-empty UTF-8 text")
	}
	messageLimit := protocol.MaxOutcomeBytes
	if input.Status == protocol.WorkUpdateRunning {
		messageLimit = protocol.MaxProgressBytes
	}
	if len([]byte(input.Message)) > messageLimit {
		return protocol.WorkUpdate{}, &ServiceError{
			Code: "update_message_too_large", Message: "update message exceeds its status limit", Status: 413,
		}
	}
	if len([]byte(input.PullRequestURL)) > 2048 || len([]byte(input.PullRequestHeadBranch)) > 255 ||
		len(input.PullRequestHeadSHA) > 64 || len(input.CheckpointSHA) > 64 {
		return protocol.WorkUpdate{}, invalid("invalid_update_evidence", "update evidence exceeds its storage limit")
	}
	if input.Status == protocol.WorkUpdateReady {
		if input.PullRequestURL == "" || (!input.ReplayOnly && (input.PullRequestHeadBranch == "" ||
			!validCommitSHA(input.PullRequestHeadSHA))) || input.CheckpointSHA != "" {
			return protocol.WorkUpdate{}, invalid(
				"invalid_delivery_evidence",
				"ready requires a pull request URL, head branch, and full head SHA",
			)
		}
	} else if input.PullRequestURL != "" || input.PullRequestHeadBranch != "" || input.PullRequestHeadSHA != "" {
		return protocol.WorkUpdate{}, invalid("unexpected_delivery_evidence", "only ready accepts pull request evidence")
	}
	if input.Status == protocol.WorkUpdateNeedsInput {
		if !input.ReplayOnly && !validCommitSHA(input.CheckpointSHA) {
			return protocol.WorkUpdate{}, invalid("invalid_checkpoint", "needs-input requires a full checkpoint SHA")
		}
	} else if input.CheckpointSHA != "" {
		return protocol.WorkUpdate{}, invalid("unexpected_checkpoint", "only needs-input accepts a checkpoint SHA")
	}
	if input.ReplayOnly && (input.PullRequestHeadBranch != "" || input.PullRequestHeadSHA != "" || input.CheckpointSHA != "") {
		return protocol.WorkUpdate{}, invalid("unexpected_replay_evidence", "replay lookup cannot include Worker-derived evidence")
	}

	digest, err := agentUpdateRequestDigest(input)
	if err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	defer tx.Rollback()
	lease, err := loadLease(ctx, tx, attemptID)
	if err != nil {
		return protocol.WorkUpdate{}, err
	}
	if !equalDigest(lease.digest, digestToken(input.LeaseToken)) {
		return protocol.WorkUpdate{}, conflict("lease_not_owner", "the lease token does not own this attempt")
	}
	if stored, storedDigest, found, err := storedAgentUpdateRequest(ctx, tx, attemptID, input.RequestID); err != nil {
		return protocol.WorkUpdate{}, err
	} else if found {
		if !equalDigest(storedDigest, digest) {
			return protocol.WorkUpdate{}, conflict(
				"update_request_conflict",
				"request_id was already used with different update fields",
			)
		}
		if err := tx.Commit(); err != nil {
			return protocol.WorkUpdate{}, unavailable(err)
		}
		return stored, nil
	}
	if input.ReplayOnly {
		return protocol.WorkUpdate{}, &ServiceError{
			Code: "update_request_not_found", Message: "the update request has not been stored", Status: 404,
		}
	}
	if err := verifyActiveLease(lease, input.LeaseToken, now); err != nil {
		return protocol.WorkUpdate{}, err
	}
	if lease.outcomeContract != protocol.OutcomeAgentUpdate {
		return protocol.WorkUpdate{}, conflict(
			"agent_update_not_allowed",
			"this Attempt uses process-exit completion and cannot accept semantic updates",
		)
	}
	if lease.attemptState != "running" || lease.executionState != "running" || lease.cancel {
		return protocol.WorkUpdate{}, conflict("update_not_active", "the Attempt is not active for updates")
	}
	var workID, workState, owner string
	if err := tx.QueryRowContext(ctx, `
		SELECT session.id, session.state, session.execution_owner
		FROM sessions session
		JOIN executions execution ON execution.session_id = session.id
		WHERE execution.id = ?
	`, lease.executionID).Scan(&workID, &workState, &owner); err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	if workState != string(protocol.WorkRunning) || owner != string(protocol.ExecutionOwnerWorkerAttempt) {
		return protocol.WorkUpdate{}, conflict("update_not_active", "the Work is not owned by this active Attempt")
	}

	existingOutcome, hasOutcome, err := attemptOutcome(ctx, tx, attemptID)
	if err != nil {
		return protocol.WorkUpdate{}, err
	}
	if hasOutcome {
		if input.Status == protocol.WorkUpdateRunning {
			return protocol.WorkUpdate{}, conflict("outcome_already_reported", "progress cannot follow an outcome report")
		}
		if !sameAgentUpdate(existingOutcome, input) {
			return protocol.WorkUpdate{}, conflict("outcome_already_reported", "a different outcome was already reported")
		}
		if err := storeAgentUpdateRequest(ctx, tx, attemptID, input.RequestID, digest, existingOutcome.ID, now); err != nil {
			return protocol.WorkUpdate{}, err
		}
		if err := tx.Commit(); err != nil {
			return protocol.WorkUpdate{}, unavailable(err)
		}
		return existingOutcome, nil
	}
	if input.Status == protocol.WorkUpdateRunning {
		var progressCount int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM work_updates WHERE attempt_id = ? AND status = 'running'
		`, attemptID).Scan(&progressCount); err != nil {
			return protocol.WorkUpdate{}, unavailable(err)
		}
		if progressCount >= protocol.MaxProgressPerAttempt {
			return protocol.WorkUpdate{}, &ServiceError{
				Code: "progress_update_limit", Message: "the Attempt already has 199 progress updates; its outcome slot is reserved", Status: 409,
			}
		}
	}
	var sequence int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1 FROM work_updates WHERE work_id = ?
	`, workID).Scan(&sequence); err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	updateID, err := newID()
	if err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO work_updates(
			id, work_id, attempt_id, request_id, sequence, status, message,
			pull_request_url, pull_request_head_branch, pull_request_head_sha,
			checkpoint_sha, actor, accepted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'agent', ?)
	`, updateID, workID, attemptID, input.RequestID, sequence, input.Status, input.Message,
		input.PullRequestURL, input.PullRequestHeadBranch, input.PullRequestHeadSHA,
		input.CheckpointSHA, now); err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	if err := storeAgentUpdateRequest(ctx, tx, attemptID, input.RequestID, digest, updateID, now); err != nil {
		return protocol.WorkUpdate{}, err
	}
	if input.Status == protocol.WorkUpdateRunning {
		if _, err := tx.ExecContext(ctx, `
			UPDATE sessions SET latest_progress = ? WHERE id = ? AND state = 'running'
		`, input.Message, workID); err != nil {
			return protocol.WorkUpdate{}, unavailable(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	return s.workUpdate(ctx, updateID)
}

func validCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func agentUpdateRequestDigest(input protocol.AttemptUpdateRequest) ([]byte, error) {
	// The digest covers only fields supplied through the agent-visible local
	// protocol. Worker-derived delivery and checkpoint evidence can change in
	// the environment, but an exact agent retry must still replay its original
	// durable result.
	body, err := json.Marshal(struct {
		RequestID      string                    `json:"request_id"`
		Status         protocol.WorkUpdateStatus `json:"status"`
		Message        string                    `json:"message"`
		PullRequestURL string                    `json:"pull_request_url,omitempty"`
	}{
		RequestID: input.RequestID, Status: input.Status, Message: input.Message,
		PullRequestURL: input.PullRequestURL,
	})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	return digest[:], nil
}

func storedAgentUpdateRequest(
	ctx context.Context,
	tx *sql.Tx,
	attemptID, requestID string,
) (protocol.WorkUpdate, []byte, bool, error) {
	var update protocol.WorkUpdate
	var acceptedAt int64
	var digest []byte
	err := tx.QueryRowContext(ctx, `
		SELECT stored_update.id, stored_update.work_id, COALESCE(stored_update.attempt_id, ''), stored_update.request_id,
		       stored_update.sequence, stored_update.status, stored_update.message, stored_update.pull_request_url,
		       stored_update.pull_request_head_branch, stored_update.pull_request_head_sha,
		       stored_update.checkpoint_sha, stored_update.actor, stored_update.accepted_at, request.request_digest
		FROM agent_update_requests request
		JOIN work_updates stored_update ON stored_update.id = request.update_id
		WHERE request.attempt_id = ? AND request.request_id = ?
	`, attemptID, requestID).Scan(
		&update.ID, &update.WorkID, &update.AttemptID, &update.RequestID, &update.Sequence,
		&update.Status, &update.Message, &update.PullRequestURL, &update.PullRequestHeadBranch,
		&update.PullRequestHeadSHA, &update.CheckpointSHA, &update.Actor, &acceptedAt, &digest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkUpdate{}, nil, false, nil
	}
	if err != nil {
		return protocol.WorkUpdate{}, nil, false, unavailable(err)
	}
	update.AcceptedAt = fromMillis(acceptedAt)
	return update, digest, true, nil
}

func attemptOutcome(ctx context.Context, tx *sql.Tx, attemptID string) (protocol.WorkUpdate, bool, error) {
	var update protocol.WorkUpdate
	var acceptedAt int64
	err := tx.QueryRowContext(ctx, `
		SELECT id, work_id, COALESCE(attempt_id, ''), request_id, sequence, status, message,
		       pull_request_url, pull_request_head_branch, pull_request_head_sha,
		       checkpoint_sha, actor, accepted_at
		FROM work_updates WHERE attempt_id = ? AND status != 'running'
	`, attemptID).Scan(&update.ID, &update.WorkID, &update.AttemptID, &update.RequestID,
		&update.Sequence, &update.Status, &update.Message, &update.PullRequestURL,
		&update.PullRequestHeadBranch, &update.PullRequestHeadSHA, &update.CheckpointSHA,
		&update.Actor, &acceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.WorkUpdate{}, false, nil
	}
	if err != nil {
		return protocol.WorkUpdate{}, false, unavailable(err)
	}
	update.AcceptedAt = fromMillis(acceptedAt)
	return update, true, nil
}

func sameAgentUpdate(update protocol.WorkUpdate, input protocol.AttemptUpdateRequest) bool {
	return update.Status == input.Status && update.Message == input.Message &&
		update.PullRequestURL == input.PullRequestURL &&
		update.PullRequestHeadBranch == input.PullRequestHeadBranch &&
		update.PullRequestHeadSHA == input.PullRequestHeadSHA &&
		update.CheckpointSHA == input.CheckpointSHA
}

func storeAgentUpdateRequest(
	ctx context.Context,
	tx *sql.Tx,
	attemptID, requestID string,
	digest []byte,
	updateID string,
	now int64,
) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_update_requests(attempt_id, request_id, request_digest, update_id, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, attemptID, requestID, digest, updateID, now); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s *Store) workUpdate(ctx context.Context, id string) (protocol.WorkUpdate, error) {
	var update protocol.WorkUpdate
	var acceptedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, work_id, COALESCE(attempt_id, ''), request_id, sequence, status, message,
		       pull_request_url, pull_request_head_branch, pull_request_head_sha,
		       checkpoint_sha, actor, accepted_at
		FROM work_updates WHERE id = ?
	`, id).Scan(&update.ID, &update.WorkID, &update.AttemptID, &update.RequestID,
		&update.Sequence, &update.Status, &update.Message, &update.PullRequestURL,
		&update.PullRequestHeadBranch, &update.PullRequestHeadSHA, &update.CheckpointSHA,
		&update.Actor, &acceptedAt)
	if err != nil {
		return protocol.WorkUpdate{}, unavailable(err)
	}
	update.AcceptedAt = fromMillis(acceptedAt)
	return update, nil
}

// Work returns the product-facing Work record backed by the existing Session
// row. Keeping the adapter here lets existing Run and Session clients remain
// compatible while later operator surfaces adopt Work directly.
func (s *Store) Work(ctx context.Context, id string) (protocol.Work, error) {
	var runID string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM sessions WHERE id = ?`, id).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Work{}, ErrNotFound
	}
	if err != nil {
		return protocol.Work{}, unavailable(err)
	}
	detail, err := s.Run(ctx, runID)
	if err != nil {
		return protocol.Work{}, err
	}
	for _, work := range detail.Sessions {
		if work.ID == id {
			return work, nil
		}
	}
	return protocol.Work{}, ErrNotFound
}

func (s *Store) WorkUpdates(
	ctx context.Context,
	workID string,
	limit int,
	after int,
) (protocol.WorkUpdatePage, error) {
	if limit == 0 {
		limit = defaultWorkUpdatePageSize
	}
	if limit < 1 || limit > maxWorkUpdatePageSize {
		return protocol.WorkUpdatePage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	if after < 0 {
		return protocol.WorkUpdatePage{}, invalid("invalid_cursor", "after must not be negative")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, workID).Scan(&exists); err != nil {
		return protocol.WorkUpdatePage{}, unavailable(err)
	}
	if exists == 0 {
		return protocol.WorkUpdatePage{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, work_id, COALESCE(attempt_id, ''), request_id, sequence, status, message,
		       pull_request_url, pull_request_head_branch, pull_request_head_sha,
		       checkpoint_sha, actor, accepted_at
		FROM work_updates
		WHERE work_id = ? AND sequence > ?
		ORDER BY sequence
		LIMIT ?
	`, workID, after, limit+1)
	if err != nil {
		return protocol.WorkUpdatePage{}, unavailable(err)
	}
	defer rows.Close()
	updates := make([]protocol.WorkUpdate, 0, limit+1)
	for rows.Next() {
		var update protocol.WorkUpdate
		var acceptedAt int64
		if err := rows.Scan(&update.ID, &update.WorkID, &update.AttemptID, &update.RequestID,
			&update.Sequence, &update.Status, &update.Message, &update.PullRequestURL,
			&update.PullRequestHeadBranch, &update.PullRequestHeadSHA, &update.CheckpointSHA,
			&update.Actor, &acceptedAt); err != nil {
			return protocol.WorkUpdatePage{}, unavailable(err)
		}
		update.AcceptedAt = fromMillis(acceptedAt)
		updates = append(updates, update)
	}
	if err := rows.Err(); err != nil {
		return protocol.WorkUpdatePage{}, unavailable(err)
	}
	page := protocol.WorkUpdatePage{Updates: updates, NextAfter: after}
	if len(page.Updates) > limit {
		page.Updates = page.Updates[:limit]
		page.HasMore = true
	}
	if len(page.Updates) != 0 {
		page.NextAfter = page.Updates[len(page.Updates)-1].Sequence
	}
	return page, nil
}

func validateWorkRetryGuards(
	ctx context.Context,
	tx *sql.Tx,
	workID, repositoryID, targetKind, sourceKind, sourceKey string,
) error {
	var replacementCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions WHERE predecessor_work_id = ?
	`, workID).Scan(&replacementCount); err != nil {
		return unavailable(err)
	}
	if replacementCount != 0 {
		return conflict("work_replaced", "replaced Work cannot be retried")
	}
	var matchingNonterminal int
	var err error
	if targetKind == "work_item" {
		err = tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM sessions
			WHERE id != ? AND repository_id = ? AND source_kind = ? AND source_key = ?
			  AND state IN ('blocked', 'queued', 'preparing', 'running', 'needs-input')
		`, workID, repositoryID, sourceKind, sourceKey).Scan(&matchingNonterminal)
	} else {
		err = tx.QueryRowContext(ctx, `
			WITH RECURSIVE lineage(work_id, root_id) AS (
				SELECT id, id FROM sessions WHERE predecessor_work_id IS NULL
				UNION ALL
				SELECT child.id, lineage.root_id
				FROM sessions child
				JOIN lineage ON child.predecessor_work_id = lineage.work_id
			)
			SELECT COUNT(*)
			FROM sessions candidate
			JOIN lineage candidate_lineage ON candidate_lineage.work_id = candidate.id
			JOIN lineage retried_lineage ON retried_lineage.work_id = ?
			WHERE candidate.id != ?
			  AND candidate.repository_id = ?
			  AND candidate_lineage.root_id = retried_lineage.root_id
			  AND candidate.state IN ('blocked', 'queued', 'preparing', 'running', 'needs-input')
		`, workID, workID, repositoryID).Scan(&matchingNonterminal)
	}
	if err != nil {
		return unavailable(err)
	}
	if matchingNonterminal != 0 {
		return conflict("matching_work_active", "matching nonterminal Work already exists")
	}
	return nil
}
