package controlplane

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
	"github.com/owainlewis/factory/migrations"
)

func TestTasksMigrationPreservesPopulatedLegacyHistory(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBeforeTasks(t, ctx, store)

	_, err = db.ExecContext(ctx, legacyTasksFixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE runs SET request_key = 'shared-legacy-key' WHERE id = 'run-scheduled';
		UPDATE tasks SET request_key = 'shared-legacy-key' WHERE id = 'task-webhook';
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var taskCount, distinctNames int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT name_key) FROM tasks WHERE migration_only = 0`).
		Scan(&taskCount, &distinctNames); err != nil {
		t.Fatal(err)
	}
	if taskCount != 4 || distinctNames != 4 {
		t.Fatalf("operator Tasks = %d, distinct names = %d", taskCount, distinctNames)
	}
	var incompatibleTasks, incompatibleRuns int
	if err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM tasks WHERE outcome_contract != 'process_exit'),
			(SELECT COUNT(*) FROM runs
			 WHERE outcome_contract != 'process_exit'
			    OR json_extract(task_snapshot, '$.outcome_contract') != 'process_exit')
	`).Scan(&incompatibleTasks, &incompatibleRuns); err != nil {
		t.Fatal(err)
	}
	if incompatibleTasks != 0 || incompatibleRuns != 0 {
		t.Fatalf("legacy outcome contracts changed: Tasks %d, Runs %d", incompatibleTasks, incompatibleRuns)
	}
	var taskToolColumns, sessionToolColumns int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name = 'allowed_tools'`).
		Scan(&taskToolColumns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'allowed_tools'`).
		Scan(&sessionToolColumns); err != nil {
		t.Fatal(err)
	}
	if taskToolColumns != 0 || sessionToolColumns != 0 {
		t.Fatalf("tool configuration survived in product tables: tasks %d, sessions %d",
			taskToolColumns, sessionToolColumns)
	}
	var migratedTaskKey, originalTaskKey string
	if err := db.QueryRowContext(ctx, `
		SELECT request_key, json_extract(task_snapshot, '$.legacy_task_request_key')
		FROM runs run WHERE id = 'task-webhook'
	`).Scan(&migratedTaskKey, &originalTaskKey); err != nil {
		t.Fatal(err)
	}
	if migratedTaskKey != "legacy-task:task-webhook" || originalTaskKey != "shared-legacy-key" {
		t.Fatalf("migrated standalone Task keys = %q, original %q", migratedTaskKey, originalTaskKey)
	}
	var migratedRunKey, originalRunKey string
	if err := db.QueryRowContext(ctx, `
		SELECT request_key, json_extract(task_snapshot, '$.legacy_run_request_key')
		FROM runs run WHERE id = 'run-scheduled'
	`).Scan(&migratedRunKey, &originalRunKey); err != nil {
		t.Fatal(err)
	}
	if migratedRunKey != "legacy-run:run-scheduled" || originalRunKey != "shared-legacy-key" {
		t.Fatalf("migrated Run keys = %q, original %q", migratedRunKey, originalRunKey)
	}
	var currentPrompt, historicalPrompt string
	var currentArchived, currentReadOnly, historicalArchived, historicalReadOnly bool
	if err := db.QueryRowContext(ctx, `SELECT prompt, archived, read_only FROM tasks WHERE id = 'revision-2'`).
		Scan(&currentPrompt, &currentArchived, &currentReadOnly); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT prompt, archived, read_only FROM tasks WHERE id = 'revision-1'`).
		Scan(&historicalPrompt, &historicalArchived, &historicalReadOnly); err != nil {
		t.Fatal(err)
	}
	if currentPrompt != "Runbook instructions:\n\nCurrent instructions\n\nRunbook summary:\n\nCurrent summary" ||
		currentArchived || currentReadOnly ||
		historicalPrompt != "Runbook instructions:\n\nInstructions\n\nRunbook summary:\n\nSummary" ||
		!historicalArchived || !historicalReadOnly {
		t.Fatalf("migrated Runbook revisions = current (%q, archived %v, read-only %v), historical (%q, archived %v, read-only %v)",
			currentPrompt, currentArchived, currentReadOnly,
			historicalPrompt, historicalArchived, historicalReadOnly)
	}
	update := protocol.SaveTaskRequest{
		Name: "Changed", Prompt: "Changed", Runtime: "codex", TimeoutSeconds: 7200,
		ConcurrencyLimit: 10, ExpectedGeneration: 1,
	}
	if _, err := store.UpdateTask(ctx, "revision-1", update); !serviceErrorCode(err, "task_read_only") {
		t.Fatalf("historical revision update error = %v", err)
	}
	if _, err := store.SetTaskArchived(ctx, "revision-1", protocol.SetTaskArchivedRequest{
		Archived: boolPointer(false), ExpectedGeneration: 1,
	}); !serviceErrorCode(err, "task_read_only") {
		t.Fatalf("historical revision restore error = %v", err)
	}
	if _, _, err := store.RunTask(ctx, "revision-1", protocol.RunTaskRequest{
		RequestKey: "historical-revision-run",
	}); !serviceErrorCode(err, "task_read_only") {
		t.Fatalf("historical revision run error = %v", err)
	}

	var issueState, issueLabels, pullState, pullLabels, pullBranches string
	var includeDrafts bool
	if err := db.QueryRowContext(ctx, `
		SELECT
			json_extract(provider_snapshot, '$.issue.configured_state'),
			json_extract(provider_snapshot, '$.issue.required_labels[0]')
		FROM runs run WHERE id = 'task-issue'
	`).Scan(&issueState, &issueLabels); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT
			json_extract(provider_snapshot, '$.pull_request.configured_state'),
			json_extract(provider_snapshot, '$.pull_request.required_labels[0]'),
			json_extract(provider_snapshot, '$.pull_request.base_branches[0]'),
			json_extract(provider_snapshot, '$.pull_request.include_drafts')
		FROM runs run WHERE id = 'task-pr'
	`).Scan(&pullState, &pullLabels, &pullBranches, &includeDrafts); err != nil {
		t.Fatal(err)
	}
	if issueState != "open" || issueLabels != "ready" || pullState != "open" ||
		pullLabels != "review" || pullBranches != "main" || !includeDrafts {
		t.Fatalf("provider snapshots lost matching criteria: issue=(%q,%q), pull=(%q,%q,%q,%v)",
			issueState, issueLabels, pullState, pullLabels, pullBranches, includeDrafts)
	}
	var webhookTitle, webhookBranch, webhookDefinition, webhookParameter string
	if err := db.QueryRowContext(ctx, `
		SELECT
			json_extract(provider_snapshot, '$.webhook.pull_request_title'),
			json_extract(provider_snapshot, '$.webhook.base_branch'),
			json_extract(provider_snapshot, '$.webhook.definition_id'),
			json_extract(provider_snapshot, '$.webhook.parameters.priority')
		FROM runs run WHERE id = 'task-webhook'
	`).Scan(&webhookTitle, &webhookBranch, &webhookDefinition, &webhookParameter); err != nil {
		t.Fatal(err)
	}
	if webhookTitle != "Webhook PR" || webhookBranch != "main" ||
		webhookDefinition != "definition-1" || webhookParameter != "high" {
		t.Fatalf("webhook snapshot = %q, %q, %q, %q", webhookTitle, webhookBranch, webhookDefinition, webhookParameter)
	}

	var historyTask, workflowID, workflowRevision, workflowTitle string
	if err := db.QueryRowContext(ctx, `
		SELECT task_id,
			json_extract(task_snapshot, '$.legacy_workflow_id'),
			json_extract(task_snapshot, '$.legacy_workflow_revision_id'),
			json_extract(task_snapshot, '$.legacy_workflow_title')
		FROM runs run WHERE id = 'task-workflow'
	`).Scan(&historyTask, &workflowID, &workflowRevision, &workflowTitle); err != nil {
		t.Fatal(err)
	}
	if historyTask != "00000000-0000-4000-8000-000000000103" || workflowID != "workflow-1" ||
		workflowRevision != "revision-1" || workflowTitle != "Shared" {
		t.Fatalf("workflow history = %q, %q, %q, %q", historyTask, workflowID, workflowRevision, workflowTitle)
	}

	var pendingDue int64
	var pendingRepository string
	var retryAt sql.NullInt64
	var scheduleHealth string
	if err := db.QueryRowContext(ctx, `
		SELECT pending_due_at, json_extract(pending_snapshot_json, '$.repository_ids[0]'),
			schedule_retry_at, schedule_health_status
		FROM tasks WHERE id = 'automation-schedule'
	`).Scan(&pendingDue, &pendingRepository, &retryAt, &scheduleHealth); err != nil {
		t.Fatal(err)
	}
	if pendingDue != 2000 || pendingRepository != "repo-1" || retryAt.Valid || scheduleHealth != "blocked" {
		t.Fatalf("pending schedule = %d, %q, retry %v, health %q", pendingDue, pendingRepository, retryAt, scheduleHealth)
	}
	var scheduledAt int64
	var scheduleOccurrence, scheduleKind, scheduleCron, scheduleTimezone, legacyTool string
	if err := db.QueryRowContext(ctx, `
		SELECT scheduled_at,
			json_extract(task_snapshot, '$.legacy_schedule_occurrence_id'),
			json_extract(task_snapshot, '$.legacy_schedule_kind'),
			json_extract(task_snapshot, '$.cron'),
			json_extract(task_snapshot, '$.timezone'),
			json_extract(task_snapshot, '$.legacy_allowed_tools[0]')
		FROM runs run WHERE id = 'run-scheduled'
	`).Scan(&scheduledAt, &scheduleOccurrence, &scheduleKind, &scheduleCron, &scheduleTimezone, &legacyTool); err != nil {
		t.Fatal(err)
	}
	if scheduledAt != 1500 || scheduleOccurrence != "occurrence-schedule-admitted" ||
		scheduleKind != "scheduled" || scheduleCron != "0 9 * * *" || scheduleTimezone != "UTC" || legacyTool != "git" {
		t.Fatalf("admitted schedule snapshot = %d, %q, %q, %q, %q, tool %q",
			scheduledAt, scheduleOccurrence, scheduleKind, scheduleCron, scheduleTimezone, legacyTool)
	}
	migratedScheduled, err := store.Run(ctx, "run-scheduled")
	if err != nil {
		t.Fatal(err)
	}
	if migratedScheduled.Run.OutcomeContract != protocol.OutcomeProcessExit ||
		len(migratedScheduled.Run.Targets) != len(migratedScheduled.Sessions) ||
		migratedScheduled.Run.State != protocol.RunSucceeded ||
		migratedScheduled.Sessions[0].State != protocol.SessionSucceeded ||
		len(migratedScheduled.Sessions[0].Attempts) != 1 ||
		migratedScheduled.Sessions[0].Attempts[0].State != "succeeded" {
		t.Fatalf("migrated scheduled process-exit Run = %#v", migratedScheduled)
	}
	legacyWorker, err := store.Worker(ctx, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	var scheduledRepositoryIdentity string
	if err := db.QueryRowContext(ctx, `SELECT remote_identity FROM repositories WHERE id = 'repo-1'`).
		Scan(&scheduledRepositoryIdentity); err != nil {
		t.Fatal(err)
	}
	repositories := []protocol.RepositoryRegistration{{
		Key: "factory", RemoteIdentity: scheduledRepositoryIdentity,
	}}
	if _, err := store.RegisterWorker(ctx, legacyWorker.ID, protocol.WorkerRegistration{
		Name: legacyWorker.Name, Labels: legacyWorker.Labels, WorkerVersion: "test-v3",
		ClaimProtocolVersion: protocol.ClaimProtocolVersion, Runtime: legacyWorker.Runtime,
		RuntimeVersion: "codex-test-v3", Capacity: legacyWorker.Capacity,
		ActiveCount: 0, Health: "healthy", Repositories: repositories,
		RetainedWorktrees: legacyWorker.RetainedWorktrees,
	}); err != nil {
		t.Fatalf("re-register migrated Worker: %v", err)
	}
	var pendingSnapshot []byte
	if err := db.QueryRowContext(ctx, `
		SELECT pending_snapshot_json FROM tasks WHERE id = 'automation-schedule'
	`).Scan(&pendingSnapshot); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	frozenPending, err := loadPendingTaskSnapshot(ctx, tx, "automation-schedule", pendingSnapshot)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	pendingAt := fromMillis(pendingDue)
	nextScheduled, created, err := store.admitTask(
		ctx,
		"automation-schedule",
		"schedule",
		fmt.Sprintf("schedule:automation-schedule:%d:%d", frozenPending.Generation, pendingDue),
		&pendingAt,
		&frozenPending,
		"",
	)
	if err != nil || !created {
		t.Fatalf("admit migrated pending schedule = %#v, created %v, err %v", nextScheduled, created, err)
	}
	if err := store.finishTaskOccurrence(ctx, "automation-schedule", pendingAt, true, nil); err != nil {
		t.Fatalf("finish migrated pending occurrence: %v", err)
	}
	claim, err := store.Claim(ctx, legacyWorker.ID, protocol.ClaimRequest{
		RequestID: "migrated-next-schedule", LeaseToken: tokenA,
	})
	if err != nil || claim == nil || claim.Session.RunID != nextScheduled.Run.ID {
		t.Fatalf("claim migrated next schedule = %#v, err %v", claim, err)
	}
	if claim.Session.OutcomeContract != protocol.OutcomeProcessExit {
		t.Fatalf("migrated pending claim contract = %q", claim.Session.OutcomeContract)
	}
	if _, err := store.StartAttempt(ctx, claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "next legacy schedule completed",
	}); err != nil {
		t.Fatal(err)
	}
	nextScheduled, err = store.Run(ctx, nextScheduled.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if nextScheduled.Run.OutcomeContract != protocol.OutcomeProcessExit ||
		nextScheduled.Run.State != protocol.RunSucceeded ||
		len(nextScheduled.Sessions) != 1 || nextScheduled.Sessions[0].State != protocol.SessionSucceeded ||
		nextScheduled.Sessions[0].Result != "next legacy schedule completed" {
		t.Fatalf("completed migrated next schedule = %#v", nextScheduled)
	}
	throttled, err := store.Run(ctx, "run-throttled")
	if err != nil {
		t.Fatal(err)
	}
	if throttled.Run.NeedsAttention || len(throttled.Sessions) != 1 ||
		throttled.Sessions[0].BlockedReason != taskConcurrencyBlockedReason {
		t.Fatalf("migrated concurrency throttle = %#v", throttled)
	}
	overview, err := store.Overview(ctx)
	if err != nil || overview.ActiveRuns != 1 || overview.NeedsAttention != 0 {
		t.Fatalf("migrated throttle Overview = %#v, err %v", overview, err)
	}

	var sessionID, eventPayload, retained string
	if err := db.QueryRowContext(ctx, `SELECT session_id FROM executions WHERE id = 'execution-issue'`).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT CAST(payload AS TEXT) FROM attempt_events WHERE attempt_id = 'attempt-issue'`).Scan(&eventPayload); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT retained_worktrees_json FROM workers WHERE id = 'worker-1'`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if sessionID != "task-issue" || eventPayload != `{"message":"kept"}` || !strings.Contains(retained, "attempt-issue") {
		t.Fatalf("lifecycle preservation = session %q, event %q, retained %q", sessionID, eventPayload, retained)
	}

	var violations int
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		violations++
	}
	if violations != 0 {
		t.Fatalf("foreign key violations = %d", violations)
	}
}

func TestTasksMigrationDoesNotCountReadOnlyRevisionHistoryTowardTaskLimit(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBeforeTasks(t, ctx, store)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for workflowNumber := 1; workflowNumber <= 6; workflowNumber++ {
		workflowID := fmt.Sprintf("workflow-%d", workflowNumber)
		currentRevisionID := fmt.Sprintf("revision-%d-100", workflowNumber)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workflows(id, enabled, current_revision_id, current_title_key, created_at, updated_at)
			VALUES (?, 1, ?, ?, 1, 100)
		`, workflowID, currentRevisionID, workflowID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		for revisionNumber := 1; revisionNumber <= 100; revisionNumber++ {
			revisionID := fmt.Sprintf("revision-%d-%d", workflowNumber, revisionNumber)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO workflow_revisions(
					id, workflow_id, revision_number, request_key, request_digest,
					title, summary, instructions, created_at
				) VALUES (?, ?, ?, ?, X'01', ?, '', ?, ?)
			`, revisionID, workflowID, revisionNumber, "request-"+revisionID,
				fmt.Sprintf("Workflow %d", workflowNumber),
				fmt.Sprintf("Instructions %d", revisionNumber), revisionNumber); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var total, editable, historical int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), SUM(read_only = 0), SUM(read_only = 1)
		FROM tasks WHERE migration_only = 0
	`).Scan(&total, &editable, &historical); err != nil {
		t.Fatal(err)
	}
	if total != 600 || editable != 6 || historical != 594 {
		t.Fatalf("migrated revisions = total %d, editable %d, historical %d", total, editable, historical)
	}
}

func TestTasksMigrationNormalizesUnicodeNameKeys(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBeforeTasks(t, ctx, store)
	if _, err := db.ExecContext(ctx, legacyTasksFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE definitions SET name = 'Éclair' WHERE id = 'definition-1';
		UPDATE automations SET title = 'Étude' WHERE id = 'automation-schedule';
		UPDATE workflow_revisions SET title = 'Übung' WHERE workflow_id = 'workflow-1';
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, `SELECT name, name_key FROM tasks WHERE migration_only = 0`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, key string
		if err := rows.Scan(&name, &key); err != nil {
			t.Fatal(err)
		}
		if want := normalizeTitleKey(name); key != want {
			t.Fatalf("migrated Task %q key = %q, want %q", name, key, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var rewrittenHistoryKeys int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks
		WHERE migration_only = 1 AND name_key NOT GLOB '__migration_*_history__'
	`).Scan(&rewrittenHistoryKeys); err != nil {
		t.Fatal(err)
	}
	if rewrittenHistoryKeys != 0 {
		t.Fatalf("rewrote %d reserved migration history keys", rewrittenHistoryKeys)
	}
	if _, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "éCLAIR · definition 1", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	}); !serviceErrorCode(err, "task_name_conflict") {
		t.Fatalf("migrated Unicode Task name conflict error = %v", err)
	}
}

func TestTasksMigrationKeepsArchivedDefinitionSchedulesDisabled(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBeforeTasks(t, ctx, store)
	if _, err := db.ExecContext(ctx, legacyTasksFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE definitions SET archived = 1 WHERE id = 'definition-1'`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var archived, scheduleEnabled bool
	var cron, timezone, healthCode, healthMessage string
	var nextDue sql.NullInt64
	var pendingDue int64
	if err := db.QueryRowContext(ctx, `
		SELECT archived, schedule_enabled, cron, timezone, next_due_at, pending_due_at,
			schedule_health_code, schedule_health_message
		FROM tasks WHERE id = 'automation-schedule'
	`).Scan(&archived, &scheduleEnabled, &cron, &timezone, &nextDue, &pendingDue, &healthCode, &healthMessage); err != nil {
		t.Fatal(err)
	}
	if !archived || scheduleEnabled || cron != "0 9 * * *" || timezone != "UTC" ||
		nextDue.Valid || pendingDue != 2000 || healthCode != "source_archived" ||
		!strings.Contains(healthMessage, "source prompt") || !strings.Contains(healthMessage, "repository unavailable") {
		t.Fatalf("archived-source schedule = archived %v, enabled %v, cron %q, timezone %q, next %v, pending %d, health %q %q",
			archived, scheduleEnabled, cron, timezone, nextDue, pendingDue, healthCode, healthMessage)
	}
}

func TestTasksMigrationBlocksInvalidLegacySchedulePrompts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, context.Context, *sql.DB)
	}{
		{
			name: "override removed from current Definition",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB) {
				t.Helper()
				if _, err := db.ExecContext(ctx, `
					UPDATE automation_schedule_triggers
					SET parameters_json = '{"removed":"value"}'
					WHERE automation_id = 'automation-schedule'
				`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "schedule-specific resolved prompt over limit",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB) {
				t.Helper()
				if _, err := db.ExecContext(ctx, `
					UPDATE definitions SET prompt = ?, inputs = '{"scope":""}'
					WHERE id = 'definition-1'
				`, strings.Repeat("x", 65000)); err != nil {
					t.Fatal(err)
				}
				parameters := `{"scope":"` + strings.Repeat("y", 1000) + `"}`
				if _, err := db.ExecContext(ctx, `
					UPDATE automation_schedule_triggers SET parameters_json = ?
					WHERE automation_id = 'automation-schedule'
				`, parameters); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "multiple failed occurrences for one schedule",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB) {
				t.Helper()
				if _, err := db.ExecContext(ctx, `
					INSERT INTO automation_occurrences(
					  id, automation_id, automation_version, automation_title,
					  repository_id, repository_identity, context, timeout_seconds,
					  state, resolved_prompt, task_request_key, task_id_snapshot,
					  diagnostic, created_at, updated_at
					) VALUES (
					  'occurrence-schedule-second-failure', 'automation-schedule', 5,
					  'Shared', 'repo-1', 'github.com/example/factory', '', 3600,
					  'failed', 'Scheduled prompt', 'schedule-second-failure-key', '',
					  'repository still unavailable', 15, 15
					);
					INSERT INTO automation_schedule_occurrences(
					  occurrence_id, automation_id, kind, scheduled_at, cron, timezone,
					  definition_id, definition_snapshot, repository_ids_json,
					  parameters_json, concurrency_limit
					) VALUES (
					  'occurrence-schedule-second-failure', 'automation-schedule',
					  'scheduled', 2100, '0 9 * * *', 'UTC', 'definition-1',
					  '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
					  '["repo-1"]', '{}', 10
					)
				`); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", t.TempDir()+"/legacy.sqlite3")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
			applyMigrationsBeforeTasks(t, ctx, store)
			if _, err := db.ExecContext(ctx, legacyTasksFixture); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, ctx, db)
			if err := store.migrate(ctx); err == nil {
				t.Fatal("migration accepted an invalid legacy schedule prompt")
			}
			var definitions int
			if err := db.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'definitions'
			`).Scan(&definitions); err != nil {
				t.Fatal(err)
			}
			if definitions != 1 {
				t.Fatal("failed migration changed the legacy database")
			}
		})
	}
}

func applyMigrationsBeforeTasks(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	applyMigrationsBefore(t, ctx, store, "027_routines_work.sql")
}

func applyMigrationsBefore(t *testing.T, ctx context.Context, store *Store, stopAt string) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	applyPendingMigrationsBefore(t, ctx, store, stopAt)
}

func applyPendingMigrationsBefore(t *testing.T, ctx context.Context, store *Store, stopAt string) {
	t.Helper()
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for index, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if entry.Name() == stopAt {
			return
		}
		var applied int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, index+1).Scan(&applied); err != nil {
			t.Fatal(err)
		}
		if applied != 0 {
			continue
		}
		body, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		version := index + 1
		if bytes.HasPrefix(body, []byte("-- factory: foreign-keys-off")) {
			if err := store.applyForeignKeyRebuildMigration(ctx, entry.Name(), version, body); err != nil {
				t.Fatal(err)
			}
			continue
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, 0)`, version)
		}
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("%s not found", stopAt)
}

const legacyTasksFixture = `
INSERT INTO repositories(id, remote_identity, created_at, updated_at)
VALUES
  ('repo-1', 'github.com/example/factory', 1, 1),
  ('repo-2', 'github.com/example/neo', 1, 1);
INSERT INTO workers(
  id, name, worker_version, runtime_version, capacity, active_count, health,
  retained_worktrees_json, registered_at, last_heartbeat, runtime
) VALUES (
  'worker-1', 'Worker', 'test', 'test', 10, 0, 'healthy',
  '[{"attempt_id":"attempt-issue","reason":"kept"}]', 1, 1, 'codex'
);

INSERT INTO definitions(
  id, name, name_key, prompt, runtime, allowed_tools, timeout_seconds, inputs,
  generation, archived, created_at, updated_at
) VALUES ('definition-1', 'Shared', 'shared', 'Review.', 'codex', '["git"]', 3600, '{}', 1, 0, 1, 1);

BEGIN;
INSERT INTO workflows(id, enabled, current_revision_id, current_title_key, created_at, updated_at)
VALUES ('workflow-1', 1, 'revision-1', 'shared', 1, 1);
INSERT INTO workflow_revisions(
  id, workflow_id, revision_number, request_key, request_digest, title, summary, instructions, created_at
) VALUES
  ('revision-1', 'workflow-1', 1, 'revision-key', X'01', 'Shared', 'Summary', 'Instructions', 1),
  ('revision-2', 'workflow-1', 2, 'revision-key-2', X'02', 'Shared', 'Current summary', 'Current instructions', 2);
UPDATE workflows SET current_revision_id = 'revision-2', updated_at = 2 WHERE id = 'workflow-1';
COMMIT;

INSERT INTO automations(
  id, request_key, request_digest, title, title_key, workflow_id, repository_id,
  context, timeout_seconds, enabled, version, trigger_type, health_status,
  health_code, health_message, created_at, updated_at
) VALUES
  ('automation-issue', 'automation-issue-key', X'01', 'Issue review', 'issue review', 'workflow-1', 'repo-1', '', 3600, 0, 2, 'github_issue', 'disabled', '', 'Disabled', 1, 1),
  ('automation-pr', 'automation-pr-key', X'02', 'PR review', 'pr review', 'workflow-1', 'repo-1', '', 3600, 0, 3, 'github_pull_request', 'disabled', '', 'Disabled', 1, 1),
  ('automation-webhook', 'automation-webhook-key', X'03', 'Webhook review', 'webhook review', NULL, 'repo-1', '', 3600, 0, 4, 'github_webhook', 'disabled', '', 'Disabled', 1, 1),
  ('automation-schedule', 'automation-schedule-key', X'04', 'Shared', 'shared', NULL, 'repo-1', '', 3600, 1, 5, 'schedule', 'healthy', '', '', 1, 1);

INSERT INTO automation_schedule_triggers(automation_id, cron, timezone, next_due_at, definition_id, parameters_json, concurrency_limit)
VALUES ('automation-schedule', '0 9 * * *', 'UTC', 3000, 'definition-1', '{}', 10);
INSERT INTO automation_schedule_repositories(automation_id, position, repository_id)
VALUES ('automation-schedule', 0, 'repo-1');

INSERT INTO tasks(
  id, request_key, title, description, repository_id, timeout_seconds, created_at,
  workflow_id, workflow_revision_id, workflow_title, workflow_revision_number, context
) VALUES
  ('task-workflow', 'task-workflow-key', 'Workflow Run', 'Workflow prompt', 'repo-1', 3600, 10, 'workflow-1', 'revision-1', 'Shared', 1, ''),
  ('task-issue', 'task-issue-key', 'Issue Run', 'Issue prompt', 'repo-1', 3600, 11, 'workflow-1', 'revision-1', 'Shared', 1, ''),
  ('task-pr', 'task-pr-key', 'PR Run', 'PR prompt', 'repo-1', 3600, 12, 'workflow-1', 'revision-1', 'Shared', 1, ''),
  ('task-webhook', 'task-webhook-key', 'Webhook Run', 'Webhook prompt', 'repo-1', 3600, 13, NULL, NULL, NULL, NULL, ''),
  ('task-scheduled', 'task-scheduled-key', 'Scheduled Run', 'Scheduled prompt', 'repo-1', 3600, 14, NULL, NULL, NULL, NULL, '');
INSERT INTO executions(id, task_id, assigned_worker_id, required_runtime, state, created_at, updated_at)
VALUES
  ('execution-workflow', 'task-workflow', 'worker-1', 'codex', 'succeeded', 10, 20),
  ('execution-issue', 'task-issue', 'worker-1', 'codex', 'succeeded', 11, 21),
  ('execution-pr', 'task-pr', 'worker-1', 'codex', 'failed', 12, 22),
  ('execution-webhook', 'task-webhook', 'worker-1', 'codex', 'succeeded', 13, 23),
  ('execution-scheduled', 'task-scheduled', 'worker-1', 'codex', 'succeeded', 14, 24);
INSERT INTO attempts(
  id, execution_id, worker_id, attempt_number, state, lease_digest, lease_expires_at,
  result, error, started_at, completed_at, created_at
) VALUES
  ('attempt-workflow', 'execution-workflow', 'worker-1', 1, 'succeeded', X'01', 20, 'done', NULL, 11, 20, 10),
  ('attempt-issue', 'execution-issue', 'worker-1', 1, 'succeeded', X'02', 21, 'done', NULL, 12, 21, 11),
  ('attempt-pr', 'execution-pr', 'worker-1', 1, 'failed', X'03', 22, NULL, 'failed', 13, 22, 12),
  ('attempt-webhook', 'execution-webhook', 'worker-1', 1, 'succeeded', X'04', 23, 'done', NULL, 14, 23, 13),
  ('attempt-scheduled', 'execution-scheduled', 'worker-1', 1, 'succeeded', X'05', 24, 'done', NULL, 15, 24, 14);
INSERT INTO attempt_events(attempt_id, sequence, kind, payload, payload_bytes, server_time)
VALUES ('attempt-issue', 0, 'progress', '{"message":"kept"}', 18, 15);

INSERT INTO runs(
  id, request_key, request_digest, source_kind, definition_id, definition_snapshot,
  parameters, admitted_at, updated_at, concurrency_limit, resolved_prompt
) VALUES
  (
    'run-scheduled', 'run-scheduled-key', X'05', 'schedule', 'definition-1',
    '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
    '{}', 14, 24, 10, 'Scheduled resolved prompt'
  ),
  (
    'run-throttled', 'run-throttled-key', X'06', 'manual', 'definition-1',
    '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
    '{}', 15, 25, 1, 'Throttled resolved prompt'
  );
INSERT INTO jobs(
  id, run_id, repository_id, task_id, execution_id, state, blocked_reason,
  admitted_at, updated_at, repository_identity
) VALUES
  ('job-scheduled', 'run-scheduled', 'repo-1', 'task-scheduled', 'execution-scheduled', 'queued', NULL, 14, 24, 'github.com/example/factory'),
  ('job-throttled', 'run-throttled', 'repo-2', NULL, NULL, 'blocked', 'Waiting for an available Run concurrency slot.', 15, 25, 'github.com/example/neo');

INSERT INTO automation_occurrences(
  id, automation_id, automation_version, automation_title, workflow_revision_id,
  repository_id, repository_identity, context, timeout_seconds, state,
  resolved_prompt, task_request_key, task_id, task_id_snapshot, diagnostic, retry_at, created_at, updated_at
) VALUES
  ('occurrence-issue', 'automation-issue', 2, 'Issue review', 'revision-1', 'repo-1', 'github.com/example/factory', '', 3600, 'dispatched', 'Issue resolved prompt', 'task-issue-key', 'task-issue', 'task-issue', '', NULL, 11, 21),
  ('occurrence-pr', 'automation-pr', 3, 'PR review', 'revision-1', 'repo-1', 'github.com/example/factory', '', 3600, 'dispatched', 'PR resolved prompt', 'task-pr-key', 'task-pr', 'task-pr', '', NULL, 12, 22),
  ('occurrence-webhook', 'automation-webhook', 4, 'Webhook review', NULL, 'repo-1', 'github.com/example/factory', '', 3600, 'dispatched', 'Webhook resolved prompt', 'task-webhook-key', 'task-webhook', 'task-webhook', '', NULL, 13, 23),
  ('occurrence-schedule-admitted', 'automation-schedule', 5, 'Shared', NULL, 'repo-1', 'github.com/example/factory', '', 3600, 'dispatched', 'Scheduled resolved prompt', 'schedule-admitted-key', NULL, '', '', NULL, 14, 24),
  ('occurrence-schedule', 'automation-schedule', 5, 'Shared', NULL, 'repo-1', 'github.com/example/factory', '', 3600, 'failed', 'Scheduled prompt', 'schedule-pending-key', NULL, '', 'repository unavailable', 2500, 14, 14);
INSERT INTO automation_github_issue_occurrences(
  occurrence_id, automation_id, issue_number, issue_url, issue_title, observed_state,
  observed_labels_json, configured_state, required_labels_json
) VALUES ('occurrence-issue', 'automation-issue', 42, 'https://github.com/example/factory/issues/42', 'Issue', 'open', '["ready","bug"]', 'open', '["ready"]');
INSERT INTO automation_github_pull_request_occurrences(
  occurrence_id, automation_id, pull_request_number, pull_request_url,
  pull_request_title, observed_state, observed_draft, observed_base_branch,
  observed_head_commit, observed_labels_json, configured_state, include_drafts,
  required_labels_json, base_branches_json
) VALUES ('occurrence-pr', 'automation-pr', 7, 'https://github.com/example/factory/pull/7', 'PR', 'open', 1, 'main', 'abc123', '["review"]', 'open', 1, '["review"]', '["main"]');
INSERT INTO github_webhook_deliveries(
  delivery_id, payload_digest, event, action, repository_identity,
  pull_request_number, pull_request_url, pull_request_title, base_branch,
  head_commit, state, created_at, updated_at
) VALUES ('delivery-1', X'01', 'pull_request', 'opened', 'github.com/example/factory', 9,
  'https://github.com/example/factory/pull/9', 'Webhook PR', 'main', 'def456', 'completed', 13, 23);
INSERT INTO automation_github_webhook_occurrences(
  occurrence_id, automation_id, delivery_id, event, action, pull_request_number,
  pull_request_url, pull_request_title, base_branch, head_commit, definition_id,
  definition_snapshot, parameters_json, run_id
) VALUES ('occurrence-webhook', 'automation-webhook', 'delivery-1', 'pull_request', 'opened', 9,
  'https://github.com/example/factory/pull/9', 'Webhook PR', 'main', 'def456', 'definition-1',
  '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
  '{"priority":"high"}', NULL);
INSERT INTO automation_schedule_occurrences(
  occurrence_id, automation_id, kind, scheduled_at, cron, timezone, definition_id,
  definition_snapshot, repository_ids_json, parameters_json, concurrency_limit, run_id
) VALUES
  ('occurrence-schedule-admitted', 'automation-schedule', 'scheduled', 1500, '0 9 * * *', 'UTC',
  'definition-1', '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
  '["repo-1"]', '{}', 10, 'run-scheduled'),
  (
  'occurrence-schedule', 'automation-schedule', 'scheduled', 2000, '0 9 * * *', 'UTC',
  'definition-1', '{"name":"Shared","prompt":"Review.","runtime":"codex","allowed_tools":["git"],"timeout_seconds":3600,"inputs":{},"generation":1}',
  '["repo-1"]', '{}', 10, NULL
);
`

// preRenameFixture populates the Routines, Work, and Target model exactly as
// migration 029 leaves it, so migration 030 has real rows to rename.
const preRenameFixture = `
INSERT INTO repositories(id, remote_identity, created_at, updated_at)
VALUES ('repo-1', 'github.com/example/factory', 1, 1),
       ('repo-2', 'github.com/example/neo', 1, 1);
INSERT INTO workers(
  id, name, worker_version, work_claim_protocol_version, runtime, runtime_version,
  capacity, active_count, health, registered_at, last_heartbeat
) VALUES ('worker-1', 'Worker one', 'test', 1, 'codex', 'codex-test', 1, 0, 'healthy', 1, 1);
INSERT INTO routines(
  id, name, name_key, prompt, runtime, timeout_seconds, concurrency_limit,
  generation, created_at, updated_at
) VALUES ('routine-1', 'Weekly scan', 'weekly scan', 'Review.', 'codex', 3600, 10, 1, 1, 1);
INSERT INTO routine_repositories(routine_id, position, repository_id)
VALUES ('routine-1', 0, 'repo-1');
INSERT INTO work(
  id, request_key, request_digest, routine_id, routine_snapshot, source,
  admitted_at, updated_at
) VALUES ('work-1', 'key-1', X'01', 'routine-1', '{"id":"routine-1","name":"Weekly scan"}',
          'manual', 10, 10);
INSERT INTO work_targets(
  id, work_id, repository_id, repository_identity, resolved_prompt,
  required_runtime, timeout_seconds, state, blocked_reason, admitted_at
) VALUES ('target-1', 'work-1', 'repo-1', 'github.com/example/factory', 'Review.',
          'codex', 3600, 'blocked', 'Waiting for an available Routine concurrency slot.', 10);
INSERT INTO executions(
  id, work_target_id, assigned_worker_id, required_runtime, state, created_at, updated_at
) VALUES ('execution-1', 'target-1', 'worker-1', 'codex', 'queued', 10, 10);
`

// openMigrationTestDatabase enables foreign keys, as Open does in production,
// so a migration test enforces the same constraints a real upgrade does.
func openMigrationTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout%285000%29&_pragma=foreign_keys%281%29")
	if err != nil {
		t.Fatal(err)
	}
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys enabled = %d, err %v", enabled, err)
	}
	return db
}

func TestTaskRenameMigrationPreservesPopulatedRows(t *testing.T) {
	ctx := context.Background()
	db := openMigrationTestDatabase(t, t.TempDir()+"/pre-rename.sqlite3")
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBefore(t, ctx, store, "030_task_run_session.sql")
	if _, err := db.ExecContext(ctx, preRenameFixture); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var taskID, runID, sessionID, blockedReason string
	if err := db.QueryRowContext(ctx, `
		SELECT task.id, run.id, session.id, session.blocked_reason
		FROM tasks task
		JOIN runs run ON run.task_id = task.id
		JOIN sessions session ON session.run_id = run.id
		JOIN task_repositories link ON link.task_id = task.id
	`).Scan(&taskID, &runID, &sessionID, &blockedReason); err != nil {
		t.Fatal(err)
	}
	if taskID != "routine-1" || runID != "work-1" || sessionID != "target-1" {
		t.Fatalf("renamed rows = %q, %q, %q", taskID, runID, sessionID)
	}
	if blockedReason != taskConcurrencyBlockedReason {
		t.Fatalf("blocked reason = %q, want %q", blockedReason, taskConcurrencyBlockedReason)
	}
	var executionSessionID string
	if err := db.QueryRowContext(ctx, `SELECT session_id FROM executions WHERE id = 'execution-1'`).
		Scan(&executionSessionID); err != nil || executionSessionID != "target-1" {
		t.Fatalf("execution session = %q, err %v", executionSessionID, err)
	}
	// The rename preserves the stored value, and the claim protocol moved to 2,
	// so a Worker registered before the upgrade must re-register before claiming.
	var claimProtocolVersion int
	if err := db.QueryRowContext(ctx, `SELECT claim_protocol_version FROM workers WHERE id = 'worker-1'`).
		Scan(&claimProtocolVersion); err != nil || claimProtocolVersion != 1 {
		t.Fatalf("claim protocol version = %d, err %v", claimProtocolVersion, err)
	}
	if claimProtocolVersion == protocol.ClaimProtocolVersion {
		t.Fatal("a pre-rename Worker registration is still accepted by the claim protocol")
	}
	if _, err := store.Claim(ctx, "worker-1", protocol.ClaimRequest{
		RequestID: "migrated-claim", LeaseToken: tokenA,
	}); !serviceErrorCode(err, "worker_upgrade_required") {
		t.Fatalf("migrated Worker claim error = %v, want worker_upgrade_required", err)
	}
	if _, err := db.ExecContext(ctx, `SELECT json_extract(task_snapshot, '$.name') FROM runs`); err != nil {
		t.Fatalf("renamed run snapshot column: %v", err)
	}
	// The operator API must read a migrated database, not just its rows.
	detail, err := store.Run(ctx, "work-1")
	if err != nil || detail.Run.TaskID != "routine-1" || len(detail.Sessions) != 1 {
		t.Fatalf("migrated Run = %#v, err %v", detail, err)
	}
	page, err := store.RunPage(ctx, 10, "")
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != "work-1" {
		t.Fatalf("migrated Run page = %#v, err %v", page, err)
	}
	task, err := store.Task(ctx, "routine-1")
	if err != nil || task.Name != "Weekly scan" || task.RepositoryCount != 1 {
		t.Fatalf("migrated Task = %#v, err %v", task, err)
	}
	overview, err := store.Overview(ctx)
	if err != nil || len(overview.RecentRuns) != 1 {
		t.Fatalf("migrated Overview = %#v, err %v", overview, err)
	}
	worker, err := store.Worker(ctx, "worker-1")
	if err != nil || worker.ID != "worker-1" {
		t.Fatalf("migrated Worker = %#v, err %v", worker, err)
	}

	for _, name := range []string{"routines", "routine_repositories", "work", "work_targets"} {
		var exists int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).
			Scan(&exists); err != nil || exists != 0 {
			t.Fatalf("old table %s exists = %d, err %v", name, exists, err)
		}
	}
	// ALTER TABLE ... RENAME TO must have carried every REFERENCES clause with
	// it. That rewrite is governed by legacy_alter_table rather than by the
	// foreign_keys pragma, so assert the outcome instead of the mechanism: read
	// the stored schema, then prove a write and a live foreign key check.
	for table, want := range map[string]string{
		"sessions":          `REFERENCES "runs"(id)`,
		"executions":        `REFERENCES "sessions"(id)`,
		"task_repositories": `REFERENCES "tasks"(id)`,
	} {
		var schema string
		if err := db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).
			Scan(&schema); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(schema, want) {
			t.Fatalf("%s schema does not contain %s:\n%s", table, want, schema)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions(
			id, run_id, repository_id, repository_identity, resolved_prompt,
			required_runtime, timeout_seconds, state, admitted_at
		) VALUES ('session-2', 'work-1', 'repo-2', 'github.com/example/neo', 'Review.',
		          'codex', 3600, 'queued', 20)
	`); err != nil {
		t.Fatalf("write to the renamed sessions table: %v", err)
	}
	// A reference left pointing at the dropped work table would fail with
	// "no such table", not a foreign key violation.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions(
			id, run_id, repository_id, repository_identity, resolved_prompt,
			required_runtime, timeout_seconds, state, admitted_at
		) VALUES ('session-3', 'missing-run', 'repo-2', 'github.com/example/neo', 'Review.',
		          'codex', 3600, 'queued', 20)
	`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("session run reference error = %v, want a foreign key violation", err)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		rows.Close()
		t.Fatal("migration 030 left a foreign key violation")
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"tasks_list_order", "tasks_due", "task_repositories_order",
		"runs_list_order", "sessions_run_order", "sessions_claim_order",
		"sessions_backend_claim",
	} {
		var exists int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).
			Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("renamed index %s exists = %d, err %v", name, exists, err)
		}
	}
}

func TestWorkLifecycleMigrationPreservesFrozenTargetOrder(t *testing.T) {
	ctx := context.Background()
	db := openMigrationTestDatabase(t, t.TempDir()+"/pre-work-lifecycle.sqlite3")
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBefore(t, ctx, store, "030_task_run_session.sql")
	if _, err := db.ExecContext(ctx, preRenameFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE work SET routine_snapshot = '{
		  "id":"routine-1",
		  "name":"Weekly scan",
		  "runtime":"codex",
		  "timeout_seconds":3600,
		  "concurrency_limit":10,
		  "generation":1,
		  "repositories":[
		    {"id":"repo-2","remote_identity":"github.com/example/neo"},
		    {"id":"repo-1","remote_identity":"github.com/example/factory"}
		  ]
		}' WHERE id = 'work-1';
		INSERT INTO routine_repositories(routine_id, position, repository_id)
		VALUES ('routine-1', 1, 'repo-2');
		INSERT INTO work_targets(
		  id, work_id, repository_id, repository_identity, resolved_prompt,
		  required_runtime, timeout_seconds, state, blocked_reason, admitted_at
		) VALUES (
		  'zzz-target-2', 'work-1', 'repo-2', 'github.com/example/neo', 'Review.',
		  'codex', 3600, 'blocked', 'Waiting for an available Routine concurrency slot.', 10
		);
	`); err != nil {
		t.Fatal(err)
	}
	legacyResult := strings.Repeat("r", protocol.MaxResultBytes)
	legacyError := strings.Repeat("e", protocol.MaxErrorBytes)
	legacyBlockedReason := strings.Repeat("🧪", protocol.MaxWaitingReasonBytes/2+1)
	if _, err := db.ExecContext(ctx, `
		UPDATE work_targets
		SET result = ?, failure_reason = ?, blocked_reason = ?
		WHERE id = 'target-1'
	`, legacyResult, legacyError, legacyBlockedReason); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var result, failure, blockedReason, waitingReason, terminalMessage string
	if err := db.QueryRowContext(ctx, `
		SELECT result, failure_reason, blocked_reason, waiting_reason, terminal_message
		FROM sessions WHERE id = 'target-1'
	`).Scan(&result, &failure, &blockedReason, &waitingReason, &terminalMessage); err != nil {
		t.Fatal(err)
	}
	if result != legacyResult || failure != legacyError || blockedReason != legacyBlockedReason {
		t.Fatal("migration did not preserve full legacy outcome or blocked payloads")
	}
	if len([]byte(waitingReason)) > protocol.MaxWaitingReasonBytes || !utf8.ValidString(waitingReason) ||
		waitingReason != strings.Repeat("🧪", 512) {
		t.Fatalf("migrated waiting reason is not a safe bounded projection: bytes=%d valid=%v",
			len([]byte(waitingReason)), utf8.ValidString(waitingReason))
	}
	if terminalMessage != "" {
		t.Fatalf("legacy process-exit outcome copied to terminal message: bytes=%d", len(terminalMessage))
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, target_position FROM sessions WHERE run_id = 'work-1' ORDER BY target_position
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		var position int
		if err := rows.Scan(&id, &position); err != nil {
			t.Fatal(err)
		}
		if position != len(ids) {
			t.Fatalf("target %q position = %d, want %d", id, position, len(ids))
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "zzz-target-2" || ids[1] != "target-1" {
		t.Fatalf("migrated target order = %#v", ids)
	}
	detail, err := store.Run(ctx, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Run.Targets) != 2 || detail.Run.Targets[0].ID != "zzz-target-2" ||
		detail.Run.Targets[1].ID != "target-1" {
		t.Fatalf("frozen target snapshot order = %#v", detail.Run.Targets)
	}
}

func TestPipelineMigrationBackfillsQueuedSessionForStageProtocol(t *testing.T) {
	ctx := context.Background()
	db := openMigrationTestDatabase(t, t.TempDir()+"/pre-pipelines.sqlite3")
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBefore(t, ctx, store, "030_task_run_session.sql")
	if _, err := db.ExecContext(ctx, preRenameFixture); err != nil {
		t.Fatal(err)
	}
	applyPendingMigrationsBefore(t, ctx, store, "033_pipeline_templates.sql")

	worker, err := store.Worker(ctx, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterWorker(ctx, worker.ID, protocol.WorkerRegistration{
		Name: worker.Name, Labels: worker.Labels, WorkerVersion: "pipeline-migration-test",
		ClaimProtocolVersion: protocol.ClaimProtocolVersion, Runtime: worker.Runtime,
		RuntimeVersion: "codex-test", Capacity: worker.Capacity,
		ActiveCount: 0, Health: "healthy",
		Repositories: []protocol.RepositoryRegistration{{
			Key: "factory", RemoteIdentity: "github.com/example/factory",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE sessions
		SET state = 'queued', blocked_reason = NULL, waiting_reason = '', execution_owner = 'none'
		WHERE id = 'target-1';
		UPDATE runs
		SET task_snapshot = json_set(task_snapshot, '$.concurrency_limit', 10)
		WHERE id = 'work-1';
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var stageState, stagePrompt string
	if err := db.QueryRowContext(ctx, `
		SELECT state, prompt FROM session_stages WHERE session_id = 'target-1' AND position = 0
	`).Scan(&stageState, &stagePrompt); err != nil {
		t.Fatal(err)
	}
	if stageState != string(protocol.StagePending) || stagePrompt != "Review." {
		t.Fatalf("backfilled stage = state %q, prompt %q", stageState, stagePrompt)
	}
	claim, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{
		RequestID: "migrated-pipeline-session", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if _, err := store.StartAttempt(ctx, claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartStage(ctx, claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteStage(ctx, claim.Attempt.ID, 0, protocol.CompleteStageRequest{
		LeaseToken: tokenA, State: protocol.StageSucceeded, Result: "done",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(ctx, claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "done",
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := store.Run(ctx, "work-1")
	if err != nil || detail.Run.State != protocol.RunSucceeded ||
		len(detail.Sessions) != 1 || detail.Sessions[0].Stages[0].State != protocol.StageSucceeded {
		t.Fatalf("completed migrated Pipeline = %#v, error %v", detail, err)
	}
}

func TestTaskRenameMigrationRefusesWhenNewNameIsTaken(t *testing.T) {
	ctx := context.Background()
	db := openMigrationTestDatabase(t, t.TempDir()+"/collision.sqlite3")
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBefore(t, ctx, store, "030_task_run_session.sql")
	if _, err := db.ExecContext(ctx, preRenameFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE sessions (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	err := store.migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "renaming onto it would lose data") {
		t.Fatalf("migration error = %v, want a named refusal", err)
	}
	var routines int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routines`).Scan(&routines); err != nil || routines != 1 {
		t.Fatalf("routines preserved = %d, err %v", routines, err)
	}
}

func TestTaskRenameMigrationRefusesWhenTheSourceModelIsMissing(t *testing.T) {
	ctx := context.Background()
	db := openMigrationTestDatabase(t, t.TempDir()+"/incomplete.sqlite3")
	store := &Store{db: db, now: time.Now, sweepEvery: 5 * time.Second}
	t.Cleanup(func() { _ = db.Close() })
	applyMigrationsBefore(t, ctx, store, "030_task_run_session.sql")
	if _, err := db.ExecContext(ctx, preRenameFixture); err != nil {
		t.Fatal(err)
	}
	// routine_repositories is referenced by nothing, so it can be removed to
	// simulate a database that never finished the Routines and Work model.
	if _, err := db.ExecContext(ctx, `DROP TABLE routine_repositories`); err != nil {
		t.Fatal(err)
	}
	err := store.migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "must all exist") {
		t.Fatalf("migration error = %v, want a missing-source refusal", err)
	}
	var routines int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routines`).Scan(&routines); err != nil || routines != 1 {
		t.Fatalf("routines preserved = %d, err %v", routines, err)
	}
}
