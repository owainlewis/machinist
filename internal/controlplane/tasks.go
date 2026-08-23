package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

const (
	defaultTaskConcurrency       = 10
	defaultTaskTimeout           = 2 * time.Hour
	defaultTaskPageSize          = 50
	maxTaskPageSize              = 200
	taskConcurrencyBlockedReason = "Waiting for an available Task concurrency slot."
)

func normalizeTitleKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// normalizeMigratedTaskTitleKeys runs inside migration 027, where Tasks were
// still stored in the routines table. Migration 030 renames it to tasks.
func normalizeMigratedTaskTitleKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM routines WHERE migration_only = 0`)
	if err != nil {
		return err
	}
	type taskName struct {
		id   string
		name string
	}
	var tasks []taskName
	for rows.Next() {
		var task taskName
		if err := rows.Scan(&task.id, &task.name); err != nil {
			rows.Close()
			return err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, task := range tasks {
		if _, err := tx.ExecContext(ctx, `UPDATE routines SET name_key = ? WHERE id = ?`,
			normalizeTitleKey(task.name), task.id); err != nil {
			return err
		}
	}
	return nil
}

type normalizedTask struct {
	name               string
	nameKey            string
	prompt             string
	runtime            string
	executionProfileID string
	timeoutSeconds     int
	concurrencyLimit   int
	repositoryIDs      []string
	scheduleEnabled    bool
	cron               string
	timezone           string
	nextDueAt          *time.Time
	outcomeContract    protocol.OutcomeContract
	pipelineID         string
}

func normalizeTask(input protocol.SaveTaskRequest, now time.Time) (normalizedTask, error) {
	value := normalizedTask{
		name:               strings.TrimSpace(input.Name),
		prompt:             strings.TrimSpace(input.Prompt),
		runtime:            strings.ToLower(strings.TrimSpace(input.Runtime)),
		executionProfileID: strings.TrimSpace(input.ExecutionProfileID),
		timeoutSeconds:     input.TimeoutSeconds,
		concurrencyLimit:   input.ConcurrencyLimit,
		repositoryIDs:      append([]string(nil), input.RepositoryIDs...),
		scheduleEnabled:    input.Schedule.Enabled,
		cron:               strings.TrimSpace(input.Schedule.Cron),
		timezone:           strings.TrimSpace(input.Schedule.Timezone),
		outcomeContract:    protocol.OutcomeContract(strings.TrimSpace(string(input.OutcomeContract))),
		pipelineID:         strings.TrimSpace(input.PipelineID),
	}
	value.nameKey = normalizeTitleKey(value.name)
	if value.executionProfileID == protocol.PersistentAutoProfileID {
		value.executionProfileID = ""
	}
	if value.pipelineID == "" {
		value.pipelineID = protocol.DefaultPipelineID
	}
	if value.name == "" || utf8.RuneCountInString(value.name) > 200 {
		return value, invalid("invalid_task_name", "name is required and limited to 200 characters")
	}
	if value.prompt == "" || len([]byte(value.prompt)) > protocol.MaxTaskPromptBytes {
		return value, invalid("invalid_task_prompt", "prompt is required and limited to 64 KiB")
	}
	if value.runtime == "" {
		value.runtime = protocol.RuntimeCodex
	}
	if !protocol.SupportedRuntime(value.runtime) {
		return value, invalid("invalid_task_runtime", "runtime is not supported")
	}
	if value.outcomeContract != "" && value.outcomeContract != protocol.OutcomeProcessExit &&
		value.outcomeContract != protocol.OutcomeAgentUpdate {
		return value, invalid("invalid_outcome_contract", "outcome_contract must be process_exit or agent_update")
	}
	if value.timeoutSeconds == 0 {
		value.timeoutSeconds = int(defaultTaskTimeout.Seconds())
	}
	if value.timeoutSeconds < 1 || value.timeoutSeconds > int(protocol.MaxTimeout.Seconds()) {
		return value, invalid("invalid_task_timeout", "timeout_seconds must be between 1 and 28800")
	}
	if value.concurrencyLimit == 0 {
		value.concurrencyLimit = defaultTaskConcurrency
	}
	if value.concurrencyLimit < 1 || value.concurrencyLimit > 100 {
		return value, invalid("invalid_task_concurrency", "concurrency_limit must be between 1 and 100")
	}
	if len(value.repositoryIDs) == 0 && value.scheduleEnabled {
		return value, invalid("task_repository_required", "select at least one repository before enabling a schedule")
	}
	if len(value.repositoryIDs) > protocol.MaxTaskRepositories {
		return value, invalid("too_many_task_repositories", "a Task is limited to 100 repositories")
	}
	repositorySet := make(map[string]struct{}, len(value.repositoryIDs))
	for index, repositoryID := range value.repositoryIDs {
		repositoryID = strings.TrimSpace(repositoryID)
		if repositoryID == "" {
			return value, invalid("task_repository_required", "repository_ids cannot contain an empty value")
		}
		if _, exists := repositorySet[repositoryID]; exists {
			return value, invalid("duplicate_task_repository", "each repository may be selected once")
		}
		repositorySet[repositoryID] = struct{}{}
		value.repositoryIDs[index] = repositoryID
	}
	if value.scheduleEnabled || value.cron != "" || value.timezone != "" {
		schedule, cron, timezone, err := parseCronSchedule(value.cron, value.timezone)
		if err != nil {
			return value, invalid("invalid_task_schedule", err.Error())
		}
		next, err := schedule.Next(now)
		if err != nil {
			return value, invalid("invalid_task_schedule", err.Error())
		}
		value.cron, value.timezone, value.nextDueAt = cron, timezone, &next
		if value.scheduleEnabled {
			resolved, err := protocol.ResolveTaskSchedulePrompt(value.prompt, next, cron, timezone)
			if err != nil {
				return value, invalid("invalid_task_schedule", err.Error())
			}
			if len([]byte(resolved)) > protocol.MaxResolvedPromptBytes {
				return value, invalid("task_schedule_prompt_too_large", "shorten the prompt before enabling its schedule")
			}
		}
	} else {
		value.cron, value.timezone = "", ""
	}
	return value, nil
}

func (s *Store) CreateTask(ctx context.Context, input protocol.SaveTaskRequest) (protocol.Task, error) {
	value, err := normalizeTask(input, s.now())
	if err != nil {
		return protocol.Task{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	defer tx.Rollback()
	if value.outcomeContract == "" {
		value.outcomeContract = protocol.OutcomeProcessExit
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE migration_only = 0 AND read_only = 0`).Scan(&count); err != nil {
		return protocol.Task{}, unavailable(err)
	}
	if count >= protocol.MaxTasks {
		return protocol.Task{}, conflict("task_limit_reached", "Factory is limited to 500 Tasks")
	}
	if err := validateTaskRepositories(ctx, tx, value.repositoryIDs); err != nil {
		return protocol.Task{}, err
	}
	if err := validateTaskExecutionProfile(ctx, tx, value.executionProfileID); err != nil {
		return protocol.Task{}, err
	}
	if _, err := loadPipelineSnapshot(ctx, tx, value.pipelineID); err != nil {
		return protocol.Task{}, err
	}
	if err := validateOutcomeContractBackend(ctx, tx, value.outcomeContract, value.executionProfileID); err != nil {
		return protocol.Task{}, err
	}
	id, err := newID()
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	var next any
	if value.scheduleEnabled && value.nextDueAt != nil {
		next = value.nextDueAt.UnixMilli()
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO tasks(
			id, name, name_key, prompt, runtime, timeout_seconds,
			concurrency_limit, execution_profile_id, outcome_contract, pipeline_id, generation, archived, migration_only, schedule_enabled,
			cron, timezone, next_due_at, schedule_health_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 0, 0, ?, ?, ?, ?, ?, ?, ?)
	`, id, value.name, value.nameKey, value.prompt, value.runtime, value.timeoutSeconds,
		value.concurrencyLimit, nullableString(value.executionProfileID), value.outcomeContract, value.pipelineID, value.scheduleEnabled, nullableString(value.cron), nullableString(value.timezone),
		next, taskScheduleHealth(value.scheduleEnabled), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return protocol.Task{}, conflict("task_name_conflict", "a Task with this name already exists")
		}
		return protocol.Task{}, unavailable(err)
	}
	if err := replaceTaskRepositories(ctx, tx, id, value.repositoryIDs); err != nil {
		return protocol.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Task{}, unavailable(err)
	}
	return s.Task(ctx, id)
}

func (s *Store) UpdateTask(ctx context.Context, id string, input protocol.SaveTaskRequest) (protocol.Task, error) {
	value, err := normalizeTask(input, s.now())
	if err != nil {
		return protocol.Task{}, err
	}
	if input.ExpectedGeneration < 1 {
		return protocol.Task{}, invalid("task_generation_required", "expected_generation is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	defer tx.Rollback()
	if err := validateTaskRepositories(ctx, tx, value.repositoryIDs); err != nil {
		return protocol.Task{}, err
	}
	if err := validateTaskExecutionProfile(ctx, tx, value.executionProfileID); err != nil {
		return protocol.Task{}, err
	}
	var archived, migrationOnly, readOnly int
	var currentOutcome protocol.OutcomeContract
	var currentPipelineID string
	var pendingDue, scheduleRetry sql.NullInt64
	var scheduleHealthStatus, scheduleHealthCode string
	err = tx.QueryRowContext(ctx, `
		SELECT archived, migration_only, read_only, outcome_contract, COALESCE(pipeline_id, ?), pending_due_at, schedule_retry_at,
		       schedule_health_status, schedule_health_code
		FROM tasks WHERE id = ?
	`, protocol.DefaultPipelineID, id).Scan(&archived, &migrationOnly, &readOnly, &currentOutcome, &currentPipelineID, &pendingDue, &scheduleRetry,
		&scheduleHealthStatus, &scheduleHealthCode)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Task{}, ErrNotFound
	}
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	if migrationOnly != 0 {
		return protocol.Task{}, conflict("task_read_only", "migration history Tasks are read-only")
	}
	if readOnly != 0 {
		return protocol.Task{}, conflict("task_read_only", "historical Task revisions are read-only")
	}
	if archived != 0 && value.scheduleEnabled {
		return protocol.Task{}, conflict("task_archived", "an archived Task cannot be scheduled")
	}
	if value.outcomeContract == "" {
		value.outcomeContract = currentOutcome
	}
	if strings.TrimSpace(input.PipelineID) == "" {
		value.pipelineID = currentPipelineID
	}
	if _, err := loadPipelineSnapshot(ctx, tx, value.pipelineID); err != nil {
		return protocol.Task{}, err
	}
	if currentOutcome == protocol.OutcomeAgentUpdate && value.outcomeContract == protocol.OutcomeProcessExit {
		return protocol.Task{}, conflict(
			"outcome_contract_conversion_invalid",
			"agent_update cannot be converted back to process_exit",
		)
	}
	if err := validateOutcomeContractBackend(ctx, tx, value.outcomeContract, value.executionProfileID); err != nil {
		return protocol.Task{}, err
	}
	now := s.now().UnixMilli()
	var next any
	if value.scheduleEnabled && value.nextDueAt != nil && !pendingDue.Valid {
		next = value.nextDueAt.UnixMilli()
	}
	preserveBlockedOccurrence := pendingDue.Valid && !scheduleRetry.Valid &&
		(scheduleHealthStatus == "blocked" || scheduleHealthCode != "")
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks SET
			name = ?, name_key = ?, prompt = ?, runtime = ?, timeout_seconds = ?,
			concurrency_limit = ?, execution_profile_id = ?, outcome_contract = ?, pipeline_id = ?, generation = generation + 1, schedule_enabled = ?,
			cron = CASE WHEN pending_due_at IS NOT NULL AND ? = 0 THEN cron ELSE ? END,
			timezone = CASE WHEN pending_due_at IS NOT NULL AND ? = 0 THEN timezone ELSE ? END,
			next_due_at = CASE WHEN pending_due_at IS NULL THEN ? ELSE next_due_at END,
			schedule_health_status = CASE
				WHEN ? = 1 AND ? = 1 THEN 'blocked'
				WHEN ? = 1 THEN 'healthy' ELSE 'disabled' END,
			schedule_health_code = CASE
				WHEN ? = 1 THEN schedule_health_code
				ELSE '' END,
			schedule_health_message = CASE
				WHEN ? = 1 THEN schedule_health_message
				ELSE '' END,
			updated_at = ?
		WHERE id = ? AND generation = ?
	`, value.name, value.nameKey, value.prompt, value.runtime, value.timeoutSeconds,
		value.concurrencyLimit, nullableString(value.executionProfileID), value.outcomeContract, value.pipelineID, value.scheduleEnabled, value.scheduleEnabled, nullableString(value.cron),
		value.scheduleEnabled, nullableString(value.timezone),
		next, value.scheduleEnabled, preserveBlockedOccurrence, value.scheduleEnabled,
		preserveBlockedOccurrence, preserveBlockedOccurrence, now, id, input.ExpectedGeneration)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return protocol.Task{}, conflict("task_name_conflict", "a Task with this name already exists")
		}
		return protocol.Task{}, unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return protocol.Task{}, conflict("task_generation_conflict", "the Task changed; refresh and try again")
	}
	if err := replaceTaskRepositories(ctx, tx, id, value.repositoryIDs); err != nil {
		return protocol.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Task{}, unavailable(err)
	}
	return s.Task(ctx, id)
}

func taskScheduleHealth(enabled bool) string {
	if enabled {
		return "healthy"
	}
	return "disabled"
}

func validateTaskRepositories(ctx context.Context, tx *sql.Tx, ids []string) error {
	for _, id := range ids {
		var enabled, centrallyManaged, advertised int
		err := tx.QueryRowContext(ctx, `
			SELECT repository.enabled, repository.centrally_managed,
			       EXISTS (SELECT 1 FROM worker_repositories available
			               WHERE available.repository_id = repository.id AND available.advertised = 1)
			FROM repositories repository WHERE repository.id = ?
		`, id).Scan(&enabled, &centrallyManaged, &advertised)
		if errors.Is(err, sql.ErrNoRows) {
			return invalid("task_repository_not_found", "one selected repository does not exist")
		}
		if err != nil {
			return unavailable(err)
		}
		if centrallyManaged != 0 && enabled == 0 || centrallyManaged == 0 && advertised == 0 {
			return conflict("task_repository_unavailable", "every selected repository must be enabled or advertised by a Worker")
		}
	}
	return nil
}

func validateTaskExecutionProfile(ctx context.Context, tx *sql.Tx, id string) error {
	if id == "" {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_profiles WHERE id = ?`, id).Scan(&exists); err != nil {
		return unavailable(err)
	}
	if exists == 0 {
		return invalid("execution_profile_not_found", "the selected execution profile does not exist")
	}
	return nil
}

func validateOutcomeContractBackend(
	ctx context.Context,
	tx *sql.Tx,
	contract protocol.OutcomeContract,
	profileID string,
) error {
	if contract != protocol.OutcomeAgentUpdate || profileID == "" || profileID == protocol.PersistentAutoProfileID {
		return nil
	}
	var backend string
	err := tx.QueryRowContext(ctx, `
		SELECT version.kind
		FROM execution_profiles profile
		JOIN execution_profile_versions version
		  ON version.profile_id = profile.id AND version.version = profile.current_version
		WHERE profile.id = ?
	`, profileID).Scan(&backend)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("execution_profile_not_found", "the selected execution profile does not exist")
	}
	if err != nil {
		return unavailable(err)
	}
	if backend != protocol.BackendPersistent {
		return conflict(
			"agent_update_backend_unsupported",
			"agent_update requires the persistent execution backend",
		)
	}
	return nil
}

func replaceTaskRepositories(ctx context.Context, tx *sql.Tx, taskID string, ids []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_repositories WHERE task_id = ?`, taskID); err != nil {
		return unavailable(err)
	}
	for position, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_repositories(task_id, position, repository_id) VALUES (?, ?, ?)`, taskID, position, id); err != nil {
			return unavailable(err)
		}
	}
	return nil
}

func (s *Store) SetTaskArchived(ctx context.Context, id string, input protocol.SetTaskArchivedRequest) (protocol.Task, error) {
	if input.Archived == nil {
		return protocol.Task{}, invalid("task_archived_required", "archived is required")
	}
	if input.ExpectedGeneration < 1 {
		return protocol.Task{}, invalid("task_generation_required", "expected_generation is required")
	}
	archived := *input.Archived
	now := s.now().UnixMilli()
	result, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET archived = ?, generation = generation + 1,
			schedule_enabled = CASE WHEN ? = 1 THEN 0 ELSE schedule_enabled END,
			next_due_at = CASE WHEN ? = 1 THEN NULL ELSE next_due_at END,
			schedule_health_status = CASE WHEN ? = 1 THEN 'disabled' ELSE schedule_health_status END,
			updated_at = ?
		WHERE id = ? AND generation = ? AND migration_only = 0 AND read_only = 0
	`, archived, archived, archived, archived, now, id, input.ExpectedGeneration)
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var exists, readOnly int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(read_only), 0) FROM tasks WHERE id = ?`, id).Scan(&exists, &readOnly)
		if exists == 0 {
			return protocol.Task{}, ErrNotFound
		}
		if readOnly != 0 {
			return protocol.Task{}, conflict("task_read_only", "historical Task revisions are read-only")
		}
		return protocol.Task{}, conflict("task_generation_conflict", "the Task changed; refresh and try again")
	}
	return s.Task(ctx, id)
}

func (s *Store) SetTaskOutcomeContract(
	ctx context.Context,
	id string,
	input protocol.SetTaskOutcomeContractRequest,
) (protocol.Task, error) {
	if input.OutcomeContract != protocol.OutcomeProcessExit && input.OutcomeContract != protocol.OutcomeAgentUpdate {
		return protocol.Task{}, invalid("invalid_outcome_contract", "outcome_contract must be process_exit or agent_update")
	}
	if input.ExpectedGeneration < 1 {
		return protocol.Task{}, invalid("task_generation_required", "expected_generation is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	defer tx.Rollback()
	var generation, archived, migrationOnly, readOnly int
	var current protocol.OutcomeContract
	var profileID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT generation, outcome_contract, execution_profile_id, archived, migration_only, read_only
		FROM tasks WHERE id = ?
	`, id).Scan(&generation, &current, &profileID, &archived, &migrationOnly, &readOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Task{}, ErrNotFound
	}
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	if generation != input.ExpectedGeneration {
		return protocol.Task{}, conflict("task_generation_conflict", "the Task changed; refresh and try again")
	}
	if archived != 0 || migrationOnly != 0 || readOnly != 0 {
		return protocol.Task{}, conflict("task_read_only", "only an active editable Task can change outcome contract")
	}
	if current == input.OutcomeContract {
		if err := tx.Commit(); err != nil {
			return protocol.Task{}, unavailable(err)
		}
		return s.Task(ctx, id)
	}
	if current == protocol.OutcomeAgentUpdate && input.OutcomeContract == protocol.OutcomeProcessExit {
		return protocol.Task{}, conflict(
			"outcome_contract_conversion_invalid",
			"agent_update cannot be converted back to process_exit",
		)
	}
	if err := validateOutcomeContractBackend(ctx, tx, input.OutcomeContract, profileID.String); err != nil {
		return protocol.Task{}, err
	}
	now := s.now().UnixMilli()
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks SET outcome_contract = ?, generation = generation + 1, updated_at = ?
		WHERE id = ? AND generation = ?
	`, input.OutcomeContract, now, id, input.ExpectedGeneration)
	if err != nil {
		return protocol.Task{}, unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return protocol.Task{}, conflict("task_generation_conflict", "the Task changed; refresh and try again")
	}
	if err := tx.Commit(); err != nil {
		return protocol.Task{}, unavailable(err)
	}
	return s.Task(ctx, id)
}

func (s *Store) Tasks(ctx context.Context, includeArchived bool, limit int, cursor string) (protocol.TaskPage, error) {
	if limit == 0 {
		limit = defaultTaskPageSize
	}
	if limit < 1 || limit > maxTaskPageSize {
		return protocol.TaskPage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	updated, cursorID, err := decodeRunCursor(cursor)
	if err != nil {
		return protocol.TaskPage{}, err
	}
	query := `SELECT id, updated_at FROM tasks WHERE migration_only = 0`
	args := make([]any, 0, 4)
	if !includeArchived {
		query += ` AND archived = 0`
	}
	if updated != 0 {
		query += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		args = append(args, updated, updated, cursorID)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.TaskPage{}, unavailable(err)
	}
	type taskKey struct {
		id      string
		updated int64
	}
	var keys []taskKey
	for rows.Next() {
		var key taskKey
		if err := rows.Scan(&key.id, &key.updated); err != nil {
			rows.Close()
			return protocol.TaskPage{}, unavailable(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		return protocol.TaskPage{}, unavailable(err)
	}
	hasMore := len(keys) > limit
	if hasMore {
		keys = keys[:limit]
	}
	page := protocol.TaskPage{Tasks: make([]protocol.Task, 0, len(keys))}
	for _, key := range keys {
		task, err := s.Task(ctx, key.id)
		if err != nil {
			return protocol.TaskPage{}, err
		}
		page.Tasks = append(page.Tasks, taskSummary(task))
	}
	if hasMore {
		last := keys[len(keys)-1]
		page.NextCursor = encodeRunCursor(last.updated, last.id)
	}
	return page, nil
}

func taskSummary(task protocol.Task) protocol.Task {
	task.PromptPreview = task.Prompt
	preview := []rune(task.PromptPreview)
	if len(preview) > 180 {
		task.PromptPreview = string(preview[:180]) + "…"
	}
	task.Prompt = ""
	task.RepositoryCount = len(task.Repositories)
	task.Repositories = nil
	return task
}

func (s *Store) Task(ctx context.Context, id string) (protocol.Task, error) {
	var task protocol.Task
	var archived, readOnly, scheduleEnabled int
	var cron, timezone sql.NullString
	var nextDue, pendingDue sql.NullInt64
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `
		SELECT task.id, task.name, task.prompt, task.runtime, COALESCE(task.execution_profile_id, ''), task.timeout_seconds, task.concurrency_limit,
		       task.generation, task.outcome_contract, COALESCE(task.pipeline_id, ?), pipeline.name,
		       task.archived, task.read_only, task.schedule_enabled, task.cron, task.timezone, task.next_due_at, task.pending_due_at,
		       task.schedule_health_status, task.schedule_health_code, task.schedule_health_message, task.created_at, task.updated_at
		FROM tasks task JOIN pipelines pipeline ON pipeline.id = COALESCE(task.pipeline_id, ?)
		WHERE task.id = ? AND task.migration_only = 0
	`, protocol.DefaultPipelineID, protocol.DefaultPipelineID, id).Scan(&task.ID, &task.Name, &task.Prompt, &task.Runtime, &task.ExecutionProfileID,
		&task.TimeoutSeconds, &task.ConcurrencyLimit, &task.Generation, &task.OutcomeContract, &task.PipelineID, &task.PipelineName, &archived, &readOnly,
		&scheduleEnabled, &cron, &timezone, &nextDue, &pendingDue, &task.Schedule.HealthStatus,
		&task.Schedule.HealthCode, &task.Schedule.HealthMessage, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return task, ErrNotFound
	}
	if err != nil {
		return task, unavailable(err)
	}
	task.Archived = archived != 0
	task.ReadOnly = readOnly != 0
	task.Schedule.Enabled = scheduleEnabled != 0
	task.Schedule.Cron, task.Schedule.Timezone = cron.String, timezone.String
	if nextDue.Valid {
		value := fromMillis(nextDue.Int64)
		task.Schedule.NextDueAt = &value
	}
	if pendingDue.Valid {
		value := fromMillis(pendingDue.Int64)
		task.Schedule.PendingDueAt = &value
	}
	task.CreatedAt, task.UpdatedAt = fromMillis(created), fromMillis(updated)
	rows, err := s.db.QueryContext(ctx, `
		SELECT repository.id, repository.remote_identity
		FROM task_repositories selected
		JOIN repositories repository ON repository.id = selected.repository_id
		WHERE selected.task_id = ? ORDER BY selected.position
	`, id)
	if err != nil {
		return task, unavailable(err)
	}
	for rows.Next() {
		var repository protocol.TaskRepository
		if err := rows.Scan(&repository.ID, &repository.RemoteIdentity); err != nil {
			rows.Close()
			return task, unavailable(err)
		}
		task.Repositories = append(task.Repositories, repository)
	}
	if err := rows.Close(); err != nil {
		return task, unavailable(err)
	}
	task.RepositoryCount = len(task.Repositories)
	_ = s.db.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT CASE
			WHEN SUM(session.state IN ('blocked','queued','preparing','running','needs-input')) = 0 THEN CASE
				WHEN SUM(session.state IN ('ready','succeeded','no-change')) = COUNT(*) THEN 'succeeded'
				WHEN SUM(session.state = 'cancelled') = COUNT(*) THEN 'cancelled'
				WHEN SUM(session.state IN ('ready','succeeded','no-change')) = 0 AND SUM(session.state = 'failed') > 0 THEN 'failed'
				ELSE 'partial' END
			WHEN SUM(session.state IN ('preparing','running')) > 0
			  OR SUM(session.state IN ('ready','succeeded','failed','no-change','cancelled')) > 0 THEN 'running'
			WHEN SUM(session.state IN ('blocked','needs-input')) = SUM(session.state IN ('blocked','queued','preparing','running','needs-input')) THEN 'blocked'
			ELSE 'queued' END
		FROM runs recent JOIN sessions session ON session.run_id = recent.id
		WHERE recent.task_id = ? GROUP BY recent.id ORDER BY recent.admitted_at DESC LIMIT 1), '')
	`, id).Scan(&task.LastRunState)
	return task, nil
}

func (s *Store) RunTask(ctx context.Context, id string, input protocol.RunTaskRequest) (protocol.RunDetail, bool, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	input.ExecutionProfileID = strings.TrimSpace(input.ExecutionProfileID)
	if input.RequestKey == "" || len(input.RequestKey) > 200 {
		return protocol.RunDetail{}, false, invalid("invalid_request_key", "request_key is required and limited to 200 bytes")
	}
	if strings.HasPrefix(input.RequestKey, "schedule:") {
		return protocol.RunDetail{}, false, invalid("reserved_request_key", "request_key uses a reserved internal prefix")
	}
	return s.admitTask(ctx, id, "manual", input.RequestKey, nil, nil, input.ExecutionProfileID)
}

func (s *Store) admitTask(
	ctx context.Context,
	taskID, source, requestKey string,
	scheduledAt *time.Time,
	frozen *protocol.TaskSnapshot,
	requestedProfileID string,
) (protocol.RunDetail, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	defer tx.Rollback()
	var existingID, existingTaskID, existingSource string
	var existingScheduledAt sql.NullInt64
	var existingRequestedProfile sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT id, task_id, source, scheduled_at, requested_execution_profile_id FROM runs run WHERE request_key = ?
	`, requestKey).Scan(&existingID, &existingTaskID, &existingSource, &existingScheduledAt, &existingRequestedProfile)
	if err == nil {
		sameSchedule := scheduledAt == nil && !existingScheduledAt.Valid
		if scheduledAt != nil && existingScheduledAt.Valid {
			sameSchedule = scheduledAt.UTC().UnixMilli() == existingScheduledAt.Int64
		}
		if existingTaskID != taskID || existingSource != source || !sameSchedule || existingRequestedProfile.String != requestedProfileID {
			return protocol.RunDetail{}, false, conflict("request_key_conflict", "request_key was already used with different Run inputs")
		}
		if err := tx.Commit(); err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		detail, err := s.Run(ctx, existingID)
		return detail, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	snapshot := protocol.TaskSnapshot{}
	if frozen != nil {
		snapshot = *frozen
		if snapshot.OutcomeContract == "" {
			snapshot.OutcomeContract = protocol.OutcomeProcessExit
		}
		if snapshot.Pipeline.ID == "" || len(snapshot.Pipeline.Stages) == 0 {
			pipeline, err := loadPipelineSnapshot(ctx, tx, protocol.DefaultPipelineID)
			if err != nil {
				return protocol.RunDetail{}, false, err
			}
			snapshot.Pipeline = pipeline
		}
		var archived, migrationOnly, readOnly, scheduleEnabled int
		var pendingDue sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT archived, migration_only, read_only, schedule_enabled, pending_due_at
			FROM tasks WHERE id = ?
		`, taskID).Scan(&archived, &migrationOnly, &readOnly, &scheduleEnabled, &pendingDue)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.RunDetail{}, false, ErrNotFound
		}
		if err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		if readOnly != 0 {
			return protocol.RunDetail{}, false, conflict("task_read_only", "historical Task revisions cannot start Runs")
		}
		if archived != 0 || migrationOnly != 0 {
			return protocol.RunDetail{}, false, conflict("task_archived", "archived Tasks cannot start Runs")
		}
		if scheduleEnabled == 0 {
			return protocol.RunDetail{}, false, conflict("task_schedule_disabled", "disabled Task schedules cannot start Runs")
		}
		if scheduledAt == nil || !pendingDue.Valid || pendingDue.Int64 != scheduledAt.UTC().UnixMilli() {
			return protocol.RunDetail{}, false, conflict("task_occurrence_changed", "the scheduled Task occurrence changed")
		}
	} else {
		var archived, migrationOnly, readOnly int
		err := tx.QueryRowContext(ctx, `
			SELECT id, name, prompt, runtime, COALESCE(execution_profile_id, ''), timeout_seconds, concurrency_limit,
			       generation, outcome_contract, COALESCE(pipeline_id, ?), archived, migration_only, read_only
			FROM tasks WHERE id = ?
		`, protocol.DefaultPipelineID, taskID).Scan(&snapshot.ID, &snapshot.Name, &snapshot.Prompt, &snapshot.Runtime,
			&snapshot.ExecutionProfileID, &snapshot.TimeoutSeconds, &snapshot.ConcurrencyLimit, &snapshot.Generation,
			&snapshot.OutcomeContract, &snapshot.Pipeline.ID, &archived, &migrationOnly, &readOnly)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.RunDetail{}, false, ErrNotFound
		}
		if err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		if readOnly != 0 {
			return protocol.RunDetail{}, false, conflict("task_read_only", "historical Task revisions cannot start Runs")
		}
		if archived != 0 || migrationOnly != 0 {
			return protocol.RunDetail{}, false, conflict("task_archived", "archived Tasks cannot start Runs")
		}
		pipeline, err := loadPipelineSnapshot(ctx, tx, snapshot.Pipeline.ID)
		if err != nil {
			return protocol.RunDetail{}, false, err
		}
		snapshot.Pipeline = pipeline
		rows, err := tx.QueryContext(ctx, `
			SELECT repository.id, repository.remote_identity
			FROM task_repositories selected
			JOIN repositories repository ON repository.id = selected.repository_id
			WHERE selected.task_id = ? ORDER BY selected.position
		`, taskID)
		if err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		for rows.Next() {
			var repository protocol.TaskRepository
			if err := rows.Scan(&repository.ID, &repository.RemoteIdentity); err != nil {
				rows.Close()
				return protocol.RunDetail{}, false, unavailable(err)
			}
			snapshot.Repositories = append(snapshot.Repositories, repository)
		}
		if err := rows.Close(); err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
	}
	if len(snapshot.Repositories) == 0 {
		return protocol.RunDetail{}, false, conflict("task_has_no_repositories", "select at least one repository before running this Task")
	}
	execution, profileReady, profileBlockedReason, err := loadExecutionSnapshot(ctx, tx, snapshot, requestedProfileID)
	if err != nil {
		return protocol.RunDetail{}, false, err
	}
	if snapshot.OutcomeContract == protocol.OutcomeAgentUpdate && execution.Backend != protocol.BackendPersistent {
		return protocol.RunDetail{}, false, conflict(
			"agent_update_backend_unsupported",
			"agent_update requires the persistent execution backend",
		)
	}
	if execution.Backend != protocol.BackendPersistent && len(snapshot.Pipeline.Stages) > 1 {
		return protocol.RunDetail{}, false, conflict(
			"pipeline_backend_unsupported",
			"multi-stage Pipelines currently require a persistent Worker",
		)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	digestBody, _ := json.Marshal(struct {
		RequestKey  string                     `json:"request_key"`
		TaskID      string                     `json:"task_id"`
		Source      string                     `json:"source"`
		ScheduledAt *time.Time                 `json:"scheduled_at,omitempty"`
		Snapshot    protocol.TaskSnapshot      `json:"snapshot"`
		Execution   protocol.ExecutionSnapshot `json:"execution"`
	}{requestKey, taskID, source, scheduledAt, snapshot, execution})
	digestArray := sha256.Sum256(digestBody)
	digest := digestArray[:]
	if err := validateFrozenRepositories(ctx, tx, snapshot.Repositories); err != nil {
		return protocol.RunDetail{}, false, err
	}
	runID, err := newID()
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	now := s.now().UnixMilli()
	var scheduled any
	if scheduledAt != nil {
		scheduled = scheduledAt.UTC().UnixMilli()
	}
	executionJSON, err := json.Marshal(execution)
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runs(id, request_key, request_digest, task_id, task_snapshot, source,
		                 scheduled_at, requested_execution_profile_id, execution_snapshot,
		                 outcome_contract, admitted_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, runID, requestKey, digest, taskID, snapshotJSON, source, scheduled,
		nullableString(requestedProfileID), executionJSON, snapshot.OutcomeContract, now, now); err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	resolvedPrompt := snapshot.Prompt
	if source == "schedule" && scheduledAt != nil {
		cron, timezone := snapshot.ScheduleCron, snapshot.ScheduleTimezone
		if cron == "" || timezone == "" {
			if err := tx.QueryRowContext(ctx, `SELECT cron, timezone FROM tasks WHERE id = ?`, taskID).Scan(&cron, &timezone); err != nil {
				return protocol.RunDetail{}, false, unavailable(err)
			}
		}
		resolvedPrompt, err = protocol.ResolveTaskSchedulePrompt(snapshot.Prompt, *scheduledAt, cron, timezone)
		if err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
	}
	if len([]byte(resolvedPrompt)) > protocol.MaxResolvedPromptBytes {
		return protocol.RunDetail{}, false, conflict("resolved_prompt_too_large", "the frozen Task prompt exceeds 64 KiB")
	}
	materialized := 0
	targets := make([]protocol.WorkTarget, 0, len(snapshot.Repositories))
	for position, repository := range snapshot.Repositories {
		sessionID, err := newID()
		if err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		target := protocol.WorkTarget{
			ID: sessionID, Position: position, TargetKey: "repository:" + repository.ID,
			TargetKind: "repository", RepositoryID: repository.ID,
			RepositoryIdentity: repository.RemoteIdentity, SourceKind: "repository",
			SourceKey: repository.ID, SourceReference: repository.RemoteIdentity,
			PublishBranch: workPublishBranch(sessionID),
		}
		resolvedStages, err := resolveSessionStages(snapshot, resolvedPrompt, runID, target)
		if err != nil {
			return protocol.RunDetail{}, false, err
		}
		state, blockedReason := "blocked", taskConcurrencyBlockedReason
		var assigned any
		var selection runRouteCandidate
		if !profileReady {
			blockedReason = profileBlockedReason
		} else if materialized < snapshot.ConcurrencyLimit {
			if execution.Backend == protocol.BackendPersistent {
				selection, err = s.selectSessionRoute(ctx, tx, repository.ID, repository.RemoteIdentity, now, "", snapshot.Runtime)
				blockedReason = "Waiting for a healthy compatible Worker with repository access."
				if err == nil {
					state, blockedReason, assigned = "queued", "", selection.workerID
					materialized++
				} else if !serviceErrorCode(err, "no_eligible_worker") {
					return protocol.RunDetail{}, false, err
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
					execution_model, resource_class, commit_resolution_policy
					, target_position, target_key, target_kind, source_kind, source_key,
					source_reference, context_snapshot, publish_branch, execution_owner, waiting_reason
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, sessionID, runID, repository.ID, repository.RemoteIdentity, resolvedPrompt, snapshot.Runtime,
			execution.TimeoutSeconds, state, nullableString(blockedReason), assigned, now,
			execution.ProfileID, execution.ProfileVersion, execution.Backend, execution.Provider,
			execution.Model, execution.ResourceClass, execution.CommitResolutionPolicy,
			target.Position, target.TargetKey, target.TargetKind, target.SourceKind, target.SourceKey,
			target.SourceReference, target.ContextSnapshot, target.PublishBranch, protocol.ExecutionOwnerNone,
			boundedUTF8Bytes(blockedReason, protocol.MaxWaitingReasonBytes)); err != nil {
			return protocol.RunDetail{}, false, unavailable(err)
		}
		if err := insertSessionStages(ctx, tx, sessionID, resolvedStages); err != nil {
			return protocol.RunDetail{}, false, err
		}
		targets = append(targets, target)
		if state == "queued" {
			executionID, err := newID()
			if err != nil {
				return protocol.RunDetail{}, false, unavailable(err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO executions(id, session_id, assigned_worker_id, required_runtime, state, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'queued', ?, ?)
			`, executionID, sessionID, selection.workerID, snapshot.Runtime, now, now); err != nil {
				return protocol.RunDetail{}, false, unavailable(err)
			}
		}
	}
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET targets_snapshot = ? WHERE id = ?`, targetsJSON, runID); err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.RunDetail{}, false, unavailable(err)
	}
	detail, err := s.Run(ctx, runID)
	return detail, true, err
}

func resolveSessionStages(
	task protocol.TaskSnapshot,
	resolvedPrompt string,
	runID string,
	target protocol.WorkTarget,
) ([]protocol.StageRun, error) {
	stages := make([]protocol.StageRun, 0, len(task.Pipeline.Stages))
	for _, stage := range task.Pipeline.Stages {
		prompt := renderPipelinePrompt(stage.Prompt, map[string]string{
			"task.id": task.ID, "task.name": task.Name, "task.prompt": resolvedPrompt,
			"run.id": runID, "repository": target.RepositoryIdentity, "branch": target.PublishBranch,
		})
		if !protocol.AgentPromptFits(task.Name, target.RepositoryIdentity, prompt) {
			return nil, conflict("agent_prompt_too_large", "one rendered Pipeline stage cannot fit the Worker request")
		}
		stages = append(stages, protocol.StageRun{
			Position: stage.Position, Name: stage.Name, Prompt: prompt, State: protocol.StagePending,
		})
	}
	return stages, nil
}

func insertSessionStages(ctx context.Context, tx *sql.Tx, sessionID string, stages []protocol.StageRun) error {
	for _, stage := range stages {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_stages(session_id, position, name, prompt, state)
			VALUES (?, ?, ?, ?, 'pending')
		`, sessionID, stage.Position, stage.Name, stage.Prompt); err != nil {
			return unavailable(err)
		}
	}
	return nil
}

func workPublishBranch(workID string) string {
	compact := strings.ReplaceAll(workID, "-", "")
	if len(compact) > 16 {
		compact = compact[:16]
	}
	return "factory/work-" + compact
}

func boundedUTF8Bytes(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func loadExecutionSnapshot(
	ctx context.Context,
	tx *sql.Tx,
	task protocol.TaskSnapshot,
	requestedProfileID string,
) (protocol.ExecutionSnapshot, bool, string, error) {
	profileID := requestedProfileID
	if profileID == "" {
		profileID = task.ExecutionProfileID
	}
	if profileID == "" || profileID == protocol.PersistentAutoProfileID {
		return protocol.ExecutionSnapshot{
			ProfileID: protocol.PersistentAutoProfileID, ProfileVersion: 1,
			Backend: protocol.BackendPersistent, Runtime: task.Runtime,
			Provider: "worker", Model: "worker-default", TimeoutSeconds: task.TimeoutSeconds,
			ResourceClass: "worker", CommitResolutionPolicy: protocol.CommitResolvePerAttempt,
		}, true, "", nil
	}
	var snapshot protocol.ExecutionSnapshot
	var enabled, healthy int
	var reason string
	err := tx.QueryRowContext(ctx, `
		SELECT p.id, p.current_version, v.kind, v.runtime, v.provider, v.model,
		       v.timeout_seconds, v.resource_class, v.commit_resolution_policy,
		       p.enabled, p.healthy, p.health_reason
		FROM execution_profiles p
		JOIN execution_profile_versions v ON v.profile_id = p.id AND v.version = p.current_version
		WHERE p.id = ?
	`, profileID).Scan(&snapshot.ProfileID, &snapshot.ProfileVersion, &snapshot.Backend,
		&snapshot.Runtime, &snapshot.Provider, &snapshot.Model, &snapshot.TimeoutSeconds,
		&snapshot.ResourceClass, &snapshot.CommitResolutionPolicy, &enabled, &healthy, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, false, "", invalid("execution_profile_not_found", "the selected execution profile does not exist")
	}
	if err != nil {
		return snapshot, false, "", unavailable(err)
	}
	if snapshot.Runtime != task.Runtime {
		return snapshot, false, fmt.Sprintf("Execution profile %s does not support runtime %s.", profileID, task.Runtime), nil
	}
	if enabled == 0 {
		return snapshot, false, fmt.Sprintf("Execution profile %s is disabled.", profileID), nil
	}
	if healthy == 0 {
		if reason == "" {
			reason = "health validation has not passed"
		}
		return snapshot, false, fmt.Sprintf("Execution profile %s is unhealthy: %s", profileID, reason), nil
	}
	return snapshot, true, "", nil
}

func validateFrozenRepositories(ctx context.Context, tx *sql.Tx, repositories []protocol.TaskRepository) error {
	for _, repository := range repositories {
		var currentIdentity string
		var enabled, centrallyManaged, advertised int
		err := tx.QueryRowContext(ctx, `
			SELECT current.remote_identity, current.enabled, current.centrally_managed,
			       EXISTS (SELECT 1 FROM worker_repositories available
			               WHERE available.repository_id = current.id AND available.advertised = 1)
			FROM repositories current WHERE current.id = ?
		`, repository.ID).Scan(&currentIdentity, &enabled, &centrallyManaged, &advertised)
		if errors.Is(err, sql.ErrNoRows) {
			return conflict("task_repository_missing", fmt.Sprintf("repository %s no longer exists", repository.RemoteIdentity))
		}
		if err != nil {
			return unavailable(err)
		}
		if centrallyManaged != 0 && enabled == 0 || centrallyManaged == 0 && advertised == 0 {
			return conflict("task_repository_unavailable", fmt.Sprintf("repository %s is disabled or unavailable", repository.RemoteIdentity))
		}
	}
	return nil
}

func encodeRunCursor(admittedAt int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s", admittedAt, id)))
}

func decodeRunCursor(value string) (int64, string, error) {
	if value == "" {
		return 0, "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, "", invalid("invalid_cursor", "cursor is invalid")
	}
	var admitted int64
	var id string
	if _, err := fmt.Sscanf(string(decoded), "%d:%s", &admitted, &id); err != nil || id == "" {
		return 0, "", invalid("invalid_cursor", "cursor is invalid")
	}
	return admitted, id, nil
}

func (s *Store) RunPage(ctx context.Context, limit int, cursor string) (protocol.RunPage, error) {
	if limit == 0 {
		limit = defaultTaskPageSize
	}
	if limit < 1 || limit > maxTaskPageSize {
		return protocol.RunPage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	admitted, cursorID, err := decodeRunCursor(cursor)
	if err != nil {
		return protocol.RunPage{}, err
	}
	query := `SELECT id, admitted_at FROM runs run WHERE 1 = 1`
	args := make([]any, 0, 5)
	if admitted != 0 {
		query += ` AND (admitted_at < ? OR (admitted_at = ? AND id < ?))`
		args = append(args, admitted, admitted, cursorID)
	}
	query += ` ORDER BY admitted_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.RunPage{}, unavailable(err)
	}
	type key struct {
		id       string
		admitted int64
	}
	var keys []key
	for rows.Next() {
		var value key
		if err := rows.Scan(&value.id, &value.admitted); err != nil {
			rows.Close()
			return protocol.RunPage{}, unavailable(err)
		}
		keys = append(keys, value)
	}
	if err := rows.Close(); err != nil {
		return protocol.RunPage{}, unavailable(err)
	}
	hasMore := len(keys) > limit
	if hasMore {
		keys = keys[:limit]
	}
	page := protocol.RunPage{Runs: make([]protocol.Run, 0, len(keys))}
	for _, key := range keys {
		run, err := s.runListEntry(ctx, key.id)
		if err != nil {
			return protocol.RunPage{}, err
		}
		page.Runs = append(page.Runs, run)
	}
	if hasMore {
		last := keys[len(keys)-1]
		page.NextCursor = encodeRunCursor(last.admitted, last.id)
	}
	return page, nil
}

func (s *Store) RunSummaryPage(ctx context.Context, limit int, cursor string) (protocol.RunListPage, error) {
	if limit == 0 {
		limit = defaultTaskPageSize
	}
	if limit < 1 || limit > maxTaskPageSize {
		return protocol.RunListPage{}, invalid("invalid_limit", "limit must be between 1 and 200")
	}
	admitted, cursorID, err := decodeRunCursor(cursor)
	if err != nil {
		return protocol.RunListPage{}, err
	}
	query := `
		SELECT id, json_extract(task_snapshot, '$.name'), source, admitted_at, updated_at, terminal_at
		FROM runs WHERE 1 = 1
	`
	args := make([]any, 0, 5)
	if admitted != 0 {
		query += ` AND (admitted_at < ? OR (admitted_at = ? AND id < ?))`
		args = append(args, admitted, admitted, cursorID)
	}
	query += ` ORDER BY admitted_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return protocol.RunListPage{}, unavailable(err)
	}
	page := protocol.RunListPage{Runs: make([]protocol.RunListSummary, 0, limit)}
	var admittedMillis []int64
	for rows.Next() {
		var summary protocol.RunListSummary
		var admittedAt, updatedAt int64
		var terminalAt sql.NullInt64
		if err := rows.Scan(&summary.ID, &summary.TaskName, &summary.Source, &admittedAt, &updatedAt, &terminalAt); err != nil {
			rows.Close()
			return protocol.RunListPage{}, unavailable(err)
		}
		summary.AdmittedAt, summary.UpdatedAt = fromMillis(admittedAt), fromMillis(updatedAt)
		if terminalAt.Valid {
			value := fromMillis(terminalAt.Int64)
			summary.TerminalAt = &value
		}
		page.Runs = append(page.Runs, summary)
		admittedMillis = append(admittedMillis, admittedAt)
	}
	if err := rows.Close(); err != nil {
		return protocol.RunListPage{}, unavailable(err)
	}
	hasMore := len(page.Runs) > limit
	if hasMore {
		page.Runs = page.Runs[:limit]
		admittedMillis = admittedMillis[:limit]
	}
	for index := range page.Runs {
		if err := s.applyRunListSummary(ctx, &page.Runs[index]); err != nil {
			return protocol.RunListPage{}, err
		}
	}
	if hasMore {
		last := page.Runs[len(page.Runs)-1]
		page.NextCursor = encodeRunCursor(admittedMillis[len(admittedMillis)-1], last.ID)
	}
	return page, nil
}

func (s *Store) applyRunListSummary(ctx context.Context, summary *protocol.RunListSummary) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT state, blocked_reason
		FROM sessions WHERE run_id = ? ORDER BY target_position, id
	`, summary.ID)
	if err != nil {
		return unavailable(err)
	}
	defer rows.Close()
	sessions := make([]protocol.Session, 0)
	for rows.Next() {
		var session protocol.Session
		var blockedReason sql.NullString
		if err := rows.Scan(&session.State, &blockedReason); err != nil {
			return unavailable(err)
		}
		session.BlockedReason = blockedReason.String
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return unavailable(err)
	}
	run := protocol.Run{TerminalAt: summary.TerminalAt}
	applyRunAggregate(&run, sessions, s.now())
	summary.State, summary.NeedsAttention = run.State, run.NeedsAttention
	summary.SessionCount, summary.SucceededCount = run.SessionCount, run.SucceededCount
	summary.ReadyCount, summary.NeedsInputCount = run.ReadyCount, run.NeedsInputCount
	summary.NoChangeCount, summary.FailedCount = run.NoChangeCount, run.FailedCount
	summary.CancelledCount, summary.ActiveCount = run.CancelledCount, run.ActiveCount
	return nil
}

func (s *Store) runListEntry(ctx context.Context, id string) (protocol.Run, error) {
	var run protocol.Run
	var snapshot, executionSnapshot, targetsSnapshot []byte
	var scheduledAt, terminalAt sql.NullInt64
	var admittedAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, task_snapshot, execution_snapshot, outcome_contract,
		       targets_snapshot, source, scheduled_at, admitted_at, updated_at, terminal_at
		FROM runs WHERE id = ?
	`, id).Scan(&run.ID, &run.TaskID, &snapshot, &executionSnapshot, &run.OutcomeContract,
		&targetsSnapshot, &run.Source,
		&scheduledAt, &admittedAt, &updatedAt, &terminalAt)
	if errors.Is(err, sql.ErrNoRows) {
		return run, ErrNotFound
	}
	if err != nil {
		return run, unavailable(err)
	}
	if err := json.Unmarshal(snapshot, &run.Task); err != nil {
		return run, unavailable(err)
	}
	if err := json.Unmarshal(executionSnapshot, &run.Execution); err != nil {
		return run, unavailable(err)
	}
	if err := json.Unmarshal(targetsSnapshot, &run.Targets); err != nil {
		return run, unavailable(err)
	}
	run.Task.Prompt, run.Task.TimeoutSeconds, run.Task.ConcurrencyLimit = "", 0, 0
	for index := range run.Task.Pipeline.Stages {
		run.Task.Pipeline.Stages[index].Prompt = ""
	}
	run.AdmittedAt, run.UpdatedAt = fromMillis(admittedAt), fromMillis(updatedAt)
	if scheduledAt.Valid {
		value := fromMillis(scheduledAt.Int64)
		run.ScheduledAt = &value
	}
	if terminalAt.Valid {
		value := fromMillis(terminalAt.Int64)
		run.TerminalAt = &value
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT repository_id, repository_identity, state, blocked_reason
		FROM sessions WHERE run_id = ? ORDER BY target_position, id
	`, id)
	if err != nil {
		return run, unavailable(err)
	}
	defer rows.Close()
	var sessions []protocol.Session
	seenRepositories := make(map[string]bool)
	rebuildRepositories := len(run.Task.Repositories) == 0
	for rows.Next() {
		var session protocol.Session
		var blockedReason sql.NullString
		if err := rows.Scan(&session.RepositoryID, &session.RepositoryIdentity, &session.State, &blockedReason); err != nil {
			return run, unavailable(err)
		}
		session.BlockedReason = blockedReason.String
		sessions = append(sessions, session)
		if rebuildRepositories && session.RepositoryID != "" && session.RepositoryIdentity != "" && !seenRepositories[session.RepositoryID] {
			seenRepositories[session.RepositoryID] = true
			run.Task.Repositories = append(run.Task.Repositories, protocol.TaskRepository{
				ID: session.RepositoryID, RemoteIdentity: session.RepositoryIdentity,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return run, unavailable(err)
	}
	applyRunAggregate(&run, sessions, s.now())
	return run, nil
}

func (s *Store) Run(ctx context.Context, id string) (protocol.RunDetail, error) {
	var detail protocol.RunDetail
	var snapshot, executionSnapshot, targetsSnapshot []byte
	var scheduledAt, terminalAt sql.NullInt64
	var providerSnapshot sql.NullString
	var admittedAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, task_snapshot, execution_snapshot, outcome_contract, targets_snapshot,
		       source, scheduled_at, provider_snapshot,
		       admitted_at, updated_at, terminal_at
		FROM runs run WHERE id = ?
	`, id).Scan(&detail.Run.ID, &detail.Run.TaskID, &snapshot, &executionSnapshot,
		&detail.Run.OutcomeContract, &targetsSnapshot, &detail.Run.Source,
		&scheduledAt, &providerSnapshot, &admittedAt, &updatedAt, &terminalAt)
	if errors.Is(err, sql.ErrNoRows) {
		return detail, ErrNotFound
	}
	if err != nil {
		return detail, unavailable(err)
	}
	if err := json.Unmarshal(snapshot, &detail.Run.Task); err != nil {
		return detail, unavailable(err)
	}
	if err := json.Unmarshal(executionSnapshot, &detail.Run.Execution); err != nil {
		return detail, unavailable(err)
	}
	if err := json.Unmarshal(targetsSnapshot, &detail.Run.Targets); err != nil {
		return detail, unavailable(err)
	}
	if providerSnapshot.Valid {
		detail.ProviderSnapshot = json.RawMessage(providerSnapshot.String)
	}
	detail.Run.AdmittedAt, detail.Run.UpdatedAt = fromMillis(admittedAt), fromMillis(updatedAt)
	if scheduledAt.Valid {
		value := fromMillis(scheduledAt.Int64)
		detail.Run.ScheduledAt = &value
	}
	if terminalAt.Valid {
		value := fromMillis(terminalAt.Int64)
		detail.Run.TerminalAt = &value
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT session.id, session.run_id, session.repository_id, session.repository_identity,
		       session.resolved_prompt, session.required_runtime, session.timeout_seconds,
		       session.execution_profile_id, session.execution_profile_version, session.execution_backend,
		       session.execution_provider, session.execution_model, session.resource_class, session.commit_resolution_policy,
		       session.state, session.blocked_reason, session.assigned_worker_id,
		       session.cancellation_requested, session.retry_may_repeat_effects,
		       session.admitted_at, session.started_at, session.terminal_at, session.result, session.failure_reason,
		       session.target_position, session.target_key, session.target_kind, session.source_kind,
		       session.source_key, session.source_reference, session.context_snapshot, session.publish_branch,
		       COALESCE(session.predecessor_work_id, ''), session.execution_owner, session.waiting_reason,
		       session.latest_progress, session.question, session.checkpoint_sha, session.pending_resume_sha,
		       session.pull_request_url, session.pull_request_head_branch, session.pull_request_head_sha,
		       session.terminal_message
		FROM sessions session WHERE session.run_id = ? ORDER BY session.target_position, session.id
	`, id)
	if err != nil {
		return detail, unavailable(err)
	}
	for rows.Next() {
		var session protocol.Session
		var blockedReason, workerID, result, failure sql.NullString
		var cancellation, retry int
		var admitted int64
		var started, terminal sql.NullInt64
		if err := rows.Scan(&session.ID, &session.RunID, &session.RepositoryID, &session.RepositoryIdentity,
			&session.ResolvedPrompt, &session.RequiredRuntime, &session.TimeoutSeconds,
			&session.Execution.ProfileID, &session.Execution.ProfileVersion, &session.Execution.Backend,
			&session.Execution.Provider, &session.Execution.Model, &session.Execution.ResourceClass,
			&session.Execution.CommitResolutionPolicy, &session.State,
			&blockedReason, &workerID, &cancellation, &retry, &admitted, &started, &terminal, &result, &failure,
			&session.Target.Position, &session.Target.TargetKey, &session.Target.TargetKind,
			&session.Target.SourceKind, &session.Target.SourceKey, &session.Target.SourceReference,
			&session.Target.ContextSnapshot, &session.Target.PublishBranch, &session.PredecessorWorkID,
			&session.ExecutionOwner, &session.WaitingReason, &session.LatestProgress, &session.Question,
			&session.CheckpointSHA, &session.PendingResumeSHA, &session.PullRequestURL,
			&session.PullRequestHeadBranch, &session.PullRequestHeadSHA, &session.TerminalMessage); err != nil {
			rows.Close()
			return detail, unavailable(err)
		}
		session.BlockedReason, session.AssignedWorkerID = blockedReason.String, workerID.String
		session.Execution.Runtime, session.Execution.TimeoutSeconds = session.RequiredRuntime, session.TimeoutSeconds
		session.CancellationRequested, session.RetryMayRepeatEffects = cancellation != 0, retry != 0
		session.Target.ID, session.Target.RepositoryID = session.ID, session.RepositoryID
		session.Target.RepositoryIdentity = session.RepositoryIdentity
		session.AdmittedAt, session.Result, session.FailureReason = fromMillis(admitted), result.String, failure.String
		if started.Valid {
			value := fromMillis(started.Int64)
			session.StartedAt = &value
		}
		if terminal.Valid {
			value := fromMillis(terminal.Int64)
			session.TerminalAt = &value
		}
		detail.Sessions = append(detail.Sessions, session)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return detail, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return detail, unavailable(err)
	}
	if len(detail.Run.Task.Repositories) == 0 {
		seen := make(map[string]bool, len(detail.Sessions))
		for _, session := range detail.Sessions {
			if session.RepositoryID == "" || session.RepositoryIdentity == "" || seen[session.RepositoryID] {
				continue
			}
			seen[session.RepositoryID] = true
			detail.Run.Task.Repositories = append(detail.Run.Task.Repositories, protocol.TaskRepository{
				ID: session.RepositoryID, RemoteIdentity: session.RepositoryIdentity,
			})
		}
	}
	for index := range detail.Sessions {
		session := &detail.Sessions[index]
		stageRows, err := s.db.QueryContext(ctx, `
			SELECT position, name, prompt, state, result, error, started_at, completed_at
			FROM session_stages WHERE session_id = ? ORDER BY position
		`, session.ID)
		if err != nil {
			return detail, unavailable(err)
		}
		for stageRows.Next() {
			stage, err := scanStageRun(stageRows)
			if err != nil {
				stageRows.Close()
				return detail, unavailable(err)
			}
			session.Stages = append(session.Stages, stage)
		}
		if err := stageRows.Err(); err != nil {
			stageRows.Close()
			return detail, unavailable(err)
		}
		if err := stageRows.Close(); err != nil {
			return detail, unavailable(err)
		}
		attemptRows, err := s.db.QueryContext(ctx, `
			SELECT attempt.id, attempt.execution_id, attempt.worker_id, attempt.attempt_number, attempt.state,
			       attempt.lease_expires_at, attempt.supervisor_pid, attempt.process_identity, attempt.process_group_id,
			       attempt.result, attempt.error, attempt.started_at, attempt.completed_at, attempt.created_at
			FROM attempts attempt JOIN executions execution ON execution.id = attempt.execution_id
			WHERE execution.session_id = ? ORDER BY attempt.attempt_number
		`, session.ID)
		if err != nil {
			return detail, unavailable(err)
		}
		for attemptRows.Next() {
			attempt, err := scanAttempt(attemptRows)
			if err != nil {
				attemptRows.Close()
				return detail, unavailable(err)
			}
			session.Attempts = append(session.Attempts, attempt)
		}
		if err := attemptRows.Err(); err != nil {
			attemptRows.Close()
			return detail, unavailable(err)
		}
		if err := attemptRows.Close(); err != nil {
			return detail, unavailable(err)
		}
	}
	applyRunAggregate(&detail.Run, detail.Sessions, s.now())
	return detail, nil
}

func (s *Store) RunSummary(ctx context.Context, id string) (protocol.RunSummary, error) {
	var summary protocol.RunSummary
	var admittedAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT run.id, json_extract(run.task_snapshot, '$.name'), run.source,
		       run.admitted_at, run.updated_at
		FROM runs run WHERE run.id = ?
	`, id).Scan(&summary.ID, &summary.TaskName, &summary.Source, &admittedAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return summary, ErrNotFound
	}
	if err != nil {
		return summary, unavailable(err)
	}
	summary.AdmittedAt, summary.UpdatedAt = fromMillis(admittedAt), fromMillis(updatedAt)
	rows, err := s.db.QueryContext(ctx, `
		SELECT session.id, session.repository_identity, session.state, session.blocked_reason,
		       session.assigned_worker_id, session.result, session.failure_reason,
		       (SELECT COUNT(*) FROM attempts attempt
		        JOIN executions execution ON execution.id = attempt.execution_id
		        WHERE execution.session_id = session.id)
		FROM sessions session
		WHERE session.run_id = ?
		ORDER BY session.target_position, session.id
	`, id)
	if err != nil {
		return summary, unavailable(err)
	}
	defer rows.Close()
	sessions := make([]protocol.Session, 0)
	for rows.Next() {
		var session protocol.RunSessionSummary
		var blockedReason, workerID, result, failure sql.NullString
		if err := rows.Scan(&session.ID, &session.RepositoryIdentity, &session.State,
			&blockedReason, &workerID, &result, &failure, &session.AttemptCount); err != nil {
			return summary, unavailable(err)
		}
		session.BlockedReason, session.AssignedWorkerID = blockedReason.String, workerID.String
		session.Result, session.FailureReason = result.String, failure.String
		summary.Sessions = append(summary.Sessions, session)
		sessions = append(sessions, protocol.Session{State: session.State, BlockedReason: session.BlockedReason})
	}
	if err := rows.Err(); err != nil {
		return summary, unavailable(err)
	}
	run := protocol.Run{}
	applyRunAggregate(&run, sessions, s.now())
	summary.State = run.State
	return summary, nil
}

func applyRunAggregate(run *protocol.Run, sessions []protocol.Session, now time.Time) {
	run.SessionCount = len(sessions)
	run.SucceededCount, run.ReadyCount, run.NeedsInputCount, run.NoChangeCount = 0, 0, 0, 0
	run.FailedCount, run.CancelledCount, run.ActiveCount = 0, 0, 0
	blocked, actionableBlocked, queued, running := 0, 0, 0, 0
	for _, session := range sessions {
		switch session.State {
		case protocol.SessionBlocked:
			blocked++
			if session.BlockedReason != taskConcurrencyBlockedReason {
				actionableBlocked++
			}
		case protocol.SessionQueued:
			queued++
		case protocol.SessionPreparing, protocol.SessionRunning:
			running++
		case protocol.SessionNeedsInput:
			blocked++
			actionableBlocked++
			run.NeedsInputCount++
		case protocol.SessionReady:
			run.ReadyCount++
		case protocol.SessionSucceeded:
			run.SucceededCount++
		case protocol.SessionFailed:
			run.FailedCount++
		case protocol.SessionNoChange:
			run.NoChangeCount++
		case protocol.SessionCancelled:
			run.CancelledCount++
		}
	}
	run.ActiveCount = blocked + queued + running
	if run.ActiveCount > 0 {
		switch {
		case running > 0 || run.SucceededCount+run.ReadyCount+run.NoChangeCount+
			run.FailedCount+run.CancelledCount > 0:
			run.State = protocol.RunRunning
		case blocked == run.ActiveCount:
			run.State = protocol.RunBlocked
		default:
			run.State = protocol.RunQueued
		}
	} else {
		successful := run.SucceededCount + run.ReadyCount + run.NoChangeCount
		switch {
		case successful == run.SessionCount:
			run.State = protocol.RunSucceeded
		case successful == 0 && run.FailedCount > 0:
			run.State = protocol.RunFailed
		case run.CancelledCount == run.SessionCount:
			run.State = protocol.RunCancelled
		default:
			run.State = protocol.RunPartial
		}
	}
	run.NeedsAttention = actionableBlocked > 0 || ((run.State == protocol.RunFailed || run.State == protocol.RunPartial) &&
		run.TerminalAt != nil && run.TerminalAt.After(now.Add(-24*time.Hour)))
}

func (s *Store) CancelRun(ctx context.Context, runID string) (protocol.RunDetail, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs run WHERE id = ?`, runID).Scan(&exists); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if exists == 0 {
		return protocol.RunDetail{}, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET
			state = CASE WHEN state IN ('blocked','queued','needs-input') OR execution_owner = 'operator' THEN 'cancelled' ELSE state END,
			cancellation_requested = CASE WHEN state IN ('preparing','running') THEN 1 ELSE cancellation_requested END,
			terminal_at = CASE WHEN state IN ('blocked','queued','needs-input') OR execution_owner = 'operator' THEN ? ELSE terminal_at END,
			execution_owner = CASE WHEN state IN ('blocked','queued','needs-input') OR execution_owner = 'operator' THEN 'none' ELSE execution_owner END,
			terminal_message = CASE WHEN state IN ('blocked','queued','needs-input') OR execution_owner = 'operator' THEN 'Cancelled by operator.' ELSE terminal_message END
		WHERE run_id = ? AND state IN ('blocked','queued','preparing','running','needs-input')
	`, now, runID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE executions SET
			state = CASE WHEN state = 'queued' THEN 'cancelled' ELSE state END,
			cancellation_requested = CASE WHEN state IN ('preparing','running') THEN 1 ELSE cancellation_requested END,
			updated_at = ?
		WHERE session_id IN (SELECT id FROM sessions WHERE run_id = ?)
		  AND state IN ('queued','preparing','running')
	`, now, runID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = 'cancelled', completed_at = ?
		WHERE session_id IN (SELECT id FROM sessions WHERE run_id = ? AND state = 'cancelled')
		  AND state IN ('pending', 'running')
	`, now, runID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET updated_at = ?, terminal_at = CASE
			WHEN NOT EXISTS (
				SELECT 1 FROM sessions session WHERE session.run_id = runs.id
				  AND session.state IN ('blocked','queued','preparing','running','needs-input')
			) THEN ? ELSE NULL END
		WHERE id = ?
	`, now, now, runID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	return s.Run(ctx, runID)
}

func (s *Store) CancelSession(ctx context.Context, runID, sessionID string) (protocol.RunDetail, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `
		SELECT state FROM sessions WHERE id = ? AND run_id = ?
	`, sessionID, runID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return protocol.RunDetail{}, ErrNotFound
	} else if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if state != "blocked" && state != "queued" && state != "preparing" && state != "running" && state != "needs-input" {
		return protocol.RunDetail{}, conflict("session_cancel_not_allowed", "only active Sessions can be cancelled")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET
			state = CASE WHEN state IN ('blocked','queued','needs-input') OR execution_owner = 'operator' THEN 'cancelled' ELSE state END,
			cancellation_requested = CASE WHEN state IN ('preparing','running') THEN 1 ELSE cancellation_requested END,
			terminal_at = CASE WHEN state IN ('blocked','queued','needs-input') OR execution_owner = 'operator' THEN ? ELSE terminal_at END,
			execution_owner = CASE WHEN state IN ('blocked','queued','needs-input') OR execution_owner = 'operator' THEN 'none' ELSE execution_owner END,
			terminal_message = CASE WHEN state IN ('blocked','queued','needs-input') OR execution_owner = 'operator' THEN 'Cancelled by operator.' ELSE terminal_message END
		WHERE id = ? AND run_id = ?
	`, now, sessionID, runID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE executions SET
			state = CASE WHEN state = 'queued' THEN 'cancelled' ELSE state END,
			cancellation_requested = CASE WHEN state IN ('preparing','running') THEN 1 ELSE cancellation_requested END,
			updated_at = ?
		WHERE session_id = ? AND state IN ('queued','preparing','running')
	`, now, sessionID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = 'cancelled', completed_at = ?
		WHERE session_id = ? AND EXISTS (SELECT 1 FROM sessions WHERE id = ? AND state = 'cancelled')
		  AND state IN ('pending', 'running')
	`, now, sessionID, sessionID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET updated_at = ?, terminal_at = CASE
			WHEN NOT EXISTS (
				SELECT 1 FROM sessions session WHERE session.run_id = runs.id
				  AND session.state IN ('blocked','queued','preparing','running','needs-input')
			) THEN ? ELSE NULL END
		WHERE id = ?
	`, now, now, runID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	return s.Run(ctx, runID)
}

func (s *Store) RetrySession(ctx context.Context, expectedRunID, sessionID string) (protocol.RunDetail, error) {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	defer tx.Rollback()
	var runID, state, repositoryID, identity, runtime, backend, profileID string
	var targetKind, sourceKind, sourceKey string
	var owner protocol.ExecutionOwner
	var profileVersion int
	err = tx.QueryRowContext(ctx, `
		SELECT run_id, state, repository_id, repository_identity, required_runtime,
		       execution_backend, execution_profile_id, execution_profile_version,
		       target_kind, source_kind, source_key, execution_owner
		FROM sessions WHERE id = ? AND run_id = ?
	`, sessionID, expectedRunID).Scan(&runID, &state, &repositoryID, &identity, &runtime,
		&backend, &profileID, &profileVersion, &targetKind, &sourceKind, &sourceKey, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.RunDetail{}, ErrNotFound
	}
	if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if state != "failed" && state != "cancelled" {
		return protocol.RunDetail{}, conflict("session_retry_not_allowed", "only failed or cancelled Sessions can be retried")
	}
	if owner != protocol.ExecutionOwnerNone {
		return protocol.RunDetail{}, conflict("work_owned", "owned Work cannot be retried")
	}
	if err := validateWorkRetryGuards(ctx, tx, sessionID, repositoryID, targetKind, sourceKind, sourceKey); err != nil {
		return protocol.RunDetail{}, err
	}
	var activeSessions, concurrencyLimit int
	if err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM sessions active
			 WHERE active.run_id = run.id AND active.state IN ('queued','preparing','running')),
			json_extract(run.task_snapshot, '$.concurrency_limit')
		FROM runs run WHERE id = ?
	`, runID).Scan(&activeSessions, &concurrencyLimit); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if activeSessions >= concurrencyLimit {
		return protocol.RunDetail{}, conflict("task_concurrency_full", "retry this Session after another active Session finishes")
	}
	assignedWorkerID := ""
	if backend == protocol.BackendPersistent {
		selection, err := s.selectSessionRoute(ctx, tx, repositoryID, identity, now, "", runtime)
		if err != nil {
			return protocol.RunDetail{}, err
		}
		assignedWorkerID = selection.workerID
	} else {
		var repositoryAvailable int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM repositories repository
				WHERE repository.id = ? AND repository.remote_identity = ?
				  AND (repository.centrally_managed = 0 OR repository.enabled = 1)
			)
		`, repositoryID, identity).Scan(&repositoryAvailable); err != nil {
			return protocol.RunDetail{}, unavailable(err)
		}
		if repositoryAvailable == 0 {
			return protocol.RunDetail{}, conflict(
				"repository_not_available",
				"the frozen repository is disabled, unavailable, or no longer matches its admitted identity",
			)
		}
		var available int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM execution_profile_versions version
				JOIN execution_profiles profile ON profile.id = version.profile_id
				JOIN workers worker ON worker.id = ? AND worker.synthetic = 1
				WHERE version.profile_id = ? AND version.version = ?
				  AND profile.enabled = 1 AND profile.healthy = 1
			)
		`, syntheticWorkerID(profileID), profileID, profileVersion).Scan(&available); err != nil {
			return protocol.RunDetail{}, unavailable(err)
		}
		if available == 0 {
			return protocol.RunDetail{}, conflict("execution_profile_version_unavailable", "the frozen execution profile version is unavailable")
		}
		assignedWorkerID = syntheticWorkerID(profileID)
	}
	var executionID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM executions WHERE session_id = ?`, sessionID).Scan(&executionID)
	if errors.Is(err, sql.ErrNoRows) {
		executionID, err = newID()
		if err != nil {
			return protocol.RunDetail{}, unavailable(err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO executions(id, session_id, assigned_worker_id, required_runtime, state,
			                       cancellation_requested, created_at, updated_at, retry_count)
			VALUES (?, ?, ?, ?, 'queued', 0, ?, ?, 1)
		`, executionID, sessionID, assignedWorkerID, runtime, now, now)
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `
			UPDATE executions SET assigned_worker_id = ?, state = 'queued', cancellation_requested = 0,
			       retry_count = retry_count + 1, updated_at = ? WHERE id = ?
		`, assignedWorkerID, now, executionID)
	}
	if err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE sessions SET state = 'queued', blocked_reason = NULL, assigned_worker_id = ?,
		       cancellation_requested = 0, retry_may_repeat_effects = 1,
		       started_at = NULL, terminal_at = NULL, result = NULL, failure_reason = NULL,
		       terminal_message = '', waiting_reason = '', execution_owner = 'none'
		WHERE id = ?
	`, assignedWorkerID, sessionID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_stages SET state = 'pending', result = '', error = '', started_at = NULL, completed_at = NULL
		WHERE session_id = ?
	`, sessionID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET updated_at = ?, terminal_at = NULL WHERE id = ?`, now, runID); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.RunDetail{}, unavailable(err)
	}
	return s.Run(ctx, runID)
}

func (s *Store) Overview(ctx context.Context) (protocol.Overview, error) {
	page, err := s.RunPage(ctx, 10, "")
	if err != nil {
		return protocol.Overview{}, err
	}
	result := protocol.Overview{
		GeneratedAt: s.now().UTC(),
		RunMetrics:  protocol.OverviewRunMetrics{Window: "24h"},
	}
	result.RecentRuns = page.Runs
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(EXISTS (
				SELECT 1 FROM sessions session
				WHERE session.run_id = run.id AND session.state IN ('blocked','queued','preparing','running','needs-input')
			)), 0),
			COALESCE(SUM(
				(terminal_at IS NULL AND EXISTS (
					SELECT 1 FROM sessions session
					WHERE session.run_id = run.id AND (
					  session.state = 'needs-input' OR (
					    session.state = 'blocked' AND COALESCE(session.blocked_reason, '') != ?
					  )
					)
				)) OR
				(terminal_at >= ? AND EXISTS (
					SELECT 1 FROM sessions session
					WHERE session.run_id = run.id AND session.state = 'failed'
				))
			), 0),
			COALESCE(SUM(terminal_at >= ?), 0)
		FROM runs run
	`, taskConcurrencyBlockedReason, result.GeneratedAt.Add(-24*time.Hour).UnixMilli(),
		result.GeneratedAt.Add(-24*time.Hour).UnixMilli()).Scan(
		&result.ActiveRuns, &result.NeedsAttention, &result.CompletedLast24H); err != nil {
		return result, unavailable(err)
	}
	var averageQueueMillis, averageCycleMillis sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `
		WITH recent_runs AS (
			SELECT run.id, run.admitted_at, run.terminal_at,
			       (SELECT MIN(attempt.started_at)
			        FROM sessions session
			        JOIN executions execution ON execution.session_id = session.id
			        JOIN attempts attempt ON attempt.execution_id = execution.id
			        WHERE session.run_id = run.id AND attempt.started_at IS NOT NULL) AS first_started_at
			FROM runs run
			WHERE run.admitted_at >= ? AND run.admitted_at <= ?
		)
		SELECT COUNT(*),
		       COALESCE(SUM(terminal_at IS NOT NULL), 0),
		       AVG(CASE WHEN first_started_at IS NOT NULL
		                THEN MAX(0, first_started_at - admitted_at) END),
		       AVG(CASE WHEN terminal_at IS NOT NULL
		                THEN MAX(0, terminal_at - admitted_at) END)
		FROM recent_runs
	`, result.GeneratedAt.Add(-24*time.Hour).UnixMilli(), result.GeneratedAt.UnixMilli()).Scan(
		&result.RunMetrics.TotalRuns, &result.RunMetrics.CompletedRuns,
		&averageQueueMillis, &averageCycleMillis); err != nil {
		return result, unavailable(err)
	}
	if result.RunMetrics.TotalRuns > 0 {
		completionRate := float64(result.RunMetrics.CompletedRuns) / float64(result.RunMetrics.TotalRuns)
		result.RunMetrics.CompletionRate = &completionRate
	}
	if averageQueueMillis.Valid {
		averageQueueSeconds := averageQueueMillis.Float64 / float64(time.Second/time.Millisecond)
		result.RunMetrics.AverageQueueTimeSeconds = &averageQueueSeconds
	}
	if averageCycleMillis.Valid {
		averageCycleSeconds := averageCycleMillis.Float64 / float64(time.Second/time.Millisecond)
		result.RunMetrics.AverageCycleTimeSeconds = &averageCycleSeconds
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			(synthetic = 1 AND health = 'healthy') OR
			(synthetic = 0 AND last_heartbeat >= ?)
		), 0), COUNT(*) FROM workers
	`, result.GeneratedAt.Add(-protocol.WorkerOnlineWindow).UnixMilli()).Scan(&result.WorkersOnline, &result.WorkersTotal); err != nil {
		return result, unavailable(err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM tasks
		WHERE migration_only = 0 AND archived = 0 AND schedule_enabled = 1 AND next_due_at IS NOT NULL
		ORDER BY next_due_at, id LIMIT 5
	`)
	if err != nil {
		return result, unavailable(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return result, unavailable(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return result, unavailable(err)
	}
	for _, id := range ids {
		task, err := s.Task(ctx, id)
		if err != nil {
			return result, err
		}
		result.UpcomingTasks = append(result.UpcomingTasks, taskSummary(task))
	}
	return result, nil
}
