# Factory architecture

## Executive summary

Factory is a local-first control plane for repeatable software-engineering
agents. An operator saves a prompt and execution settings as a Task. Running
or scheduling that Task creates a Run with one Session per repository. A
persistent Worker claims each Session, prepares an isolated Git worktree, runs
Pi, Codex, or Claude Code, streams events, and reports one terminal result.

The implementation has four main parts:

- `factory` is the operator CLI. Long-running commands replace themselves with
  the compatible server or Worker executable, while finite commands read the
  loopback HTTP API and never open SQLite or Worker directories.
- `factory-server` owns durable state, scheduling, routing, the HTTP API, and
  the embedded browser UI.
- `factory-worker` owns runtime health, repository caches, worktrees, agent
  processes, and cleanup or retention.
- SQLite stores Tasks, Runs, Sessions, executions, Attempts, events, Workers,
  and repositories.

The operator API is loopback-only. Workers make outbound polling requests to
the server. Remote VM Workers use a separate TLS listener and per-Worker bearer
credential. No server connection into a Worker host is required.

The Cloud Run backend contract is implemented behind immutable execution
profiles and a deterministic fake provider. Real Google Cloud dispatch,
artifacts, and credentials are not implemented yet. The contract keeps Factory
as the source of truth and preserves the persistent Worker path as the built-in
`persistent-auto` default.

### System architecture

```text
Operator browser or `factory` CLI
      |
      | loopback HTTP and JSON
      v
factory-server
  |-- Task scheduler and Run admission
  |-- routing and lease state machine
  |-- embedded React UI
  `-- SQLite
      ^
      | register, claim, heartbeat, events, complete
      | local HTTP or separate authenticated TLS
      |
factory-worker
  |-- stable identity and N slots
  |-- runtime capability probes
  |-- bounded repository cache
  |-- isolated worktrees and manifests
  `-- Pi, Codex, or Claude Code
```

The control plane decides what should run and records what happened. A Worker
decides how to execute one claim safely on its machine. The agent runtime is a
child process and does not receive a control-plane operator credential.

### Dependency hierarchy

```text
cmd/factory         -> internal/factorycli   -> internal/protocol
cmd/factory-server  -> internal/controlplane -> internal/protocol
                    -> web
cmd/factory-worker  -> internal/worker       -> internal/protocol
```

Entry points depend on their runtime package, and both long-running runtime
packages share only protocol types. Worker code must never import control-plane
implementation code. Finite CLI commands depend on the HTTP protocol and cannot
read control-plane or Worker state directly.

## Current product model

### Task

A Task is a reusable definition containing:

- name and prompt;
- runtime: `pi`, `codex`, or `claude-code`;
- timeout and per-Run concurrency limit;
- one or more managed repositories;
- optional cron schedule and IANA timezone;
- mutable generation and archived state.

A Task may also save an execution-profile ID. Missing profile data means
`persistent-auto`, so existing rows need no migration. Manual Run requests may
override the saved profile; scheduled Runs use the saved default.

Updates use an expected generation. Admission snapshots the Task so later
edits do not change existing Runs. A manual run uses an idempotency key.
Scheduled admission polls every ten seconds and preserves the frozen pending
snapshot while retrying a failed admission.

### Run and Session

One Task admission creates one Run and one Session per selected repository.
A Run stores the Task snapshot, immutable execution-profile version, backend,
runtime, provider, model, timeout, resource class, commit-resolution policy,
source (`manual` or `schedule`), schedule time, and aggregate state. A Session
stores the same frozen execution choice with its resolved prompt, repository
identity, assigned Worker, result, and failure state.

Session states are `blocked`, `queued`, `preparing`, `running`, `succeeded`,
`failed`, and `cancelled`. Run state is derived from all Sessions as `blocked`,
`queued`, `running`, `succeeded`, `failed`, `partial`, or `cancelled`.

A Session starts blocked when no eligible Worker can currently accept it. A
later claim can route it when a healthy Worker advertises the runtime and
repository access. Task concurrency limits how many sibling Sessions may be
queued or active at once.

### Execution and Attempt

An Execution is the durable assignment of one Session to one Worker and runtime.
An Attempt is one leased try of that Execution. An explicit retry of a failed or
cancelled Session requeues its Execution, increments retry history, and warns
that external effects may repeat.

An Attempt begins in `preparing`, moves to `running` after the Worker reports
its supervisor identity, then ends as `succeeded`, `failed`, `cancelled`, or
`lost`. It owns ordered bounded events, a bounded result or error, process
identity, and a 30-second lease.

### Worker and repository

A Worker has one durable ID, display name, labels, capacity, health, runtime
capabilities, source access, repository advertisements, and retained-worktree
inventory. One Worker can advertise several runtimes and run 1 to 100 Attempts,
with ten slots by default.

Each fake cloud profile projects into one stable synthetic Worker named
`cloud-run-<profile-id>`. The control plane creates Attempts internally for
that Worker. Synthetic Workers cannot enroll, register, heartbeat, poll claims,
or hold a remote Worker credential.

The control plane owns a catalog of managed GitHub repositories. Eligible
Workers clone them on demand with `gh`, keep at most 100 cache entries, fetch
before an Attempt, and resolve the current base branch and commit. Legacy
static repository paths remain readable through Worker configuration.

## Architectural invariants

1. SQLite and the control plane are the authority for Run and Attempt state.
2. A claim is assigned only to its selected, healthy, online Worker with a ready
   runtime, free capacity, and repository availability.
3. A random lease token owns one active Attempt. The server stores its digest,
   not the token. Active mutations require the matching unexpired lease.
4. Claim request IDs and terminal completion are idempotent. Replays cannot
   create two Attempts or replace a stored terminal outcome.
5. Every runtime starts in a Worker-owned worktree. The supervisor owns its
   process group and enforces cancellation, timeout, lease loss, and parent
   loss.
6. Cleanup fails closed unless repository, manifest, path, branch, process, and
   worktree identity can all be proved.
7. Dirty, failed, cancelled, lost, unpublished, or uncertain worktrees are
   retained for inspection. Clean unchanged or proved-published work may be
   removed.
8. Plain HTTP accepts loopback clients only. Remote Workers require TLS,
   one-time enrollment bound to a stable Worker ID, and a stored bearer
   credential.
9. Task admission snapshots prompt, runtime, repositories, timeout,
   concurrency, generation, and schedule context.
10. Operator builds embed committed `web/dist` assets and do not require Node.js
    at runtime.
11. Finite `factory` commands accept only an explicit-port plain HTTP loopback
    endpoint and read current state through bounded API routes.

## Components

### Operator CLI

`cmd/factory` delegates parsing and finite HTTP work to `internal/factorycli`.
The `status`, `show`, and `workers` commands decode protocol resources and write
either stable tabular output or one JSON value. They do not import SQLite or
Worker packages.

The `server start` and `worker start` commands replace the CLI process with the
matching compatibility executable beside it or on `PATH`. An explicit config
path is passed through the existing `FACTORY_SERVER_CONFIG` or
`FACTORY_WORKER_CONFIG` environment contract. Process replacement preserves the
existing role's signal and shutdown behavior.

### Control plane

`cmd/factory-server` loads optional bootstrap TOML, opens SQLite, applies
embedded migrations, sweeps expired leases, starts the Task scheduler,
serves the local API and UI, and optionally starts the remote Worker TLS
listener. Shutdown stops schedulers first and gives HTTP servers ten seconds.

`internal/controlplane` owns validation, transactions, Run admission, routing,
claiming, leases, event ingestion, completion, cancellation, retry, pagination,
overview aggregates, Worker authentication, and backup or restore validation.

SQLite uses foreign keys, WAL journaling, a five-second busy timeout, and a
bounded connection pool. The default database is
`~/.factory/server/factory.sqlite3`.

The backup path validates a live database and uses `VACUUM INTO` to publish a
mode-`0600` standalone snapshot without replacement. Restore validates a marked
snapshot, rejects SQLite sidecars, applies supported migrations in a private
staging directory, and publishes only a complete destination.

### Worker

`cmd/factory-worker` loads TOML configuration and starts one manager. The
manager:

- creates or loads its stable identity and local credential;
- probes Git, `gh`, and configured runtime readiness;
- registers every ten seconds and polls for claims about every two seconds;
- acquires managed repositories into a bounded local cache;
- renews active leases every ten seconds;
- starts up to the configured number of isolated sessions;
- reconciles manifests, worktrees, and owned process groups after restart.

The supervisor is a subprocess of `factory-worker`. It anchors ownership of the
runtime process group. Unix process-group behavior is required, so Windows
Workers are unsupported.

### Agent runtimes

The Worker launches each runtime non-interactively in the prepared checkout:

- Pi uses `--print --no-session` and captures the final plain-text result.
- Codex uses `codex exec` with JSON events and a last-message file.
- Claude Code uses `claude --print` with streaming JSON.

Runtime output is normalized into the same Attempt event and completion
contract. Event batches are at most 100 events and 256 KiB; each event is at
most 64 KiB; one Attempt stores at most 10 MiB of events. Results are at most
256 KiB and errors at most 64 KiB.

### Browser UI

`web/src` is a React and TypeScript single-page application. It exposes Work,
Tasks, Overview, Workers, and Repositories, with detail views for each
operational resource. Work presents Run history as a board or table and polls
the same-origin API.

`web/dist` is generated, committed, and embedded by `web/embed.go`. The server
uses an SPA fallback, immutable caching for versioned assets, and restrictive
security headers. Node.js is needed only when UI source changes.

## Critical flows

### Task admission

1. The operator runs a Task with a request key, or the scheduler claims a
   due occurrence.
2. The server freezes the Task generation and repository list.
3. One Run and one Session per repository are inserted transactionally.
4. Routing selects compatible Workers where possible. Unroutable Sessions stay
   blocked with a reason.
5. The same manual request key or scheduled occurrence cannot admit duplicate
   Runs.

### Claim and execution

1. A healthy Worker registers capabilities and polls with a fresh claim request
   ID and lease token.
2. In one transaction, the server may materialize a blocked route or reroute a
   queued Session, checks capacity and Task concurrency, creates an Attempt,
   and moves Execution and Session to `preparing`.
3. The Worker acquires or refreshes the repository, resolves its base commit,
   creates a branch and worktree, writes a manifest, then starts the supervisor.
4. The Worker reports process identity and the server moves the lifecycle to
   `running`.
5. Heartbeats extend the lease by 30 seconds and return cancellation state.
6. Ordered events are appended idempotently. Completion verifies the lease and
   stores the terminal result once.
7. The Worker removes proved-safe worktrees and reports retained ones back to
   the control plane.

### Cancellation, lease loss, and retry

Queued or blocked Sessions cancel immediately. Active cancellation is stored on
the Session and Execution, returned by the next heartbeat, and enforced by the
supervisor. If lease renewal fails or the 30-second deadline passes, the
supervisor stops the process group and the control plane marks the Attempt
lost. Startup and periodic sweeps recover expired leases after server failure.

Only failed or cancelled Sessions can be retried. Retry preserves the Session
and Attempt history, selects a currently eligible Worker, and creates the next
Attempt when claimed.

## API and security boundaries

The local listener exposes health plus operator and Worker routes under
`/api/v1`: Workers, repositories, Tasks, Runs, overview, Attempts, and event
history. It rejects non-loopback clients before route handling.

The optional remote listener exposes only health, enrollment exchange, Worker
registration and claims, and the active Attempt lifecycle. Creating an
enrollment remains a local operator action. Enrollment tokens are one-time and
short-lived; exchange installs a per-Worker credential. Attempt routes also
check that the authenticated Worker owns the Attempt.

Factory is a trusted single-operator system. It has no multi-user tenant model.
Agents may execute repository code using credentials already available on the
Worker host. Worktrees isolate Git state, not hostile code. The product must not
describe a Worker as a security sandbox.

## Persistence and migration

Migrations are embedded from `migrations/` and applied in order. Migration 27
introduces the current lifecycle model. Migration 28 adds the current
single-claim protocol and rejects incompatible old Workers; that protocol is at
version 2, because the claim payload replaced its target field with a Session.
Migration 30 renames the operator model to Tasks, Runs, and Sessions without
changing behaviour, and refuses to apply if the new table names are already in
use. Supported legacy
Definitions, schedules, repositories, and execution history are converted;
unsupported legacy provider admission is blocked and reported rather than
silently discarded.

Current lifecycle tables include `tasks`, `task_repositories`, `runs`,
`sessions`, `executions`, `attempts`, `attempt_events`, `workers`,
`repositories`, Worker repository state, claim request deduplication, and
Worker enrollment or credentials. Older migration tables may remain for
history and upgrade compatibility but are not part of the current UI or
admission path.

## Known limitations

- Only the embedded SQLite orchestration path exists.
- Managed repository acquisition supports GitHub through `gh`.
- The current Worker resolves a repository's base commit during Attempt
  preparation, so a later retry can observe a newer default branch commit.
- Remote Workers require operator-managed TLS certificates and enrollment.
- Windows Workers are unsupported.
- Execution isolates worktrees and process groups but does not sandbox hostile
  repository code or network egress.

## Source map

| Area | Primary files |
| --- | --- |
| Operator CLI | `cmd/factory/main.go`, `internal/factorycli/command.go`, `internal/factorycli/client.go` |
| Server startup and config | `cmd/factory-server/main.go`, `cmd/factory-server/config.go` |
| HTTP routes and auth | `internal/controlplane/http.go`, `internal/controlplane/worker_auth.go` |
| Task and Run model | `internal/controlplane/tasks.go`, `internal/protocol/tasks.go` |
| Schedule admission | `internal/controlplane/task_scheduler.go`, `internal/controlplane/schedule_cron.go` |
| Routing and claims | `internal/controlplane/task_claim.go`, `internal/controlplane/state.go` |
| Lease sweep and recovery | `internal/controlplane/server.go`, `internal/controlplane/recovery.go` |
| Worker manager | `internal/worker/manager.go`, `internal/worker/registration.go`, `internal/worker/claiming.go` |
| Attempt execution | `internal/worker/attempt_lifecycle.go`, `internal/worker/supervisor.go`, `internal/worker/events.go` |
| Git and worktrees | `internal/worker/git.go`, `internal/worker/repository_cache.go`, `internal/worker/reconcile.go` |
| Protocol limits and types | `internal/protocol/types.go`, `internal/protocol/prompt.go` |
| Schema | `migrations/027_routines_work.sql`, `migrations/028_work_claim_protocol.sql`, `migrations/030_task_run_session.sql` |
| Browser UI | `web/src/App.tsx`, `web/src/Tasks.tsx`, `web/src/Runs.tsx`, `web/src/Workers.tsx`, `web/src/Repositories.tsx` |

## Verification

- `go test ./cmd/... ./internal/...` covers entry points, CLI routing and output,
  API contracts, storage, Worker lifecycle, and release construction.
- `just boundary` proves Worker code does not import control-plane code.
- `just test-tooling` proves the Node-free build produces the complete operator
  binary set.
- `just test-launcher` proves server and Worker readiness and signal handling.
- `just test-release` proves archive contents, metadata, reproducibility, and
  native execution.
