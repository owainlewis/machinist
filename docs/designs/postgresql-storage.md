# Spec 2: PostgreSQL Storage Backend & Multi-Revision Concurrency

*   **Author:** Owain Lewis
*   **Status:** Approved (Goldfish-reviewed)
*   **Date:** 2026-11-26
*   **Issue:** ENG-214
*   **Parent architecture:** [Production Cloud Run & PostgreSQL](production-cloud-run.md)
*   **Prerequisites:** [Spec 1: Identity and Access Control](identity-and-access.md) *(completed)*

--------------------------------------------------------------------------------

## Overview

Machinist's single-binary SQLite store (`.machinist/tickets.db`) remains the zero-setup default for laptops and cheap single-VM deployments. For production team deployments on Google Cloud Run (Spec 3) — where container filesystems are ephemeral and rolling deploys briefly overlap Revision `N-1` and Revision `N` — Machinist needs **PostgreSQL as a first-class storage backend** behind the existing `internal/tickets.Store` API.

This specification defines:
1. **Driver & connection routing (`pgx/v5/stdlib`):** Supporting both SQLite file paths and PostgreSQL URLs (`postgres://` / `postgresql://`, TCP/TLS and Cloud SQL Unix sockets) via `DATABASE_URL` env override or `server.database`, pooler-safe query execution (`QueryExecModeExec`), fail-closed Cloud Run guardrails, and zero secret leakage into `control-plane.json` or logs.
2. **Dialect-neutral SQL across all 19 existing tables (+ 2 new):** Eliminating every SQLite-only construct audited in `internal/tickets/` (`rowid`, `SUM(boolean)`, `group_concat`, `COLLATE NOCASE`, `AUTOINCREMENT`, `INSERT OR IGNORE`, `BLOB`, 32-bit Postgres `REAL`/`INTEGER` pitfalls, `SUM(bigint) -> NUMERIC`, and integer-vs-bool scanning on `probed` / `cost_unknown`).
3. **Full PostgreSQL `READ COMMITTED` concurrency safety:** Global lock ordering (`tickets FOR UPDATE` → `runs FOR UPDATE`), lock-free compare-and-swap `ClaimRun`, atomic `DELETE ... RETURNING` in `ConsumeOAuthTransaction`, guarded `CompleteRun`, `auth_state`-serialized invites, and post-rollback `23505` recovery in `CreateTicket` / `AddInput` / `CreateScopedToken`.
4. **Versioned expand-and-contract migrations:** Monotonic `schema_migrations` table guarded by a single transaction-scoped `pg_advisory_xact_lock` with `SET LOCAL lock_timeout = '5s'`.
5. **Database-backed `leader_leases`:** Coordinating Machinist's six periodic background loops across four worker groups (`engine.Run` — covering `Tick`, `runDeliveries`, and `runPRChecks` — plus `schedule.Run`, `runLinear`, and `runAuthPrune`) across overlapping Cloud Run revisions without session advisory locks.
6. **Dual SQLite + PostgreSQL CI parity:** Running the full `internal/tickets` and `internal/controlplane` `-race` suites against both SQLite and PostgreSQL 16 in `.github/workflows/ci.yml`.

--------------------------------------------------------------------------------

## 1. Configuration, `DATABASE_URL`, and Driver Choice

### Driver: `github.com/jackc/pgx/v5/stdlib`
*(Chosen over `pgxpool` from the initial parent doc so `tickets.Store` keeps a single `*sql.DB` codebase across SQLite and PostgreSQL with one pure-Go `CGO_ENABLED=0` dependency.)*

*   Parse the DSN via `pgx.ParseConfig(dsn)` and explicitly set:
    *   **`cfg.DefaultQueryExecMode = pgx.QueryExecModeExec`** — uses unnamed extended-protocol statements instead of `QueryExecModeCacheStatement`. This is safe behind **PgBouncer / Neon transaction pooling** (zero `prepared statement already exists/does not exist` errors) while preserving typed/binary `BYTEA` binding.
    *   Open via `stdlib.OpenDB(*cfg)`.
*   **Pool sizing (safe for Cloud Run rolling overlap):**
    *   Cloud Run's `--max-instances=1` is **per revision**, so rolling deploys briefly run **2 instances** (`N-1` + `N`).
    *   Default PostgreSQL pool: **`MaxOpenConns = 8`**, `MaxIdleConns = 2`, `ConnMaxIdleTime = 5m`, `ConnMaxLifetime = 30m`. Two overlapping revisions peak at `16` connections — safely under Cloud SQL `db-f1-micro`'s default `max_connections = 25` (~20 usable).
    *   Optional env override `DATABASE_MAX_OPEN_CONNS` (validated `1..100`; invalid values fail startup cleanly).
    *   Startup health ping (`db.PingContext`) with a 10-second timeout completes **before** running migrations and **before** `net.Listen` binds `$PORT`.
*   SQLite pool configuration is unchanged (`SetMaxOpenConns(1)`, `SetMaxIdleConns(1)`, `SetConnMaxIdleTime(0)`, `SetConnMaxLifetime(0)`).

### `DATABASE_URL` Precedence, Connection Formats & Cloud Run Fail-Closed Guard

| Priority | Source | Example |
| --- | --- | --- |
| 1 (highest) | `DATABASE_URL` environment variable | `postgresql://user:pass@host/db?sslmode=require` |
| 2 | `server.database` in `config.yaml` | `.machinist/tickets.db` |
| 3 (default) | Built-in quickstart path | `.machinist/tickets.db` |

**Supported PostgreSQL DSN formats:**
*   **TCP + TLS (Neon / RDS / Cloud SQL Private IP / self-hosted):** `postgresql://user:pass@db.example.com:5432/machinist?sslmode=require`
*   **Cloud SQL Unix socket (Cloud Run `--add-cloudsql-instances`):** `postgres://user:pass@/machinist?host=/cloudsql/PROJECT:REGION:INSTANCE` *(note: empty URL host with `host=` query param)*
*   **PgBouncer / Neon transaction pooler:** standard `postgresql://...` URL — works automatically because `DefaultQueryExecMode = pgx.QueryExecModeExec`.

**Safety rules:**
1. **`config.IsPostgresURL(dsn string) bool`:** Returns `true` when trimmed `dsn` starts with `postgres://` or `postgresql://` (case-insensitive). Supports empty-host Cloud SQL Unix socket URLs.
2. **Bypass filesystem path resolution:**
   *   In `config.applyServerDefaults` (`internal/config/config.go:330-379`): apply `DATABASE_URL` override before path resolution; when `IsPostgresURL(server.Database)` is true, skip `resolveConfigPath`.
   *   In `tickets.Open(dsn)` (`internal/tickets/store.go`): when `IsPostgresURL(dsn)` is true, skip `os.MkdirAll` and `os.Chmod` and route to `openPostgres(dsn)`.
3. **Prevent silent ephemeral SQLite fallback on Cloud Run:**
   *   In `controlplane.New` (`internal/controlplane/server.go`): when `os.Getenv("K_SERVICE") != ""` (always set by Cloud Run) and `!config.IsPostgresURL(cfg.Server.Database)`, **refuse to start** with a clear error telling the operator to set `DATABASE_URL` (preventing silent data loss on a misconfigured secret).
   *   Log the active storage backend at startup (`sqlite: <path>` or `postgres: <redacted>`).
4. **Zero credential leakage:**
   *   `config.RedactDatabaseURL(dsn string) string` strips credentials via `url.Parse` AND falls back to a string-level `://...@` redaction if `url.Parse` fails (e.g. unescaped special chars in passwords). `tickets.Open` sanitizes all parse/connect/ping errors through `RedactDatabaseURL`.
   *   In `internal/cli/reload.go`: guard **both** `writeControlPlaneState` (`reload.go:62-83`) and `findControlPlaneState` (`reload.go:125-129`) with `!config.IsPostgresURL(...)` so PostgreSQL DSNs are never written to `.machinist/control-plane.json` and `filepath.Dir(dsn)` is never called on a URL.
5. **CLI note:** `machinist token`, `machinist invite`, and `machinist user` open the store directly via `config.LoadConfig` (`DATABASE_URL` works identically for them); `machinist reload` is VM-only (Cloud Run config is immutable).

--------------------------------------------------------------------------------

## 2. Timestamp Precision & Dialect Abstraction

### Canonical UTC `TEXT` Timestamps
Machinist's `stamp(t time.Time)` formats every timestamp as fixed-width 9-digit UTC RFC3339Nano (`2006-01-02T15:04:05.000000000Z`).
*   PostgreSQL `TIMESTAMPTZ` only stores **6-digit microsecond** precision and silently truncates nanoseconds, which would break exact nanosecond comparisons in `configForTicket` / `PinnedFactory` (`internal/engine/engine.go:150, 181`) and `sameTime` (`internal/tickets/pulls.go:275`).
*   **Decision:** Store timestamp columns as canonical fixed-width UTC RFC3339Nano `TEXT` on **both** SQLite and PostgreSQL. Preserves nanosecond precision, makes `<`, `>=`, `ORDER BY`, and `substr(ts, 1, 10)` byte-for-byte identical, and eliminates driver timezone/scanning divergence.

### Compile-Time-Safe Dialect Wrapper (`internal/tickets/dialect.go`)
Add `dialect` (`dialectSQLite`, `dialectPostgres`) on `Store`:
*   **`rebind(query string) string`:** No-op on SQLite; on PostgreSQL converts `?` placeholders **outside** single-quoted SQL literals (handling escaped `''`) and `--` / `/* */` comments into `$1, $2, ...`.
*   **`dbQuerier` & `txQuerier`:** Wrap `*sql.DB` and `*sql.Tx` so `QueryContext`, `QueryRowContext`, and `ExecContext` always call `rebind`.
*   Change `inTx` signature to `inTx(ctx context.Context, work func(querier) error) error` (plus `inTxReadOnly(ctx, work)` using `sql.LevelRepeatableRead` + `ReadOnly: true` on Postgres for consistent snapshots in `ListTicketsPage` and `ListBoardTickets`). Passing `querier` instead of raw `*sql.Tx` makes bypassing `rebind` a **compile-time error**.
*   **`s.forUpdate() string`:** Returns `""` on SQLite and `" FOR UPDATE"` on PostgreSQL.
*   **`boolToInt(b bool) int`:** Returns `1` or `0` (explicitly used on `runners.probed` and `run_stages.cost_unknown` writes; reads scan into `int` and convert `v != 0`).
*   **`isUniqueViolation(err error) bool`:** Matches SQLite `UNIQUE constraint failed` (Slice 2.1) and PostgreSQL `*pgconn.PgError` SQLSTATE `23505` (Slice 2.2).

--------------------------------------------------------------------------------

## 3. Exhaustive SQLite → PostgreSQL Portability Fixes

Every live SQLite-only construct across `internal/tickets/` is replaced with portable SQL:

| Location | SQLite-only construct | Portable Replacement (SQLite + PostgreSQL) |
| --- | --- | --- |
| `store.go:575, 1344, 1447`, `progress.go:114` (`CurrentStep`) | `runs.rowid` (4 queries) | Add monotonic `runs.seq` (`BIGSERIAL` on Postgres; `INTEGER NOT NULL DEFAULT 0` backfilled from `rowid` on SQLite). **Dialect-split `INSERT`:** Postgres omits `seq` (uses `BIGSERIAL` lock-free); SQLite computes `(SELECT COALESCE(MAX(seq),0)+1 FROM runs)` inside single-writer `inTx`. Query `ORDER BY seq` / `seq >= (SELECT seq FROM runs WHERE id=?)`. |
| `store.go:1080, 1305` | `tickets.rowid` (2 queries) | Add monotonic `tickets.seq` (`BIGSERIAL` on Postgres; `INTEGER NOT NULL DEFAULT 0` backfilled from `rowid` on SQLite; same dialect-split `INSERT`). Query `ORDER BY seq DESC` / `ORDER BY seq ASC`. |
| `store.go:77` | `ticket_entries.id INTEGER PRIMARY KEY AUTOINCREMENT` | SQLite keeps `INTEGER PRIMARY KEY AUTOINCREMENT`; Postgres uses `BIGSERIAL PRIMARY KEY`. (Zero `LastInsertId()` calls exist.) |
| `store.go:82, 100` | `REAL` (32-bit `float4` on Postgres!) and `INTEGER` (32-bit) for `int64` columns | Postgres v1 DDL declares `cost DOUBLE PRECISION` and `tokens`, `duration_millis`, `sequence`, `seq` as `BIGINT`. All `SUM(tokens)` queries (`stats.go:282, 409`, `usage.go:32`, `ledger.go:34`) use `CAST(COALESCE(SUM(tokens),0) AS BIGINT)` because Postgres `SUM(bigint)` returns `NUMERIC`. |
| `store.go:397-405` | 5 columns added only via `addColumns` (`pr_opened_at`, `first_review_at`, `human_reviews`, `cost_unknown`, `probed`) | Included explicitly in the canonical Postgres v1 DDL. |
| `usage.go:29–31` | `SUM(status='succeeded')`, `SUM(status IN (...))` | `SUM(CASE WHEN status='succeeded' THEN 1 ELSE 0 END)`. |
| `runners.go:67, 186`, `store.go:604, 1577`, `stats.go:685` | Passing Go `bool` to `INTEGER` column / scanning `INTEGER` into `bool` | Explicitly write `boolToInt(b)` (`0`/`1`) and scan into `int` (`!= 0`). |
| `reports.go:39` | `group_concat(r.kind)` | Query `tracker_reports` for the matched ticket IDs in a second portable query (guarded by `if len(jobs) == 0 { return jobs, nil }`) and assemble `job.Reported` in Go. |
| `store.go:102`, `projects.go:18, 40, 47` | `COLLATE NOCASE` + `ON CONFLICT(repo)` + `ORDER BY repo` | Postgres schema uses `projects(repo TEXT PRIMARY KEY, ...)` + `CREATE UNIQUE INDEX idx_projects_repo_lower ON projects(LOWER(repo))`. `AddProject` uses targetless `ON CONFLICT DO NOTHING` (catches both PK and lower-index conflicts); `RemoveProject` uses `WHERE LOWER(repo) = LOWER(?)`; `Projects()` uses `ORDER BY LOWER(repo), repo`. |
| `store.go:103` | `run_patches.body BLOB` | `BLOB` on SQLite, `BYTEA` on Postgres. |
| `store.go:109` | `INSERT OR IGNORE INTO auth_state ...` | `INSERT INTO auth_state (id, bootstrapped_at) VALUES (1, NULL) ON CONFLICT (id) DO NOTHING`. |
| `store.go:2009, 2018` | `coalesce(last_used_at,'') < ?` | `(last_used_at IS NULL OR last_used_at < ?)`. |
| `auth_store.go:991` | `ORDER BY (u.disabled_at IS NOT NULL) ASC` | `ORDER BY CASE WHEN u.disabled_at IS NULL THEN 0 ELSE 1 END ASC, u.created_at ASC, u.id ASC`. |

--------------------------------------------------------------------------------

## 4. Concurrency Design for PostgreSQL `READ COMMITTED`

SQLite's `SetMaxOpenConns(1)` hides write races that surface immediately under PostgreSQL's `READ COMMITTED`. Every write transaction enforces strict concurrency invariants with **one global lock order** to prevent AB-BA deadlocks (`SQLSTATE 40P01`):

> **Global lock order:** `auth_state` (for auth/team ops) | `tickets` (`FOR UPDATE`) → `runs` (`FOR UPDATE`)

1. **Atomic OAuth Transaction Consumption (`auth_store.go:534`):**
   *   Replace `SELECT` + unchecked `DELETE` with single-statement atomic consumption:
       ```sql
       DELETE FROM oauth_transactions WHERE id_hash = ? AND expires_at > ?
       RETURNING provider, state, pkce_verifier, return_to, invite_id, expires_at, created_at
       ```
       (Supported on both SQLite ≥3.35 and PostgreSQL; eliminates callback replay races completely.)
2. **Lock-Free Compare-and-Swap `ClaimRun` (`store.go:1436`):**
   *   Loop up to `maxClaimRetries = 32`:
     1. Page through queued runs (`WHERE status=? AND kind='agent' AND harness IN (...) ORDER BY seq ASC, id ASC`) and pick the first run matching `accept == nil || accept(ticket)`. If none found, return `(nil, nil)`.
     2. In a write tx: lock the ticket (`SELECT ... FROM tickets WHERE id=?` + `s.forUpdate()`) to snapshot `before.UpdatedAt` safely, then execute atomic conditional update:
        ```sql
        UPDATE runs SET status=?, runner=?, lease_expires_at=?, started_at=?
        WHERE id=? AND status=?
        ```
        *   If `RowsAffected() == 0`: another runner claimed or cancelled this run — roll back and retry!
        *   If `RowsAffected() == 1`: update `tickets.updated_at`, mark `ticket_entries.consumed_by_run`, commit, and return `Claim`.
   *   If all 32 retries contend, return `(nil, nil)` (no HTTP 500; runner retries on next poll).
3. **Guarded `CompleteRun` (`store.go:1524`) & Ticket/Run Mutations:**
   *   Lock `tickets` (`FOR UPDATE`) then `runs` (`FOR UPDATE`) via `s.forUpdate()` in `CompleteRun`, `Cancel`, `reply`/`Rerun`, `AddInput`, `QueueRun`, `Apply`, `SetPRState`, `SetShipping`, `StewardRequeue`, and `RenewLease`.
   *   In `CompleteRun`, guard `UPDATE runs SET ... WHERE id=? AND status=?` and check `RowsAffected() == 0` → `ErrRunNotActive` (prevents racing `ExpireLeases`, `Cancel`, or runner retry duplicates).
   *   In `IngestRunEvents` (`store.go:1613`), lock the `runs` row (`FOR UPDATE`) before reading sequence cursor and updating `run_progress`.
4. **Post-Rollback Unique-Violation Recovery (`CreateTicket`, `AddInput`, `CreateScopedToken`):**
   *   Add partial unique index `one_active_ticket_per_schedule ON tickets(source) WHERE source LIKE 'schedule:%' AND state='working'`.
   *   Because PostgreSQL aborts a transaction after `23505`, check `isUniqueViolation(err)` **outside** `inTx` (after rollback):
       *   `CreateTicket`: re-read winning ticket by `SourceKey`, active `(repo, issue_number)`, or active `schedule:*` and return `(existing, false, nil)`.
       *   `AddInput`: re-read ticket and return `(ticket, false, nil)`.
       *   `CreateScopedToken`: map `isUniqueViolation(err)` to `ErrTokenExists` (`409 Conflict`).
5. **Auth & Invite Serialization (`auth_store.go`):**
   *   `CreateInvite`, `RegenerateInvite`, and `RevokeInvite` execute `UPDATE auth_state SET id = id WHERE id = 1` first inside `inTx` (matching `ResolveGitHubLogin` and `UpdateUser`), preventing concurrent duplicate pending invites or reviving disabled users' invites.
6. **Consistent Read Snapshots (`ListTicketsPage`, `ListBoardTickets`):**
   *   Use `inTxReadOnly` (`sql.LevelRepeatableRead`, `ReadOnly: true` on Postgres) so `COUNT(*)`, page rows, and `ticketSummary` observe one consistent snapshot.

--------------------------------------------------------------------------------

## 5. Versioned Expand-and-Contract Migrations

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);
```

*   **PostgreSQL migration protocol (`openPostgres`):**
    1. Acquire a pinned connection (`conn, err := db.Conn(ctx)`; `defer conn.Close()`) with a 30-second startup context timeout.
    2. Begin **one** transaction `tx` and execute:
       *   `SELECT pg_advisory_xact_lock(73310202601)` (transaction-scoped — automatically released on commit/rollback, safe behind PgBouncer/Neon transaction poolers).
       *   `SET LOCAL lock_timeout = '5s'` (so a stalled `ALTER TABLE` fails fast instead of queueing behind Revision `N-1` and blocking all `N-1` traffic).
    3. Ensure `schema_migrations` exists, read `COALESCE(MAX(version), 0)`, and apply every pending migration `v > current` sequentially inside that single advisory-locked transaction (PostgreSQL DDL is transactional; non-transactional DDL like `CREATE INDEX CONCURRENTLY` is forbidden in startup migrations).
    4. Record `(version, stamp(now))` in `schema_migrations` and commit.
    *   Migration v1 creates all 21 tables (including all 5 `addColumns`-only columns, `tickets.seq BIGSERIAL`, `runs.seq BIGSERIAL`, `leader_leases`, `idx_projects_repo_lower`, `one_active_ticket_per_schedule`, `idx_runs_queued_seq`, and `idx_tickets_source_seq`) and seeds `auth_state(id=1)`.
*   **SQLite migration protocol (`openSQLite`):**
    *   Runs idempotent `CREATE TABLE IF NOT EXISTS` + existing `addColumns` (adding `tickets.seq` and `runs.seq` and backfilling `UPDATE tickets SET seq = rowid WHERE seq = 0`, `UPDATE runs SET seq = rowid WHERE seq = 0`), creates `leader_leases`, `schema_migrations`, `idx_projects_repo_lower`, and `one_active_ticket_per_schedule`, and runs legacy SQLite-only `backfillOrigins` + `needs_you` cleanup.
*   **Expand-and-contract rule:** Future migrations must remain additive/compatible with Revision `N-1` during Cloud Run rolling deploys.

--------------------------------------------------------------------------------

## 6. Database-Backed Leader Leases (`leader_leases`)

Machinist runs **six periodic background loops across four worker groups** (`engine.Run` — covering `Tick`, `runDeliveries`, and `runPRChecks` — plus `schedule.Run`, `runLinear`, and `runAuthPrune`). During a Cloud Run rolling deploy, Revision `N-1` and Revision `N` briefly overlap.

### Schema & Store API (`internal/tickets/lease_store.go`)
```sql
CREATE TABLE IF NOT EXISTS leader_leases (
    name             TEXT PRIMARY KEY,
    holder_id        TEXT NOT NULL,
    lease_expires_at TEXT NOT NULL
);
```
*   `AcquireOrRenewLease(ctx, name, holderID string, ttl time.Duration) (bool, error)`:
    ```sql
    INSERT INTO leader_leases(name, holder_id, lease_expires_at) VALUES(?, ?, ?)
    ON CONFLICT(name) DO UPDATE
      SET holder_id = excluded.holder_id, lease_expires_at = excluded.lease_expires_at
      WHERE leader_leases.holder_id = excluded.holder_id
         OR leader_leases.lease_expires_at <= ?
    ```
    Returns `RowsAffected() > 0` (`false, nil` if another holder holds an unexpired lease).
*   `ReleaseLease(ctx, name, holderID string) error`: `DELETE FROM leader_leases WHERE name = ? AND holder_id = ?`.

### Leader Coordinator in `controlplane.Server.Serve`
*   Generates unique process ID `cp_<16 random hex>`. (If `s.store == nil` in unit tests, runs background loops directly without DB leases.)
*   Constants (configurable on `Server` for fast unit tests):
    *   `leaseName = "control-plane"`
    *   `leaseTTL = 45 * time.Second`
    *   `leaseRenewInterval = 15 * time.Second` (when holding leadership)
    *   `leaseAcquireInterval = 2 * time.Second` (when standing by → `<2s` takeover on clean handoff!)
    *   `leaseRPCTimeout = 5 * time.Second` (per acquire/renew call)
*   **Transient error & clock-skew safety:**
    *   Record `attemptStart := now()` *before* each `AcquireOrRenewLease` RPC; on `(true, nil)`, set `leaseValidUntil = attemptStart.Add(leaseTTL)`.
    *   On `(false, nil)` (lease stolen/held elsewhere): cancel `leaderCtx` **immediately** and wait for worker goroutines to exit.
    *   On transient DB error while leading: retry every `2s` rather than thrashing on a single blip, and proactively cancel `leaderCtx` if `now()` reaches `leaseValidUntil - 5s` (5s safety margin absorbs Cloud Run NTP clock skew and prevents split-brain!).
*   **Graceful handoff on `SIGTERM`:**
    *   `leaderCtx` is cancelled immediately in parallel with `httpServer.Shutdown` (see Spec 3 §1.3).
    *   Call `ReleaseLease` (with a `1.5s` timeout) **only after** all four worker groups exit cleanly; if any worker is still stuck at shutdown deadline, log an error and skip `ReleaseLease` so the 45s TTL protects against split-brain.
*   HTTP serving (API, UI, runner poll/heartbeat/events/complete, MCP, OAuth) remains active on **every** revision regardless of leadership.

--------------------------------------------------------------------------------

## 7. Dual SQLite + PostgreSQL Testing & CI

1. **Schema-isolated PostgreSQL test harness:**
   *   When `TEST_POSTGRES_URL` is set, `openStore(t)` (shared across `internal/tickets` and `internal/controlplane` fixtures) creates a fresh schema `CREATE SCHEMA test_<16 hex>` on an admin connection, appends `search_path=test_<16 hex>` as a URL query parameter on the DSN passed to `tickets.Open(testDSN)` (so `pgx/v5/stdlib` sends `search_path` on **every** pooled connection), and runs `DROP SCHEMA IF EXISTS test_<16 hex> CASCADE` in `t.Cleanup` after `store.Close()`.
   *   SQLite-only legacy file-upgrade and trigger tests (`pulls_test.go`, `origin_test.go`, `store_test.go` trigger test) skip cleanly when `TEST_POSTGRES_URL` is set; any raw `store.db.Exec("...?...")` helper calls in `internal/tickets/*_test.go` route through `store.exec` so they work on both backends.
2. **PostgreSQL concurrency test suite:**
   *   8 goroutines concurrently calling `ClaimRun` on 20 queued runs → every run claimed exactly once, zero duplicates, zero errors.
   *   Concurrent `CreateTicket` with same `SourceKey` and same `schedule:*` → exactly 1 ticket created.
   *   Concurrent `ConsumeOAuthTransaction` with same `moauth_` cookie → exactly 1 succeeds, rest get `ErrNotFound`.
   *   Concurrent `CompleteRun` vs `Cancel` / retry → clean `ErrRunNotActive`, zero duplicate entries.
   *   Concurrent `UpdateUser` last-admin demotion/disable → at least 1 active admin survives.
   *   `AcquireOrRenewLease` / `ReleaseLease` contention, clean `<2s` handoff, and TTL failover.
3. **CI workflow (`.github/workflows/ci.yml`):**
   *   Add a `postgres` job (`postgres:16-alpine` service container, required by `check`) running `TEST_POSTGRES_URL=postgres://machinist:machinist@127.0.0.1:5432/machinist_test?sslmode=disable go test -race ./internal/tickets/... ./internal/controlplane/...`.

--------------------------------------------------------------------------------

## 8. Vertical Implementation Slices

| Slice | Scope | Done when |
| --- | --- | --- |
| **Slice 2.1: Dialect-Neutral SQL & Concurrency Fixes** | Add `dialect.go` (`rebind`, `dbQuerier`/`txQuerier`, `forUpdate`, `boolToInt`, SQLite `isUniqueViolation`), convert `inTx` to `func(querier) error` + `inTxReadOnly`, add `tickets.seq` & `runs.seq` (removing all 6 `rowid` queries), rewrite `usage.go` `SUM(CASE...)`, `CAST(SUM(tokens) AS BIGINT)`, `reports.go` second query, `projects.go` `ON CONFLICT DO NOTHING` + `LOWER(repo)` index/ordering, `probed`/`cost_unknown` int conversions, atomic `DELETE ... RETURNING` in `ConsumeOAuthTransaction`, CAS `ClaimRun`, guarded `CompleteRun`, `FOR UPDATE` lock ordering, `auth_state`-locked invites, and `23505`/unique recovery in `CreateTicket`/`AddInput`/`CreateScopedToken`. | Full existing `-race` suite passes on SQLite; zero `rowid`, `group_concat`, or `SUM(boolean)` remain in `internal/tickets/`. |
| **Slice 2.2: `pgx/v5` Driver, Migrations & `DATABASE_URL`** | Add `github.com/jackc/pgx/v5`, implement `openPostgres` (`QueryExecModeExec`, `MaxOpenConns=8`, `DATABASE_MAX_OPEN_CONNS`) + `schema_migrations` + single-tx `pg_advisory_xact_lock` + `lock_timeout='5s'`, wire `DATABASE_URL` + `IsPostgresURL` + `RedactDatabaseURL` in `config.go`, fail-closed Cloud Run `K_SERVICE` SQLite guard, guard `writeControlPlaneState` & `findControlPlaneState` in `reload.go`, add `TEST_POSTGRES_URL` schema-isolated harness + concurrency tests, and wire `postgres` job into `.github/workflows/ci.yml`. | Full `internal/tickets` and `internal/controlplane` `-race` suites pass on both SQLite and PostgreSQL 16 in CI. |
| **Slice 2.3: `leader_leases` Background Coordinator** | Add `leader_leases` table, `AcquireOrRenewLease` / `ReleaseLease`, integrate leader supervisor (`leaseTTL=45s`, `renewInterval=15s`, `acquireInterval=2s`, `5s` RPC timeout + safety margin) into `controlplane.Server.Serve` guarding all 6 loops across 4 worker groups, with handoff/failover tests. | Two control planes sharing one DB never run duplicate background loops; clean release hands off in `<2s`; crash fails over after TTL. |
