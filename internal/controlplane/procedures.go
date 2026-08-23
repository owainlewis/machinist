package controlplane

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/owainlewis/factory/internal/protocol"
)

const procedureConcurrencyBlockedReason = "Waiting for an available Procedure concurrency slot."

type resolvedProcedureTarget struct {
	repository  protocol.TaskRepository
	predecessor string
}

func (s *Store) AdmitProcedureRun(
	ctx context.Context,
	input protocol.ProcedureRunRequest,
) (protocol.ProcedureRunAdmission, error) {
	canonical, normalized, fingerprint, err := protocol.CanonicalProcedureRunRequest(input)
	if err != nil {
		return protocol.ProcedureRunAdmission{}, invalid("invalid_procedure_run", err.Error())
	}
	if canonical.RequestKey == "" || len([]byte(canonical.RequestKey)) > 200 {
		return protocol.ProcedureRunAdmission{}, invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if strings.HasPrefix(canonical.RequestKey, "schedule:") {
		return protocol.ProcedureRunAdmission{}, invalid("reserved_request_key", "request_key uses a reserved internal prefix")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.ProcedureRunAdmission{}, unavailable(err)
	}
	defer tx.Rollback()

	// Replay intentionally wins before Procedure, repository, execution-profile,
	// duplicate, and rebuild-predecessor reads.
	var existingRunID string
	var existingFingerprint []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, request_digest FROM runs WHERE request_key = ?
	`, canonical.RequestKey).Scan(&existingRunID, &existingFingerprint)
	if err == nil {
		if !bytes.Equal(existingFingerprint, fingerprint) {
			return protocol.ProcedureRunAdmission{}, conflict(
				"request_key_conflict",
				"request_key was already used with different Procedure Run inputs",
			)
		}
		if err := tx.Commit(); err != nil {
			return protocol.ProcedureRunAdmission{}, unavailable(err)
		}
		detail, err := s.Run(ctx, existingRunID)
		if err != nil {
			return protocol.ProcedureRunAdmission{}, err
		}
		return protocol.ProcedureRunAdmission{
			Result: protocol.AdmissionReplayed, RequestKey: canonical.RequestKey, Run: detail,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.ProcedureRunAdmission{}, unavailable(err)
	}

	snapshot, err := loadProcedureSnapshot(ctx, tx, normalized.Procedure)
	if err != nil {
		return protocol.ProcedureRunAdmission{}, err
	}
	if snapshot.ConcurrencyLimit < 1 || snapshot.ConcurrencyLimit > protocol.MaxWorkTargets {
		return protocol.ProcedureRunAdmission{}, conflict(
			"invalid_procedure_concurrency", "the Procedure concurrency limit must be between 1 and 100",
		)
	}
	targets, err := resolveProcedureTargets(ctx, tx, normalized)
	if err != nil {
		return protocol.ProcedureRunAdmission{}, err
	}
	if err := selectProcedurePredecessors(ctx, tx, snapshot.ID, targets, normalized.Rebuild); err != nil {
		return protocol.ProcedureRunAdmission{}, err
	}
	snapshot.Repositories = make([]protocol.TaskRepository, 0, len(targets))
	for _, target := range targets {
		snapshot.Repositories = append(snapshot.Repositories, target.repository)
	}

	execution, profileReady, profileBlockedReason, err := loadExecutionSnapshot(ctx, tx, snapshot, "")
	if err != nil {
		return protocol.ProcedureRunAdmission{}, err
	}
	if snapshot.OutcomeContract == protocol.OutcomeAgentUpdate && execution.Backend != protocol.BackendPersistent {
		return protocol.ProcedureRunAdmission{}, conflict(
			"agent_update_backend_unsupported",
			"agent_update requires the persistent execution backend",
		)
	}
	if execution.Backend != protocol.BackendPersistent && len(snapshot.Pipeline.Stages) > 1 {
		return protocol.ProcedureRunAdmission{}, conflict(
			"pipeline_backend_unsupported",
			"multi-stage Pipelines currently require a persistent Worker",
		)
	}
	if len([]byte(snapshot.Prompt)) > protocol.MaxResolvedPromptBytes {
		return protocol.ProcedureRunAdmission{}, conflict(
			"resolved_prompt_too_large", "the frozen Procedure prompt exceeds 64 KiB",
		)
	}

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return protocol.ProcedureRunAdmission{}, unavailable(err)
	}
	executionJSON, err := json.Marshal(execution)
	if err != nil {
		return protocol.ProcedureRunAdmission{}, unavailable(err)
	}
	runID, err := newID()
	if err != nil {
		return protocol.ProcedureRunAdmission{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runs(
			id, request_key, request_digest, task_id, task_snapshot, source,
			requested_execution_profile_id, execution_snapshot, outcome_contract,
			admitted_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'manual', NULL, ?, ?, ?, ?)
	`, runID, canonical.RequestKey, fingerprint, snapshot.ID, snapshotJSON,
		executionJSON, snapshot.OutcomeContract, now, now); err != nil {
		return protocol.ProcedureRunAdmission{}, unavailable(err)
	}

	frozenTargets := make([]protocol.WorkTarget, 0, len(targets))
	materialized := 0
	for position, target := range targets {
		workID, err := newID()
		if err != nil {
			return protocol.ProcedureRunAdmission{}, unavailable(err)
		}
		frozen := protocol.WorkTarget{
			ID: workID, Position: position, TargetKey: "repository:" + target.repository.ID,
			TargetKind: "repository", RepositoryID: target.repository.ID,
			RepositoryIdentity: target.repository.RemoteIdentity, SourceKind: "repository",
			SourceKey: target.repository.ID, SourceReference: target.repository.RemoteIdentity,
			PublishBranch: workPublishBranch(workID),
		}
		resolvedStages, err := resolveSessionStages(snapshot, snapshot.Prompt, runID, frozen)
		if err != nil {
			return protocol.ProcedureRunAdmission{}, err
		}
		state, blockedReason := "blocked", procedureConcurrencyBlockedReason
		var assigned any
		var selection runRouteCandidate
		if !profileReady {
			blockedReason = profileBlockedReason
		} else if materialized < snapshot.ConcurrencyLimit {
			if execution.Backend == protocol.BackendPersistent {
				selection, err = s.selectSessionRoute(
					ctx, tx, target.repository.ID, target.repository.RemoteIdentity, now, "", snapshot.Runtime,
				)
				blockedReason = "Waiting for a healthy compatible Worker with repository access."
				if err == nil {
					state, blockedReason, assigned = "queued", "", selection.workerID
					materialized++
				} else if !serviceErrorCode(err, "no_eligible_worker") {
					return protocol.ProcedureRunAdmission{}, err
				}
			} else {
				state, blockedReason, assigned = "queued", "", syntheticWorkerID(execution.ProfileID)
				selection.workerID = assigned.(string)
				materialized++
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sessions(
				id, run_id, repository_id, repository_identity, resolved_prompt, required_runtime,
				timeout_seconds, state, blocked_reason, assigned_worker_id, admitted_at,
				execution_profile_id, execution_profile_version, execution_backend, execution_provider,
				execution_model, resource_class, commit_resolution_policy,
				target_position, target_key, target_kind, source_kind, source_key,
				source_reference, context_snapshot, publish_branch, predecessor_work_id,
				execution_owner, waiting_reason
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, workID, runID, target.repository.ID, target.repository.RemoteIdentity, snapshot.Prompt, snapshot.Runtime,
			execution.TimeoutSeconds, state, nullableString(blockedReason), assigned, now,
			execution.ProfileID, execution.ProfileVersion, execution.Backend, execution.Provider,
			execution.Model, execution.ResourceClass, execution.CommitResolutionPolicy,
			frozen.Position, frozen.TargetKey, frozen.TargetKind, frozen.SourceKind, frozen.SourceKey,
			frozen.SourceReference, frozen.ContextSnapshot, frozen.PublishBranch,
			nullableString(target.predecessor), protocol.ExecutionOwnerNone,
			boundedUTF8Bytes(blockedReason, protocol.MaxWaitingReasonBytes)); err != nil {
			return protocol.ProcedureRunAdmission{}, unavailable(err)
		}
		if err := insertSessionStages(ctx, tx, workID, resolvedStages); err != nil {
			return protocol.ProcedureRunAdmission{}, err
		}
		if state == "queued" {
			executionID, err := newID()
			if err != nil {
				return protocol.ProcedureRunAdmission{}, unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO executions(
					id, session_id, assigned_worker_id, required_runtime, state, created_at, updated_at
				) VALUES (?, ?, ?, ?, 'queued', ?, ?)
			`, executionID, workID, selection.workerID, snapshot.Runtime, now, now); err != nil {
				return protocol.ProcedureRunAdmission{}, unavailable(err)
			}
		}
		frozenTargets = append(frozenTargets, frozen)
	}
	targetsJSON, err := json.Marshal(frozenTargets)
	if err != nil {
		return protocol.ProcedureRunAdmission{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET targets_snapshot = ? WHERE id = ?`, targetsJSON, runID); err != nil {
		return protocol.ProcedureRunAdmission{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.ProcedureRunAdmission{}, unavailable(err)
	}
	detail, err := s.Run(ctx, runID)
	if err != nil {
		return protocol.ProcedureRunAdmission{}, err
	}
	return protocol.ProcedureRunAdmission{
		Result: protocol.AdmissionAdmitted, RequestKey: canonical.RequestKey, Run: detail,
	}, nil
}

func loadProcedureSnapshot(ctx context.Context, tx *sql.Tx, nameKey string) (protocol.TaskSnapshot, error) {
	var snapshot protocol.TaskSnapshot
	var archived, migrationOnly, readOnly int
	err := tx.QueryRowContext(ctx, `
		SELECT id, name, prompt, runtime, COALESCE(execution_profile_id, ''),
		       timeout_seconds, concurrency_limit, generation, outcome_contract,
		       COALESCE(pipeline_id, ?),
		       archived, migration_only, read_only
		FROM tasks WHERE name_key = ?
	`, protocol.DefaultPipelineID, nameKey).Scan(&snapshot.ID, &snapshot.Name, &snapshot.Prompt, &snapshot.Runtime,
		&snapshot.ExecutionProfileID, &snapshot.TimeoutSeconds, &snapshot.ConcurrencyLimit,
		&snapshot.Generation, &snapshot.OutcomeContract, &snapshot.Pipeline.ID,
		&archived, &migrationOnly, &readOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, invalid("procedure_not_found", fmt.Sprintf("Procedure %s does not exist", nameKey))
	}
	if err != nil {
		return snapshot, unavailable(err)
	}
	if migrationOnly != 0 || readOnly != 0 {
		return snapshot, conflict("procedure_read_only", "read-only Procedures cannot start fleet Runs")
	}
	if archived != 0 {
		return snapshot, conflict("procedure_archived", "archived Procedures cannot start Runs")
	}
	pipeline, err := loadPipelineSnapshot(ctx, tx, snapshot.Pipeline.ID)
	if err != nil {
		return snapshot, err
	}
	snapshot.Pipeline = pipeline
	return snapshot, nil
}

func resolveProcedureTargets(
	ctx context.Context,
	tx *sql.Tx,
	input protocol.NormalizedProcedureRunInput,
) ([]*resolvedProcedureTarget, error) {
	var rows *sql.Rows
	var err error
	if input.AllRepositories {
		rows, err = tx.QueryContext(ctx, `
			SELECT id, remote_identity FROM repositories
			WHERE centrally_managed = 1 AND enabled = 1
			ORDER BY lower(remote_identity), id
		`)
		if err != nil {
			return nil, unavailable(err)
		}
		defer rows.Close()
		var targets []*resolvedProcedureTarget
		for rows.Next() {
			var repository protocol.TaskRepository
			if err := rows.Scan(&repository.ID, &repository.RemoteIdentity); err != nil {
				return nil, unavailable(err)
			}
			targets = append(targets, &resolvedProcedureTarget{repository: repository})
			if len(targets) > protocol.MaxWorkTargets {
				return nil, conflict("too_many_procedure_targets", "a Procedure Run is limited to 100 repositories")
			}
		}
		if err := rows.Err(); err != nil {
			return nil, unavailable(err)
		}
		if len(targets) == 0 {
			return nil, conflict("procedure_repositories_empty", "no enabled managed repositories are available")
		}
		return targets, nil
	}
	targets := make([]*resolvedProcedureTarget, 0, len(input.Repositories))
	for _, identity := range input.Repositories {
		var repository protocol.TaskRepository
		err := tx.QueryRowContext(ctx, `
			SELECT id, remote_identity FROM repositories
			WHERE lower(remote_identity) = lower(?) AND centrally_managed = 1 AND enabled = 1
		`, identity).Scan(&repository.ID, &repository.RemoteIdentity)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, conflict(
				"repository_not_managed",
				fmt.Sprintf("repository %s is not enabled in the managed repository catalog", identity),
			)
		}
		if err != nil {
			return nil, unavailable(err)
		}
		targets = append(targets, &resolvedProcedureTarget{repository: repository})
	}
	return targets, nil
}

func selectProcedurePredecessors(
	ctx context.Context,
	tx *sql.Tx,
	procedureID string,
	targets []*resolvedProcedureTarget,
	rebuild bool,
) error {
	if !rebuild {
		return nil
	}
	for _, target := range targets {
		var nonterminalID string
		err := tx.QueryRowContext(ctx, `
			SELECT session.id
			FROM sessions session JOIN runs run ON run.id = session.run_id
			WHERE run.task_id = ? AND session.repository_id = ? AND session.target_kind = 'repository'
			  AND session.state IN ('blocked', 'queued', 'preparing', 'running', 'needs-input')
			ORDER BY session.admitted_at DESC, session.id DESC LIMIT 1
		`, procedureID, target.repository.ID).Scan(&nonterminalID)
		if err == nil {
			return conflict(
				"procedure_rebuild_active",
				fmt.Sprintf("matching Work %s is still nonterminal", nonterminalID),
			)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return unavailable(err)
		}
		var terminalID string
		err = tx.QueryRowContext(ctx, `
			SELECT session.id
			FROM sessions session JOIN runs run ON run.id = session.run_id
			WHERE run.task_id = ? AND session.repository_id = ? AND session.target_kind = 'repository'
			  AND session.state IN ('ready', 'succeeded', 'failed', 'no-change', 'cancelled')
			  AND NOT EXISTS (
				SELECT 1 FROM sessions child WHERE child.predecessor_work_id = session.id
			  )
			ORDER BY session.admitted_at DESC, session.id DESC LIMIT 1
		`, procedureID, target.repository.ID).Scan(&terminalID)
		if errors.Is(err, sql.ErrNoRows) {
			return conflict(
				"rebuild_predecessor_not_found",
				fmt.Sprintf("repository %s has no terminal predecessor for Procedure rebuild", target.repository.RemoteIdentity),
			)
		}
		if err != nil {
			return unavailable(err)
		}
		target.predecessor = terminalID
	}
	return nil
}
