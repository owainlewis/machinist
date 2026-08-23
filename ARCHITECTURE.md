# Factory architecture

> **Status:** Current implementation
>
> **Verification basis:** implementation and tests in this repository

## Executive summary

Factory is a local-first control plane for repeatable software-engineering
agents. An operator can save a prompt and execution settings as a Task, or use
`factory build` to admit up to 100 existing work-item references in one Run, or
run one saved Procedure across up to 100 enabled managed repositories. Each
target becomes independent Session-backed Work. A persistent Worker claims the
Work, prepares an isolated Git worktree, runs Pi, Codex, or Claude Code, streams
events, and reports one terminal result.

The implementation has four main parts:

- `factory` is the operator CLI. Long-running commands replace themselves with
  the compatible server or Worker executable, while finite commands read the
  loopback HTTP API and never open SQLite or Worker directories.
- `factory-server` owns durable state, scheduling, routing, the HTTP API, and
  the embedded browser UI.
- `factory-worker` owns runtime health, repository caches, worktrees, agent
  processes, and cleanup or retention.
- SQLite stores Tasks, Runs, Session-backed Work, durable Work updates,
  executions, Attempts, events, Workers, and repositories.

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
  |-- Task scheduler, Build admission, and Procedure fleet admission
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
commands and browser -> HTTP API -> control-plane store -> SQLite
factory-worker       -> typed protocol -> control-plane store
factory-worker       -> runtime adapters -> agent child processes
```

Entry points depend on their runtime package, and both long-running runtime
packages share only protocol types. Product and Worker code depend on
`internal/protocol`; Worker code must never import `internal/controlplane`.
Commands and the browser use the HTTP API, and only the control plane writes
lifecycle state to SQLite. Work is user-facing lifecycle truth. Execution and
Attempt remain process and lease truth. Finite CLI commands cannot read
control-plane or Worker state directly.

## Current product model

### Task

A Task is the stored form of a saved Procedure. It contains:

- name and prompt;
- runtime: `pi`, `codex`, or `claude-code`;
- timeout and per-Run concurrency limit;
- one or more managed repositories;
- optional cron schedule and IANA timezone;
- mutable generation and archived state;
- an outcome contract: `process_exit` or `agent_update`.

A Task may also save an execution-profile ID. Missing profile data means
`persistent-auto`, so existing rows need no migration. Manual Run requests may
override the saved profile; scheduled Runs use the saved default.

Existing and newly created Tasks default to `process_exit`. An explicit
conversion to `agent_update` increments the generation and requires the
persistent backend. Updates use an expected generation. Admission snapshots
the Task and outcome contract so later edits do not change existing Runs. A
manual run uses an idempotency key.
Scheduled admission polls every ten seconds and preserves the frozen pending
snapshot while retrying a failed admission.

### Run and Session

One Task admission creates one Run and one Session-backed Work record per
selected repository. A Run stores the Task snapshot, immutable execution
profile version, backend, runtime, provider, model, timeout, resource class,
commit-resolution policy, outcome contract, ordered target snapshot, source
(`manual` or `schedule`), schedule time, and aggregate state. Work stores the
same frozen execution choice with target identity, source reference, context,
stable publish branch, repository identity, ownership, waiting reason,
progress, checkpoint, pending resume, pull-request evidence, predecessor,
result, and terminal fields.

Work can represent `queued`, `running`, `needs-input`, `ready`, `succeeded`,
`failed`, `no-change`, and `cancelled`. The backing Session table also retains
the compatibility routing states `blocked` and `preparing`. Run state and
counts are derived from Work as `blocked`, `queued`, `running`, `succeeded`,
`failed`, `partial`, or `cancelled`.

Work updates store typed status, actor, request, Attempt, sequence, message,
checkpoint, and pull-request fields. Storage allows at most 199 progress
updates and reserves one outcome update per Attempt. Progress messages are at
most 2 KiB and outcome messages are at most 8 KiB.

A Session starts blocked when no eligible Worker can currently accept it. A
later claim can route it when a healthy Worker advertises the runtime and
repository access. Procedure concurrency limits how many sibling Sessions may
be queued or active at once. Claim ordering uses each Run's prior Attempt count,
then admission order, so a large Run yields to an older compatible Run that has
received less service.

### Build admission

`factory build` accepts 1 to 100 ordered HTTPS GitHub issue URLs or opaque
references. Opaque references require `--repo`. An explicit repository must
also match every GitHub URL, while an omitted repository permits a GitHub-only
batch across managed repositories. The server resolves repository state and
rejects the whole batch before writing when any target is invalid.

Every Build freezes generation 1 of the built-in `standard-build` Procedure,
the configured default or explicit runtime, persistent execution settings, and
one immutable target and publish branch per Work. The Procedure text is trusted
policy. References are labelled as untrusted context and are not fetched by the
CLI or control plane.

The caller fingerprint covers ordered normalized references, explicit option
presence and values, and the rebuild flag. Request-key lookup and fingerprint
comparison happen before repository, runtime-default, duplicate, or predecessor
reads. Matching replay returns the original Run. Active matching Work blocks a
duplicate. A rebuild requires a new key and the latest terminal predecessor for
every target, selected in the same transaction.

### Procedure fleet admission

`factory procedures` reads saved Procedures, including archived entries, from
the bounded local API. `factory run PROCEDURE --repos ...|all` selects explicit
enabled managed repositories in command order or freezes all enabled managed
repositories in repository-identity order. The server snapshots the current
Procedure generation, prompt, runtime, timeout, concurrency, outcome contract,
execution choice, and ordered targets in one transaction.

The caller fingerprint covers the normalized Procedure name, ordered repository
selectors or `all`, and the rebuild flag. Request-key replay happens before
Procedure, repository, execution-profile, or predecessor reads. A rebuild uses
a new key and records the latest terminal predecessor for the exact Procedure
and repository. Existing scheduled Tasks continue to use their saved repository
selection and schedule snapshot.

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
9. Task admission snapshots prompt, runtime, ordered targets, timeout,
   concurrency, generation, outcome contract, execution choice, and schedule
   context.
10. Operator builds embed committed `web/dist` assets and do not require Node.js
    at runtime.
11. Finite `factory` commands accept only an explicit-port plain HTTP loopback
    endpoint and read current state through bounded API routes.
12. Claim protocol version 3 gates every persistent Worker claim. Older Workers
    receive `worker_upgrade_required`, including for process-exit Work.
13. Build and Procedure fleet admission are all-or-none. Exact request-key
    replay wins before mutable configuration reads, and one Run cannot contain
    a duplicate target.
14. An implicit admission key is written durably before HTTP submission and is
    not removed until an authoritative admitted, replayed, or pre-commit
    rejection result has been written and flushed.

## Components

### Operator CLI

`cmd/factory` delegates parsing and finite HTTP work to `internal/factorycli`.
The `build`, `run`, `procedures`, `status`, `show`, and `workers` commands use
typed protocol resources and write either stable tabular output or one JSON
value. They do not import SQLite or Worker packages. Build and Procedure Run
syntax normalization is local, but managed-state resolution belongs to the
server. An injected `factory update` command instead connects only to a private
Attempt-scoped Unix socket using the Work ID, Attempt ID, and update token
supplied by the Worker.

When no Build or Procedure Run key is supplied, the CLI journals a random key
under the private operator data directory before sending. A nonblocking OS lock
scopes concurrent submissions by endpoint and caller fingerprint. Lost
responses retain the key for replay by a later CLI process. The journal holds
at most 100 uncertain requests and never evicts one silently.

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

Only a frozen `agent_update` Attempt receives a private update socket and
token. The Worker validates that capability locally, resolves ready pull
request evidence with GitHub and Git, and forwards a typed update under its own
lease. The agent-facing request never contains an operator credential, Worker
credential, or Attempt lease token. Outcome reports ask the supervisor to stop
the process group, then the Worker completes the Attempt only after verified
process stop and any required delivery postflight check.

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
2. The server freezes the Task generation, outcome contract, execution choice,
   and ordered target list.
3. One Run and one Session-backed Work record per repository are inserted
   transactionally.
4. Routing selects compatible Workers where possible. Unroutable Sessions stay
   blocked with a reason.
5. The same manual request key or scheduled occurrence cannot admit duplicate
   Runs.

### Build admission

1. The CLI normalizes reference syntax and computes the caller fingerprint.
2. For an implicit key, it acquires the endpoint/fingerprint lock and durably
   journals a random request key before sending one HTTP request.
3. The server checks the key and fingerprint before mutable reads. Exact replay
   returns the stored Run and different input conflicts.
4. For a new key, one transaction resolves every enabled managed repository,
   rejects duplicates, applies active-Work and rebuild guards, and freezes the
   standard Procedure and runtime.
5. The transaction inserts one Run and ordered independent Work targets. Work
   routes immediately when possible or remains visibly blocked for a later
   scheduler claim. The CLI never starts agents.
6. The CLI clears an implicit journal entry only after it flushes an admitted,
   replayed, or typed pre-commit rejection result. Transport, server, malformed
   response, timeout, interruption, and output errors leave the key pending.

### Procedure fleet admission

1. The CLI normalizes the Procedure name and ordered repository selectors, or
   the `all` selector, then computes a caller fingerprint.
2. Explicit and generated request keys use the same lock, durable journal,
   replay, typed rejection, output flush, and cleanup contract as Build.
3. The server checks the key and fingerprint before any mutable read. Exact
   replay returns the frozen Run even after Procedure or repository changes.
4. For a new key, one transaction loads the active Procedure, resolves the
   explicit enabled repositories or complete enabled set, validates the frozen
   execution backend, and selects exact Procedure-and-repository predecessors
   for a rebuild.
5. The transaction inserts one Run and one ordered repository Work target per
   selection. Each target keeps independent scheduling, Attempts, cancellation,
   retries, updates, and outcomes.

### Claim and execution

1. A healthy Worker registers capabilities and polls with a fresh claim request
   ID and lease token.
2. In one transaction, the server may materialize a blocked route or reroute a
   queued Session, checks capacity and Procedure concurrency, applies run-aware
   fair ordering, creates an Attempt, and moves Execution and Session to
   `preparing`.
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
`/api/v1`: Builds, Workers, repositories, Tasks, Runs, overview, Attempts, and
event history. It rejects non-loopback clients before route handling.

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
introduces the current lifecycle model. Migration 28 adds the single-claim
protocol and rejects incompatible old Workers. Migration 30 renames the
operator model to Tasks, Runs, and Sessions without changing behaviour, and
refuses to apply if the new table names are already in use. Migration 31 adds
the durable Work lifecycle, ordered Run targets, outcome contracts, bounded
Work updates, and claim protocol version 3. It preserves existing rows as
`process_exit`. Supported legacy
Definitions, schedules, repositories, and execution history are converted;
unsupported legacy provider admission is blocked and reported rather than
silently discarded.

Current lifecycle tables include `tasks`, `task_repositories`, `runs`,
`sessions`, `work_updates`, `executions`, `attempts`, `attempt_events`, `workers`,
`repositories`, Worker repository state, claim request deduplication, and
Worker enrollment or credentials. Older migration tables may remain for
history and upgrade compatibility but are not part of the current UI or
admission path.

## Known limitations

- Only the embedded SQLite orchestration path exists.
- Cloud Run execution profiles and elastic dispatch are designed but not
  implemented.
- Durable agent-update fields, persistent-backend admission, and missing-outcome
  failure enforcement exist. Worker-local Work-update transport, write
  endpoints, successful semantic completion, resumable questions, and operator
  Work commands are not implemented yet. Existing execution remains
  process-exit-based.
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
| Build admission and journal | `internal/controlplane/build.go`, `internal/factorycli/build.go`, `internal/factorycli/admission_journal.go`, `internal/protocol/build.go` |
| Procedure fleet admission | `internal/controlplane/procedures.go`, `internal/factorycli/procedures.go`, `internal/protocol/procedures.go` |
| Server startup and config | `cmd/factory-server/main.go`, `cmd/factory-server/config.go` |
| HTTP routes and auth | `internal/controlplane/http.go`, `internal/controlplane/worker_auth.go` |
| Task, Run, and Work model | `internal/controlplane/tasks.go`, `internal/controlplane/work.go`, `internal/protocol/tasks.go` |
| Schedule admission | `internal/controlplane/task_scheduler.go`, `internal/controlplane/schedule_cron.go` |
| Routing and claims | `internal/controlplane/task_claim.go`, `internal/controlplane/state.go` |
| Lease sweep and recovery | `internal/controlplane/server.go`, `internal/controlplane/recovery.go` |
| Worker manager | `internal/worker/manager.go`, `internal/worker/registration.go`, `internal/worker/claiming.go` |
| Attempt execution | `internal/worker/attempt_lifecycle.go`, `internal/worker/supervisor.go`, `internal/worker/events.go` |
| Git and worktrees | `internal/worker/git.go`, `internal/worker/repository_cache.go`, `internal/worker/reconcile.go` |
| Protocol limits and types | `internal/protocol/types.go`, `internal/protocol/prompt.go` |
| Schema | `migrations/027_routines_work.sql`, `migrations/030_task_run_session.sql`, `migrations/031_work_lifecycle.sql` |
| Browser UI | `web/src/App.tsx`, `web/src/Tasks.tsx`, `web/src/Runs.tsx`, `web/src/Workers.tsx`, `web/src/Repositories.tsx` |

## Verification

- `internal/controlplane/work_lifecycle_test.go` proves outcome-contract
  freezing, backend compatibility, Work states, bounded update history,
  replacement guards, ordered targets, and legacy prompt limits.
- `internal/controlplane/build_test.go`, `internal/protocol/build_test.go`, and
  `internal/factorycli/build_test.go` prove atomic Build admission,
  normalization, replay ordering, runtime freezing, duplicate and rebuild
  guards, scheduler claim, typed commit status, journal recovery, locking, and
  wait exit codes.
- `internal/controlplane/procedures_test.go`,
  `internal/protocol/procedures_test.go`, and
  `internal/factorycli/procedures_test.go` prove Procedure listing, explicit and
  all-repository selection, atomic freezing, replay before mutable reads,
  rebuild lineage, journal recovery, and fair cross-Run claims.
- `internal/controlplane/tasks_migration_test.go` opens populated historical
  databases and proves identity, lifecycle, scheduled process-exit completion,
  and foreign-key preservation.
- `go test ./cmd/... ./internal/...` covers entry points, CLI routing and output,
  API contracts, storage, Worker lifecycle, and release construction.
- `just format-check`, `just vet`, `just boundary`, and `just test` provide the
  repository-wide formatting, static-analysis, dependency, and test proof.
- `just test-tooling` proves the Node-free build produces the complete operator
  binary set.
- `just test-launcher` proves server and Worker readiness and signal handling.
- `just test-release` proves archive contents, metadata, reproducibility, and
  native execution.
