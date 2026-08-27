# Local Control Plane V1

Status: Implemented

## 1. Outcome

Machinist can run immediately from the CLI or receive work from one local control plane.
The control plane stores jobs and runs in SQLite, owns agent templates and pipelines,
and shows process state in a small React application. Vite bundles the frontend at build
time and Go embeds the static output, so Machinist still deploys as one binary with no Node
runtime. A worker polls outbound, executes one run at a time with the existing runner,
and reports the terminal result.

The control plane records only execution facts. It does not interpret agent output,
GitHub labels, Linear state, or whether the requested product outcome is correct.

## 2. Commands

Direct mode remains immediate and independent:

```sh
machinist run --agent=foreman --prompt="Complete a GitHub issue URL"
machinist run --pipeline=quality --prompt="Check the current repository"
```

`quality` illustrates a user-defined pipeline. Machinist does not ship a default pipeline.

Managed mode uses two long-running commands:

```sh
machinist start --config=/path/to/config.toml
machinist worker start --config=/path/to/worker.toml
```

`machinist start` listens on `127.0.0.1:7331` so it does not interfere with the existing
application on port 8080. This local phase rejects non-loopback listeners. Remote VM and
Kubernetes deployment remains a target, but requires a separate authenticated human web
surface and TLS boundary before Machinist exposes the server beyond the host.

## 3. Configuration ownership

The shared Machinist configuration owns the server, portable prompts, and pipelines:

```toml
[server]
listen = "127.0.0.1:7331"
database = "~/.machinist/server/machinist.db"
worker_token_file = "~/.machinist/server/worker.token"

[agents.foreman]
executor = "codex"
prompt_file = "agents/foreman.md"
timeout = "120m"

[agents.audit]
executor = "codex"
prompt_file = "agents/audit.md"
timeout = "60m"
```

These are the two shipped agent definitions. Users can add their own agents and compose
them into named pipelines; pipeline configuration and execution are unchanged.

The worker owns the machine-specific execution and credential boundary. Its
configuration file stores executor commands, repository paths, and token-file
paths. Provider credentials come from executor configuration or the worker
process environment:

```toml
data_directory = "~/.machinist/worker"

[control_plane]
url = "http://127.0.0.1:7331"
token_file = "~/.machinist/server/worker.token"

[executors.codex]
command = ["codex", "exec", "--json", "--model={{machinist.model}}", "--sandbox", "danger-full-access", "-"]
models = { luna = "gpt-5.6-luna", terra = "gpt-5.6-terra", sol = "gpt-5.6-sol" }

[repositories.machinist]
path = "/workspace/machinist"
```

Direct mode reads `config.toml` and resolves its named executor through `worker.toml`.
Managed mode renders the same configuration on the server and the worker
resolves the named executor and repository through its own `worker.toml`.

## 4. Shared execution boundary

Both modes produce one resolved execution request for the existing runner. The runner
receives only a complete prompt, command, repository path, timeout, and run ID.

For managed work, the server sends this immutable run specification:

```text
run ID
job ID
agent name and definition hash
executor name
optional model alias
repository key
rendered prompt
timeout
opaque lease token
```

The server never sends an executable command, local path, environment variable, or
credential. Each poll advertises the worker's executor and repository names plus each
executor's model aliases. An empty alias list means that executor accepts raw model
names. The server leases only compatible work. The worker still rejects an unknown name
before starting a process and reports that preflight failure without a local event file.

## 5. Job and run state

A submitted job contains an input prompt, repository key, and either one agent or one
pipeline. A pipeline expands to ordered runs with immutable rendered prompts. Only the
first run starts as queued; later runs start as pending. Exit status zero promotes the
next pending run to queued. Any other terminal state fails the job and marks later runs
skipped.

```text
job: queued -> running -> succeeded | failed
run: pending -> queued -> running -> succeeded | failed | timed_out | cancelled
                ^          |
                +----------+ expired lease
     pending -----------------------------------------------> skipped
```

Each worker process generates a random instance ID at startup; the configured worker name
is display metadata only. Leasing selects a compatible queued run, creates an opaque
lease token, and changes the run to running for that instance in one SQLite transaction.
The lease expires after 30 seconds. Completion requires the lease token. Two concurrent
polls cannot receive the same run.

Polling is idempotent for a worker instance. If a lease commits but its HTTP response is
lost, the instance's next poll before expiry returns the same active run and lease token
instead of claiming new work. A worker instance therefore has at most one active run.

While its agent process runs or its completion is awaiting acknowledgement, the worker
renews the lease every 10 seconds. It also renews once immediately before completion
delivery begins. Each accepted heartbeat extends the expiry to 30 seconds after the
server receives it. Polling first
returns every expired running lease to queued in the same transaction used to select
work. Reclaim clears the old worker, token, expiry, and attempt start time while leaving
the job running. A compatible worker can then receive a new token and execute the run.
The old worker may continue locally during a network partition, but its stale token
cannot renew or complete the redispatched run. This provides abandoned-run recovery, not
exactly-once agent side effects or process resumption.

## 6. Persistence

SQLite stores:

- `jobs`: input prompt, repository key, selection, state, and timestamps.
- `runs`: ordered agent runs, immutable rendered prompt metadata, worker, lease expiry,
  process outcome, and timestamps. Existing databases add the nullable expiry field on
  open; a running row without an expiry is recoverable on the next poll.
- `workers`: worker instance ID, display name, and last poll time.

The worker writes `events.jsonl` and `result.json` under a directory keyed by both the run
ID and lease token. A redispatched lease therefore keeps its files separate from an
abandoned attempt on the same machine. On completion the worker uploads the current
attempt's result and event file to the server. A preflight failure instead uploads a
terminal state, exit code, and diagnostic with no files. The server stores the payload
with the run so completed output survives restarts.

The runner caps the actual encoded event file at 32 MiB, including per-event metadata.
The completion endpoint accepts a 96 MiB JSON envelope, which safely contains the event
string even under worst-case JSON escaping plus the result and completion metadata.

Completion is idempotent for the leased run and worker. If the database commits but the
response is lost, sending the same completion again succeeds without changing state. A
live worker retries the same local completion payload with bounded backoff until the
server acknowledges it or the worker is stopped. Permanent HTTP 4xx responses stop the
worker instead of retrying an impossible payload forever. This retries result delivery,
never the agent execution. The server rejects terminal states that contradict their exit
code. Live log streaming is not in V1. Losing the control-plane connection does not
terminate the local agent process, including when heartbeat delivery temporarily fails.

## 7. HTTP surface

- `GET /`: embedded React application.
- `GET /api/v1/status`: summary-only jobs, runs, workers, available selections, and CSRF
  token. Stored result and event payloads are not included in periodic status responses.
- `GET /api/v1/catalog`: agent and pipeline names plus every repository name previously
  advertised by a worker. The CLI uses this catalog to validate managed submissions.
- `POST /api/v1/jobs`: submit an agent or pipeline job. CLI submissions require the
  worker bearer token. Browser submissions require a server-generated CSRF token plus
  matching loopback Host and Origin headers.
- `POST /api/v1/workers/poll`: authenticate a worker, update last-seen state, and lease
  one compatible run when available. The request includes the worker name and its
  process instance ID, executor names, repository names, and per-executor model aliases
  or raw-model support. Repeated polls return that instance's existing unexpired active
  lease. Polling also reclaims expired leases before selecting work.
- `POST /api/v1/runs/{id}/heartbeat`: authenticate the owning worker and renew its active
  lease. Unknown runs return 404. Non-running, expired, differently owned, and stale-token
  leases return 409 without changing state.
- `POST /api/v1/runs/{id}/complete`: authenticate the owning worker and record the
  terminal result and optional result/events payload. The request must include the opaque
  lease token.

Worker endpoints and CLI submissions require `Authorization: Bearer <token>`. The V1
status page, catalog, and form are intended for the loopback listener and have no user
authentication. The submission form uses a random process-local CSRF token, and browser
submissions with a missing or foreign Origin are rejected. Starting the server on a
non-loopback address is rejected so that this limitation is explicit.

Workers permit plain HTTP only for loopback control-plane URLs. Non-loopback URLs require
HTTPS so bearer tokens and prompts are never sent across a network in cleartext.

## 8. Invariants

- INV-1: `machinist run` never requires or contacts a control plane.
- INV-2: a managed run uses the server-rendered prompt but only worker-owned executors,
  repositories, environment, and credentials.
- INV-3: the runner receives a final rendered prompt and knows nothing about templates,
  tickets, labels, or control-plane semantics.
- INV-4: one worker process instance has at most one active run, and it receives only work
  matching its advertised executor, repository, and requested model capability.
- INV-5: one unexpired run lease belongs to one worker instance and opaque token. Expired
  leases are transactionally reclaimed during polling and redispatched with a new token.
- INV-6: the control plane advances pipelines only from process terminal state.
- INV-7: job, run, result, and completed output state survives a server restart.

## 9. Acceptance criteria and checks

- AC-1: existing direct agent and pipeline tests continue to pass without a server.
- AC-2: a local status-page submission is persisted, leased by a configured worker,
  executed by the existing runner, and shown as succeeded or failed.
- AC-3: a three-agent pipeline leases agents in order, uses the same input prompt for
  every template, and marks later steps skipped after the first non-zero result.
- AC-4: invalid worker tokens receive HTTP 401 and cannot lease or complete runs.
- AC-5: concurrent compatible polls lease a queued run once, while workers with the wrong
  executor, repository, or model capability receive no work. Unknown names that still
  reach a worker fail locally without starting an agent and report a diagnostic without
  event files.
- AC-6: restarting the server against the same database preserves jobs, runs, results,
  completed output, and worker last-seen state.
- AC-7: the default server binds only to `127.0.0.1:7331`; port 8080 is never used.
- AC-8: `just check` passes, focused HTTP/store tests pass, and a real browser verifies
  submission and terminal status in the page.
- AC-9: repeating completion after a committed-but-unacknowledged response succeeds
  idempotently and does not advance a pipeline twice.
- AC-10: polling again after a committed-but-unacknowledged lease returns the same run and
  lease token to the same worker instance; another instance cannot complete it.
- AC-11: workers renew active leases every 10 seconds, each valid heartbeat sets expiry
  30 seconds from server receipt, and invalid heartbeats do not change the lease.
- AC-12: polling transactionally reclaims expired or pre-migration running leases and a
  compatible worker can receive the run with a new token while the job remains running.
- AC-13: an expired token cannot upload output or advance a pipeline, even if the old
  worker continues executing locally.
- AC-14: the worker renews its lease until completion delivery is acknowledged, and a
  redispatched lease writes to a distinct local artifact directory.

## 10. Out of scope

GitHub or Linear intake, ticket synchronization, label interpretation, prompt result
interpretation, live log streaming, worker enrollment, token issuance, general retry
counts or backoff policy, interrupted-process resumption, exactly-once side effects,
cancellation, repository cloning, worktrees, scheduling,
multiple control-plane replicas, non-loopback serving, TLS, and production web
authentication.
