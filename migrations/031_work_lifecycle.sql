-- factory: foreign-keys-off

-- Existing Tasks and Runs retain their exit-based completion contract. New
-- agent-directed Procedures may opt in explicitly, and every admitted Run
-- stores the selected contract independently of later Task changes.
ALTER TABLE tasks ADD COLUMN outcome_contract TEXT NOT NULL DEFAULT 'process_exit'
    CHECK (outcome_contract IN ('process_exit', 'agent_update'));

ALTER TABLE runs ADD COLUMN outcome_contract TEXT NOT NULL DEFAULT 'process_exit'
    CHECK (outcome_contract IN ('process_exit', 'agent_update'));
ALTER TABLE runs ADD COLUMN targets_snapshot TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(targets_snapshot) AND json_type(targets_snapshot) = 'array');

UPDATE runs
SET task_snapshot = json_set(task_snapshot, '$.outcome_contract', 'process_exit');

-- Rebuild Sessions as the additive backing store for Work. This removes the
-- old one-Session-per-repository constraint, widens the state check, and keeps
-- every existing column and identifier so historical foreign keys remain
-- valid. Existing blocked and preparing states stay readable as compatibility
-- execution states while new Work uses the product lifecycle states.
CREATE TABLE sessions_work_lifecycle (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES "runs"(id) ON DELETE CASCADE,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    repository_identity TEXT NOT NULL,
    resolved_prompt TEXT NOT NULL,
    required_runtime TEXT NOT NULL CHECK (required_runtime IN ('pi', 'codex', 'claude-code')),
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 28800),
    state TEXT NOT NULL CHECK (state IN (
        'blocked', 'queued', 'preparing', 'running', 'needs-input',
        'ready', 'succeeded', 'failed', 'no-change', 'cancelled'
    )),
    blocked_reason TEXT,
    assigned_worker_id TEXT REFERENCES workers(id),
    cancellation_requested INTEGER NOT NULL DEFAULT 0 CHECK (cancellation_requested IN (0, 1)),
    retry_may_repeat_effects INTEGER NOT NULL DEFAULT 0 CHECK (retry_may_repeat_effects IN (0, 1)),
    admitted_at INTEGER NOT NULL,
    started_at INTEGER,
    terminal_at INTEGER,
    result TEXT,
    failure_reason TEXT,
    execution_profile_id TEXT NOT NULL DEFAULT 'persistent-auto',
    execution_profile_version INTEGER NOT NULL DEFAULT 1 CHECK (execution_profile_version >= 1),
    execution_backend TEXT NOT NULL DEFAULT 'persistent'
        CHECK (execution_backend IN ('persistent', 'fake_cloud_run')),
    execution_provider TEXT NOT NULL DEFAULT 'worker',
    execution_model TEXT NOT NULL DEFAULT 'worker-default',
    resource_class TEXT NOT NULL DEFAULT 'worker',
    commit_resolution_policy TEXT NOT NULL DEFAULT 'resolve_per_attempt'
        CHECK (commit_resolution_policy IN ('resolve_per_attempt', 'frozen_commit')),
    target_position INTEGER NOT NULL DEFAULT 0 CHECK (target_position BETWEEN 0 AND 99),
    target_key TEXT NOT NULL DEFAULT '' CHECK (length(CAST(target_key AS BLOB)) <= 1024),
    target_kind TEXT NOT NULL DEFAULT 'repository'
        CHECK (target_kind IN ('work_item', 'repository')),
    source_kind TEXT NOT NULL DEFAULT 'repository'
        CHECK (source_kind IN ('github_issue', 'opaque', 'repository')),
    source_key TEXT NOT NULL DEFAULT '' CHECK (length(CAST(source_key AS BLOB)) <= 1024),
    source_reference TEXT NOT NULL DEFAULT '' CHECK (length(CAST(source_reference AS BLOB)) <= 2048),
    context_snapshot TEXT NOT NULL DEFAULT '' CHECK (length(CAST(context_snapshot AS BLOB)) <= 65536),
    publish_branch TEXT NOT NULL DEFAULT '' CHECK (length(CAST(publish_branch AS BLOB)) <= 255),
    predecessor_work_id TEXT REFERENCES sessions_work_lifecycle(id),
    execution_owner TEXT NOT NULL DEFAULT 'none'
        CHECK (execution_owner IN ('none', 'worker_attempt', 'operator')),
    waiting_reason TEXT NOT NULL DEFAULT '' CHECK (length(CAST(waiting_reason AS BLOB)) <= 2048),
    latest_progress TEXT NOT NULL DEFAULT '' CHECK (length(CAST(latest_progress AS BLOB)) <= 2048),
    question TEXT NOT NULL DEFAULT '' CHECK (length(CAST(question AS BLOB)) <= 8192),
    checkpoint_sha TEXT NOT NULL DEFAULT '' CHECK (length(checkpoint_sha) <= 64),
    pending_resume_sha TEXT NOT NULL DEFAULT '' CHECK (length(pending_resume_sha) <= 64),
    pull_request_url TEXT NOT NULL DEFAULT '' CHECK (length(CAST(pull_request_url AS BLOB)) <= 2048),
    pull_request_head_branch TEXT NOT NULL DEFAULT '' CHECK (length(CAST(pull_request_head_branch AS BLOB)) <= 255),
    pull_request_head_sha TEXT NOT NULL DEFAULT '' CHECK (length(pull_request_head_sha) <= 64),
    terminal_message TEXT NOT NULL DEFAULT '' CHECK (length(CAST(terminal_message AS BLOB)) <= 8192),
    CHECK (
        (state = 'preparing' AND execution_owner = 'worker_attempt')
        OR (state = 'running' AND execution_owner IN ('worker_attempt', 'operator'))
        OR (state NOT IN ('preparing', 'running') AND execution_owner = 'none')
    ),
    CHECK (state != 'needs-input' OR (question != '' AND checkpoint_sha != '' AND pending_resume_sha != '')),
    CHECK (state != 'ready' OR pull_request_url != ''),
    CHECK (state NOT IN ('ready', 'succeeded', 'failed', 'no-change', 'cancelled') OR terminal_at IS NOT NULL)
);

INSERT INTO sessions_work_lifecycle(
    id, run_id, repository_id, repository_identity, resolved_prompt, required_runtime,
    timeout_seconds, state, blocked_reason, assigned_worker_id, cancellation_requested,
    retry_may_repeat_effects, admitted_at, started_at, terminal_at, result, failure_reason,
    execution_profile_id, execution_profile_version, execution_backend, execution_provider,
    execution_model, resource_class, commit_resolution_policy, target_position, target_key,
    target_kind, source_kind, source_key, source_reference, context_snapshot, publish_branch,
    execution_owner, waiting_reason, terminal_message
)
SELECT
    session.id, session.run_id, session.repository_id, session.repository_identity,
    session.resolved_prompt, session.required_runtime, session.timeout_seconds, session.state,
    session.blocked_reason, session.assigned_worker_id, session.cancellation_requested,
    session.retry_may_repeat_effects, session.admitted_at, session.started_at, session.terminal_at,
    session.result, session.failure_reason, session.execution_profile_id,
    session.execution_profile_version, session.execution_backend, session.execution_provider,
    session.execution_model, session.resource_class, session.commit_resolution_policy,
    COALESCE(
        (
            SELECT CAST(target.key AS INTEGER)
            FROM runs frozen_run, json_each(frozen_run.task_snapshot, '$.repositories') target
            WHERE frozen_run.id = session.run_id
              AND CASE WHEN target.type = 'object'
                  THEN json_extract(target.value, '$.id') ELSE target.value END = session.repository_id
            LIMIT 1
        ),
        (
            SELECT CAST(target.key AS INTEGER)
            FROM runs frozen_run, json_each(frozen_run.task_snapshot, '$.repository_ids') target
            WHERE frozen_run.id = session.run_id AND target.value = session.repository_id
            LIMIT 1
        ),
        session.fallback_position
    ),
    'repository:' || session.repository_id,
    'repository', 'repository', session.repository_id, session.repository_identity, '',
    'factory/work-' || substr(replace(session.id, '-', ''), 1, 16),
    CASE WHEN session.state IN ('preparing', 'running') THEN 'worker_attempt' ELSE 'none' END,
    CASE
        WHEN length(CAST(COALESCE(session.blocked_reason, '') AS BLOB)) <= 2048
            THEN COALESCE(session.blocked_reason, '')
        -- SQLite substrings TEXT by characters. A 512-character prefix is
        -- valid UTF-8 and at most 2048 bytes, even for four-byte code points.
        ELSE substr(session.blocked_reason, 1, 512)
    END,
    -- Legacy process-exit outcomes remain in result/failure_reason. Copying
    -- them here would reject their valid 256 KiB/64 KiB limits against the
    -- deliberately narrower agent-update terminal message.
    ''
FROM (
    SELECT existing.*,
           ROW_NUMBER() OVER (
               PARTITION BY existing.run_id ORDER BY existing.admitted_at, existing.id
           ) - 1 AS fallback_position
    FROM sessions existing
) session;

DROP TABLE sessions;
ALTER TABLE sessions_work_lifecycle RENAME TO sessions;

CREATE INDEX sessions_run_order ON sessions(run_id, target_position, id);
CREATE INDEX sessions_claim_order ON sessions(state, admitted_at, id);
CREATE INDEX sessions_backend_claim ON sessions(execution_backend, state, admitted_at, id);
CREATE INDEX sessions_predecessor ON sessions(predecessor_work_id);
CREATE UNIQUE INDEX sessions_run_target_key
    ON sessions(run_id, target_key) WHERE target_key != '';
CREATE UNIQUE INDEX sessions_run_target_position
    ON sessions(run_id, target_position) WHERE target_key != '';
CREATE INDEX sessions_retry_identity
    ON sessions(repository_id, source_kind, source_key, state);

UPDATE runs
SET targets_snapshot = COALESCE((
    SELECT json_group_array(json(target_json))
    FROM (
        SELECT json_object(
            'id', session.id,
            'position', session.target_position,
            'target_key', session.target_key,
            'target_kind', session.target_kind,
            'repository_id', session.repository_id,
            'repository_identity', session.repository_identity,
            'source_kind', session.source_kind,
            'source_key', session.source_key,
            'source_reference', session.source_reference,
            'context_snapshot', session.context_snapshot,
            'publish_branch', session.publish_branch
        ) AS target_json
        FROM sessions session
        WHERE session.run_id = runs.id
        ORDER BY session.target_position
    ) ordered_targets
), '[]');

-- The full update history is durable and bounded per Attempt. The reserved
-- outcome slot means an agent can always finish after 199 progress updates.
CREATE TABLE work_updates (
    id TEXT PRIMARY KEY,
    work_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    attempt_id TEXT REFERENCES attempts(id),
    request_id TEXT NOT NULL CHECK (length(CAST(request_id AS BLOB)) BETWEEN 1 AND 200),
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    status TEXT NOT NULL CHECK (status IN ('running', 'ready', 'needs-input', 'failed', 'no-change')),
    message TEXT NOT NULL CHECK (
        length(CAST(message AS BLOB)) BETWEEN 1 AND
        CASE WHEN status = 'running' THEN 2048 ELSE 8192 END
    ),
    pull_request_url TEXT NOT NULL DEFAULT '' CHECK (length(CAST(pull_request_url AS BLOB)) <= 2048),
    pull_request_head_branch TEXT NOT NULL DEFAULT '' CHECK (length(CAST(pull_request_head_branch AS BLOB)) <= 255),
    pull_request_head_sha TEXT NOT NULL DEFAULT '' CHECK (length(pull_request_head_sha) <= 64),
    checkpoint_sha TEXT NOT NULL DEFAULT '' CHECK (length(checkpoint_sha) <= 64),
    actor TEXT NOT NULL CHECK (actor IN ('agent', 'operator', 'system')),
    accepted_at INTEGER NOT NULL,
    UNIQUE (work_id, sequence),
    CHECK (status != 'ready' OR pull_request_url != ''),
    CHECK (status != 'needs-input' OR checkpoint_sha != '')
);

CREATE UNIQUE INDEX work_updates_attempt_request
    ON work_updates(attempt_id, request_id) WHERE attempt_id IS NOT NULL;
CREATE UNIQUE INDEX work_updates_operator_request
    ON work_updates(work_id, request_id) WHERE attempt_id IS NULL;
CREATE UNIQUE INDEX work_updates_attempt_outcome
    ON work_updates(attempt_id) WHERE attempt_id IS NOT NULL AND status != 'running';
CREATE INDEX work_updates_work_order ON work_updates(work_id, sequence);

CREATE TRIGGER work_updates_attempt_limit
BEFORE INSERT ON work_updates
WHEN NEW.attempt_id IS NOT NULL AND (
    SELECT COUNT(*) FROM work_updates WHERE attempt_id = NEW.attempt_id
) >= 200
BEGIN
    SELECT RAISE(ABORT, 'work update limit reached');
END;

CREATE TRIGGER work_updates_progress_limit
BEFORE INSERT ON work_updates
WHEN NEW.attempt_id IS NOT NULL AND NEW.status = 'running' AND (
    SELECT COUNT(*) FROM work_updates
    WHERE attempt_id = NEW.attempt_id AND status = 'running'
) >= 199
BEGIN
    SELECT RAISE(ABORT, 'work progress update limit reached');
END;
