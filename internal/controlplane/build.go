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

type resolvedBuildTarget struct {
	reference   protocol.NormalizedBuildReference
	repository  protocol.TaskRepository
	targetKey   string
	predecessor string
}

func (s *Store) AdmitBuild(ctx context.Context, input protocol.BuildRequest) (protocol.BuildAdmission, error) {
	canonical, normalized, fingerprint, err := protocol.CanonicalBuildRequest(input)
	if err != nil {
		return protocol.BuildAdmission{}, invalid("invalid_build", err.Error())
	}
	if canonical.RequestKey == "" || len([]byte(canonical.RequestKey)) > 200 {
		return protocol.BuildAdmission{}, invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if strings.HasPrefix(canonical.RequestKey, "schedule:") {
		return protocol.BuildAdmission{}, invalid("reserved_request_key", "request_key uses a reserved internal prefix")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.BuildAdmission{}, unavailable(err)
	}
	defer tx.Rollback()

	// Request-key replay intentionally wins before repository, Procedure,
	// runtime-default, duplicate, and rebuild-predecessor reads.
	var existingRunID string
	var existingFingerprint []byte
	err = tx.QueryRowContext(ctx, `
		SELECT id, request_digest FROM runs WHERE request_key = ?
	`, canonical.RequestKey).Scan(&existingRunID, &existingFingerprint)
	if err == nil {
		if !bytes.Equal(existingFingerprint, fingerprint) {
			return protocol.BuildAdmission{}, conflict(
				"request_key_conflict",
				"request_key was already used with different Build inputs",
			)
		}
		if err := tx.Commit(); err != nil {
			return protocol.BuildAdmission{}, unavailable(err)
		}
		detail, err := s.Run(ctx, existingRunID)
		if err != nil {
			return protocol.BuildAdmission{}, err
		}
		return protocol.BuildAdmission{
			Result: protocol.AdmissionReplayed, RequestKey: canonical.RequestKey, Run: detail,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.BuildAdmission{}, unavailable(err)
	}

	runtime := normalized.Runtime
	if !normalized.RuntimeSpecified {
		runtime = s.defaultBuildRuntime
	}
	if !protocol.SupportedRuntime(runtime) {
		return protocol.BuildAdmission{}, invalid(
			"invalid_runtime",
			fmt.Sprintf("runtime must be one of %s", strings.Join(protocol.SupportedRuntimes(), ", ")),
		)
	}

	targets, err := resolveBuildTargets(ctx, tx, normalized)
	if err != nil {
		return protocol.BuildAdmission{}, err
	}
	if err := selectBuildPredecessors(ctx, tx, targets, normalized.Rebuild); err != nil {
		return protocol.BuildAdmission{}, err
	}

	snapshot := protocol.TaskSnapshot{
		ID:                 protocol.StandardBuildProcedureID,
		Name:               protocol.StandardBuildProcedureName,
		Prompt:             protocol.StandardBuildProcedurePrompt,
		Runtime:            runtime,
		TimeoutSeconds:     protocol.StandardBuildTimeoutSeconds,
		ConcurrencyLimit:   protocol.StandardBuildConcurrencyLimit,
		Generation:         protocol.StandardBuildProcedureGeneration,
		OutcomeContract:    protocol.OutcomeAgentUpdate,
		ExecutionProfileID: protocol.PersistentAutoProfileID,
	}
	pipeline, err := loadPipelineSnapshot(ctx, tx, protocol.DefaultPipelineID)
	if err != nil {
		return protocol.BuildAdmission{}, err
	}
	snapshot.Pipeline = pipeline
	seenRepositories := make(map[string]bool, len(targets))
	for _, target := range targets {
		if !seenRepositories[target.repository.ID] {
			snapshot.Repositories = append(snapshot.Repositories, target.repository)
			seenRepositories[target.repository.ID] = true
		}
	}
	execution := protocol.ExecutionSnapshot{
		ProfileID: protocol.PersistentAutoProfileID, ProfileVersion: 1,
		Backend: protocol.BackendPersistent, Runtime: runtime,
		Provider: "worker", Model: "worker-default",
		TimeoutSeconds: protocol.StandardBuildTimeoutSeconds,
		ResourceClass:  "worker", CommitResolutionPolicy: protocol.CommitResolvePerAttempt,
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return protocol.BuildAdmission{}, unavailable(err)
	}
	executionJSON, err := json.Marshal(execution)
	if err != nil {
		return protocol.BuildAdmission{}, unavailable(err)
	}
	runID, err := newID()
	if err != nil {
		return protocol.BuildAdmission{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runs(
			id, request_key, request_digest, task_id, task_snapshot, source,
			requested_execution_profile_id, execution_snapshot, outcome_contract,
			admitted_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'manual', NULL, ?, ?, ?, ?)
	`, runID, canonical.RequestKey, fingerprint, protocol.StandardBuildProcedureID,
		snapshotJSON, executionJSON, protocol.OutcomeAgentUpdate, now, now); err != nil {
		return protocol.BuildAdmission{}, unavailable(err)
	}

	frozenTargets := make([]protocol.WorkTarget, 0, len(targets))
	materialized := 0
	for position, target := range targets {
		workID, err := newID()
		if err != nil {
			return protocol.BuildAdmission{}, unavailable(err)
		}
		frozen := protocol.WorkTarget{
			ID: workID, Position: position, TargetKey: target.targetKey,
			TargetKind: "work_item", RepositoryID: target.repository.ID,
			RepositoryIdentity: target.repository.RemoteIdentity,
			SourceKind:         target.reference.SourceKind, SourceKey: target.reference.SourceKey,
			SourceReference: target.reference.Reference,
			PublishBranch:   workPublishBranch(workID),
		}
		resolvedPrompt := resolveStandardBuildPrompt(frozen)
		resolvedStages, err := resolveSessionStages(snapshot, resolvedPrompt, runID, frozen)
		if err != nil {
			return protocol.BuildAdmission{}, err
		}
		state, blockedReason := "blocked", taskConcurrencyBlockedReason
		var assigned any
		var selection runRouteCandidate
		if materialized < snapshot.ConcurrencyLimit {
			selection, err = s.selectSessionRoute(
				ctx, tx, target.repository.ID, target.repository.RemoteIdentity, now, "", runtime,
			)
			blockedReason = "Waiting for a healthy compatible Worker with repository access."
			if err == nil {
				state, blockedReason, assigned = "queued", "", selection.workerID
				materialized++
			} else if !serviceErrorCode(err, "no_eligible_worker") {
				return protocol.BuildAdmission{}, err
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
		`, workID, runID, target.repository.ID, target.repository.RemoteIdentity, resolvedPrompt, runtime,
			execution.TimeoutSeconds, state, nullableString(blockedReason), assigned, now,
			execution.ProfileID, execution.ProfileVersion, execution.Backend, execution.Provider,
			execution.Model, execution.ResourceClass, execution.CommitResolutionPolicy,
			frozen.Position, frozen.TargetKey, frozen.TargetKind, frozen.SourceKind, frozen.SourceKey,
			frozen.SourceReference, frozen.ContextSnapshot, frozen.PublishBranch,
			nullableString(target.predecessor), protocol.ExecutionOwnerNone,
			boundedUTF8Bytes(blockedReason, protocol.MaxWaitingReasonBytes)); err != nil {
			return protocol.BuildAdmission{}, unavailable(err)
		}
		if err := insertSessionStages(ctx, tx, workID, resolvedStages); err != nil {
			return protocol.BuildAdmission{}, err
		}
		if state == "queued" {
			executionID, err := newID()
			if err != nil {
				return protocol.BuildAdmission{}, unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO executions(
					id, session_id, assigned_worker_id, required_runtime, state, created_at, updated_at
				) VALUES (?, ?, ?, ?, 'queued', ?, ?)
			`, executionID, workID, selection.workerID, runtime, now, now); err != nil {
				return protocol.BuildAdmission{}, unavailable(err)
			}
		}
		frozenTargets = append(frozenTargets, frozen)
	}
	targetsJSON, err := json.Marshal(frozenTargets)
	if err != nil {
		return protocol.BuildAdmission{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET targets_snapshot = ? WHERE id = ?`, targetsJSON, runID); err != nil {
		return protocol.BuildAdmission{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.BuildAdmission{}, unavailable(err)
	}
	detail, err := s.Run(ctx, runID)
	if err != nil {
		return protocol.BuildAdmission{}, err
	}
	return protocol.BuildAdmission{
		Result: protocol.AdmissionAdmitted, RequestKey: canonical.RequestKey, Run: detail,
	}, nil
}

func resolveBuildTargets(
	ctx context.Context,
	tx *sql.Tx,
	input protocol.NormalizedBuildInput,
) ([]*resolvedBuildTarget, error) {
	targets := make([]*resolvedBuildTarget, 0, len(input.References))
	seen := make(map[string]bool, len(input.References))
	for _, reference := range input.References {
		var repository protocol.TaskRepository
		err := tx.QueryRowContext(ctx, `
			SELECT id, remote_identity FROM repositories
			WHERE lower(remote_identity) = lower(?) AND centrally_managed = 1 AND enabled = 1
		`, reference.RepositoryIdentity).Scan(&repository.ID, &repository.RemoteIdentity)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, conflict(
				"repository_not_managed",
				fmt.Sprintf("repository %s is not enabled in the managed repository catalog", reference.RepositoryIdentity),
			)
		}
		if err != nil {
			return nil, unavailable(err)
		}
		targetKey := repository.ID + ":" + reference.SourceKind + ":" + reference.SourceKey
		if seen[targetKey] {
			return nil, conflict(
				"duplicate_build_target",
				fmt.Sprintf("reference %s appears more than once in this Build", reference.Reference),
			)
		}
		seen[targetKey] = true
		targets = append(targets, &resolvedBuildTarget{
			reference: reference, repository: repository, targetKey: targetKey,
		})
	}
	return targets, nil
}

func selectBuildPredecessors(
	ctx context.Context,
	tx *sql.Tx,
	targets []*resolvedBuildTarget,
	rebuild bool,
) error {
	for _, target := range targets {
		var nonterminalID string
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM sessions
			WHERE repository_id = ? AND source_kind = ? AND source_key = ?
			  AND state IN ('blocked', 'queued', 'preparing', 'running', 'needs-input')
			ORDER BY admitted_at DESC, id DESC LIMIT 1
		`, target.repository.ID, target.reference.SourceKind, target.reference.SourceKey).Scan(&nonterminalID)
		if err == nil {
			return conflict(
				"duplicate_build_active",
				fmt.Sprintf("matching Work %s is still nonterminal", nonterminalID),
			)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return unavailable(err)
		}

		var terminalID string
		err = tx.QueryRowContext(ctx, `
			SELECT candidate.id FROM sessions AS candidate
			WHERE candidate.repository_id = ? AND candidate.source_kind = ? AND candidate.source_key = ?
			  AND candidate.state IN ('ready', 'succeeded', 'failed', 'no-change', 'cancelled')
			  AND NOT EXISTS (
				SELECT 1 FROM sessions AS child
				WHERE child.predecessor_work_id = candidate.id
			  )
			ORDER BY candidate.admitted_at DESC, candidate.id DESC LIMIT 1
		`, target.repository.ID, target.reference.SourceKind, target.reference.SourceKey).Scan(&terminalID)
		if errors.Is(err, sql.ErrNoRows) {
			if rebuild {
				return conflict(
					"rebuild_predecessor_not_found",
					fmt.Sprintf("reference %s has no terminal predecessor to rebuild", target.reference.Reference),
				)
			}
			continue
		}
		if err != nil {
			return unavailable(err)
		}
		if !rebuild {
			return conflict(
				"rebuild_required",
				fmt.Sprintf("reference %s was already built; use --rebuild with a new request key", target.reference.Reference),
			)
		}
		target.predecessor = terminalID
	}
	return nil
}

func resolveStandardBuildPrompt(target protocol.WorkTarget) string {
	return protocol.StandardBuildProcedurePrompt +
		"\n\nUntrusted work-item context:\n\n" +
		"Repository: " + target.RepositoryIdentity + "\n" +
		"Reference: " + target.SourceReference + "\n" +
		"Source kind: " + target.SourceKind + "\n" +
		"Factory publish branch: " + target.PublishBranch
}
