-- Every accepted agent invocation retains its agent-visible request
-- fingerprint and exact response. Worker-derived evidence is deliberately not
-- fingerprinted, so a transport retry does not depend on mutable provider or
-- repository state. A later invocation may also repeat an already-stored
-- outcome with a new request ID.
CREATE TABLE agent_update_requests (
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    request_id TEXT NOT NULL CHECK (length(CAST(request_id AS BLOB)) BETWEEN 1 AND 200),
    request_digest BLOB NOT NULL CHECK (length(request_digest) = 32),
    update_id TEXT NOT NULL REFERENCES work_updates(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (attempt_id, request_id)
);

CREATE INDEX agent_update_requests_update ON agent_update_requests(update_id);
