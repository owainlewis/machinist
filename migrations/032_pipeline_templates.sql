CREATE TABLE pipelines (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    name_key TEXT NOT NULL UNIQUE,
    generation INTEGER NOT NULL CHECK (generation >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE pipeline_stages (
    pipeline_id TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 19),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 200),
    prompt TEXT NOT NULL CHECK (length(CAST(prompt AS BLOB)) BETWEEN 1 AND 65536),
    PRIMARY KEY (pipeline_id, position)
);

INSERT INTO pipelines(id, name, name_key, generation, created_at, updated_at)
VALUES ('00000000-0000-0000-0000-000000000001', 'Single agent', 'single agent', 1, 0, 0);

INSERT INTO pipeline_stages(pipeline_id, position, name, prompt)
VALUES ('00000000-0000-0000-0000-000000000001', 0, 'Do the task', '{{ task.prompt }}');

ALTER TABLE tasks ADD COLUMN pipeline_id TEXT REFERENCES pipelines(id);
UPDATE tasks SET pipeline_id = '00000000-0000-0000-0000-000000000001' WHERE pipeline_id IS NULL;

CREATE TABLE session_stages (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 19),
    name TEXT NOT NULL,
    prompt TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at INTEGER,
    completed_at INTEGER,
    PRIMARY KEY (session_id, position)
);

CREATE INDEX pipelines_list_order ON pipelines(updated_at DESC, id DESC);
CREATE INDEX session_stages_state ON session_stages(session_id, state, position);
