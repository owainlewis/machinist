package tickets

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/owainlewis/machinist/internal/config"
)

const (
	// DatabaseMaxOpenConnsEnv overrides the default PostgreSQL connection pool size.
	DatabaseMaxOpenConnsEnv = "DATABASE_MAX_OPEN_CONNS"

	defaultPostgresMaxOpenConns    = 8
	defaultPostgresMaxIdleConns    = 2
	minPostgresMaxOpenConns        = 1
	maxPostgresMaxOpenConns        = 100
	defaultPostgresConnMaxIdleTime = 5 * time.Minute
	defaultPostgresConnMaxLifetime = 30 * time.Minute
	postgresPingTimeout            = 10 * time.Second
	postgresMigrationTimeout       = 30 * time.Second
	postgresMigrationLockID        = int64(73310202601)
)

type postgresMigration struct {
	version    int
	statements []string
}

var postgresMigrations = []postgresMigration{
	{
		version: 1,
		statements: []string{
			`CREATE TABLE IF NOT EXISTS tickets (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  repo TEXT NOT NULL,
  issue_number INTEGER,
  title TEXT NOT NULL,
  state TEXT NOT NULL,
  agent TEXT NOT NULL,
  pr_number INTEGER,
  created_by TEXT NOT NULL,
  human_commits INTEGER,
  merged_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  pr_state TEXT,
  pr_state_at TEXT,
  origin TEXT NOT NULL DEFAULT '',
  source_url TEXT NOT NULL DEFAULT '',
  shipping TEXT,
  pr_opened_at TEXT,
  first_review_at TEXT,
  human_reviews INTEGER,
  merge_gate_decision TEXT NOT NULL DEFAULT '',
  merge_gate_risk TEXT NOT NULL DEFAULT '',
  merge_gate_reason TEXT NOT NULL DEFAULT '',
  steward_retries INTEGER NOT NULL DEFAULT 0,
  seq BIGSERIAL NOT NULL)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS one_active_ticket_per_issue ON tickets(repo, issue_number)
  WHERE issue_number IS NOT NULL AND state IN ('working','needs_you','pr_ready')`,
			`CREATE UNIQUE INDEX IF NOT EXISTS one_active_ticket_per_schedule ON tickets(source)
  WHERE source LIKE 'schedule:%' AND state='working'`,
			`CREATE INDEX IF NOT EXISTS tickets_created_at ON tickets(created_at DESC, id DESC)`,
			`CREATE INDEX IF NOT EXISTS tickets_updated_at ON tickets(updated_at DESC, id DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_tickets_source_seq ON tickets(source, seq DESC)`,
			`CREATE TABLE IF NOT EXISTS ticket_entries (
  id BIGSERIAL PRIMARY KEY,
  ticket_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  author TEXT NOT NULL,
  body TEXT NOT NULL,
  source_key TEXT UNIQUE,
  created_at TEXT NOT NULL,
  consumed_by_run TEXT,
  posted_at TEXT)`,
			`CREATE INDEX IF NOT EXISTS ticket_entries_ticket ON ticket_entries(ticket_id, id)`,
			`CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  ticket_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  reviewer TEXT NOT NULL DEFAULT '',
  harness TEXT NOT NULL,
  runner TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  lease_expires_at TEXT,
  tokens BIGINT,
  cost DOUBLE PRECISION,
  report TEXT,
  queued_at TEXT,
  started_at TEXT,
  ended_at TEXT,
  cancel_requested_at TEXT,
  seq BIGSERIAL NOT NULL)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS one_active_agent_run ON runs(ticket_id)
  WHERE kind='agent' AND status IN ('queued','running')`,
			`CREATE INDEX IF NOT EXISTS runs_status ON runs(status)`,
			`CREATE INDEX IF NOT EXISTS runs_ticket_status ON runs(ticket_id, status)`,
			`CREATE INDEX IF NOT EXISTS idx_runs_queued_seq ON runs(status, kind, seq, id)`,
			`CREATE TABLE IF NOT EXISTS tokens (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  last_used_at TEXT,
  revoked_at TEXT,
  role TEXT NOT NULL DEFAULT '',
  prefix TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL DEFAULT '')`,
			`CREATE UNIQUE INDEX IF NOT EXISTS one_active_token_per_name ON tokens(name) WHERE revoked_at IS NULL`,
			`CREATE TABLE IF NOT EXISTS runners (
  name TEXT PRIMARY KEY,
  kind TEXT NOT NULL DEFAULT '',
  harnesses TEXT NOT NULL DEFAULT '',
  slots INTEGER NOT NULL DEFAULT 1,
  last_contact TEXT NOT NULL,
  probed INTEGER NOT NULL DEFAULT 0)`,
			`CREATE TABLE IF NOT EXISTS source_cursors (
  source TEXT NOT NULL,
  repo TEXT NOT NULL,
  cursor TEXT NOT NULL,
  PRIMARY KEY(source, repo))`,
			`CREATE TABLE IF NOT EXISTS tracker_reports (
  ticket_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  reported_at TEXT NOT NULL,
  PRIMARY KEY(ticket_id, kind))`,
			`CREATE TABLE IF NOT EXISTS ignored_source_keys (
  key TEXT PRIMARY KEY,
  created_at TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS run_activity_events (
  run TEXT NOT NULL,
  sequence BIGINT NOT NULL,
  body TEXT NOT NULL,
  PRIMARY KEY(run, sequence))`,
			`CREATE TABLE IF NOT EXISTS run_progress (
  run TEXT PRIMARY KEY,
  step TEXT NOT NULL DEFAULT '',
  step_started_at TEXT,
  action TEXT NOT NULL DEFAULT '',
  partial TEXT NOT NULL DEFAULT '')`,
			`CREATE TABLE IF NOT EXISTS run_stages (
  run TEXT NOT NULL,
  position INTEGER NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  state TEXT NOT NULL,
  outcome TEXT NOT NULL DEFAULT '',
  tokens BIGINT,
  cost DOUBLE PRECISION,
  cost_unknown INTEGER NOT NULL DEFAULT 0,
  duration_millis BIGINT NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  agents TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(run, position))`,
			`CREATE TABLE IF NOT EXISTS projects (
  repo TEXT PRIMARY KEY,
  added_by TEXT NOT NULL,
  added_at TEXT NOT NULL)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_repo_lower ON projects(LOWER(repo))`,
			`CREATE TABLE IF NOT EXISTS run_patches (
  run TEXT PRIMARY KEY,
  body BYTEA NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS human_touches (
  ticket_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  author TEXT NOT NULL,
  at TEXT NOT NULL)`,
			`CREATE INDEX IF NOT EXISTS human_touches_ticket ON human_touches(ticket_id, at)`,
			`CREATE TABLE IF NOT EXISTS auth_state (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  bootstrapped_at TEXT)`,
			`INSERT INTO auth_state (id, bootstrapped_at) VALUES (1, NULL) ON CONFLICT (id) DO NOTHING`,
			`CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  role TEXT NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
  github_id TEXT UNIQUE CHECK (github_id IS NULL OR github_id != ''),
  github_login TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  avatar_url TEXT NOT NULL DEFAULT '',
  invited_by TEXT NOT NULL DEFAULT '',
  disabled_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS invites (
  id TEXT PRIMARY KEY,
  role TEXT NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
  github_login TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL UNIQUE,
  created_by TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  accepted_by TEXT REFERENCES users(id),
  accepted_at TEXT,
  revoked_at TEXT,
  created_at TEXT NOT NULL,
  CHECK (github_login != '' OR email != ''))`,
			`CREATE INDEX IF NOT EXISTS idx_invites_github_login ON invites(github_login) WHERE accepted_at IS NULL AND revoked_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS idx_invites_email ON invites(email) WHERE accepted_at IS NULL AND revoked_at IS NULL`,
			`CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  auth_method TEXT NOT NULL CHECK (auth_method IN ('github', 'token')),
  user_id TEXT REFERENCES users(id),
  token_id TEXT REFERENCES tokens(id),
  user_agent TEXT NOT NULL DEFAULT '',
  ip_address TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  idle_expires_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked_at TEXT,
  CHECK (
    (auth_method = 'github' AND user_id IS NOT NULL AND token_id IS NULL) OR
    (auth_method = 'token' AND token_id IS NOT NULL AND user_id IS NULL)
  ))`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id, revoked_at)`,
			`CREATE INDEX IF NOT EXISTS idx_sessions_token ON sessions(token_id, revoked_at)`,
			`CREATE TABLE IF NOT EXISTS oauth_transactions (
  id_hash TEXT PRIMARY KEY,
  provider TEXT NOT NULL CHECK (provider IN ('github')),
  state TEXT NOT NULL,
  pkce_verifier TEXT NOT NULL,
  return_to TEXT NOT NULL DEFAULT '/',
  invite_id TEXT REFERENCES invites(id),
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS leader_leases (
  name TEXT PRIMARY KEY,
  holder_id TEXT NOT NULL,
  lease_expires_at TEXT NOT NULL)`,
		},
	},
	{
		version: 2,
		statements: []string{
			`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS harness TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT ''`,
			`CREATE TABLE IF NOT EXISTS automations (
  name TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  preset TEXT NOT NULL,
  prompt TEXT NOT NULL,
  schedule TEXT NOT NULL DEFAULT '',
  timezone TEXT NOT NULL DEFAULT '',
  harness TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL)`,
		},
	},
	{
		version: 3,
		statements: []string{
			`ALTER TABLE runners ADD COLUMN IF NOT EXISTS labels TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS runs_on TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE automations ADD COLUMN IF NOT EXISTS runs_on TEXT NOT NULL DEFAULT ''`,
		},
	},
	{
		version: 4,
		statements: []string{
			`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS resume_from_step TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS resume_checkpoint_run TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE runs ADD COLUMN IF NOT EXISTS failed_step TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE runs ADD COLUMN IF NOT EXISTS resume_from_step TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE runs ADD COLUMN IF NOT EXISTS checkpoint_run_id TEXT NOT NULL DEFAULT ''`,
			`CREATE TABLE IF NOT EXISTS run_checkpoints (
  run TEXT PRIMARY KEY,
  failed_step TEXT NOT NULL DEFAULT '',
  head_sha TEXT NOT NULL DEFAULT '',
  base_ref TEXT NOT NULL DEFAULT '',
  patch BYTEA,
  worktree BYTEA,
  control_files TEXT NOT NULL DEFAULT '')`,
		},
	},
}

func postgresMaxOpenConns() (int, error) {
	raw := strings.TrimSpace(os.Getenv(DatabaseMaxOpenConnsEnv))
	if raw == "" {
		return defaultPostgresMaxOpenConns, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < minPostgresMaxOpenConns || n > maxPostgresMaxOpenConns {
		return 0, fmt.Errorf("%s must be an integer between %d and %d (got %q)", DatabaseMaxOpenConnsEnv, minPostgresMaxOpenConns, maxPostgresMaxOpenConns, raw)
	}
	return n, nil
}

func openPostgres(dsn string) (*Store, error) {
	redacted := config.RedactDatabaseURL(dsn)
	maxOpen, err := postgresMaxOpenConns()
	if err != nil {
		return nil, err
	}
	cfg, err := pgx.ParseConfig(strings.TrimSpace(dsn))
	if err != nil {
		return nil, fmt.Errorf("parse postgres database URL (%s): %w", redacted, sanitizeDSNError(err, dsn, redacted))
	}
	// QueryExecModeExec uses unnamed extended-protocol statements, avoiding
	// prepared-statement collisions behind PgBouncer/Neon transaction poolers
	// while preserving typed/binary BYTEA binding.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec

	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(maxOpen)
	maxIdle := min(defaultPostgresMaxIdleConns, maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxIdleTime(defaultPostgresConnMaxIdleTime)
	db.SetConnMaxLifetime(defaultPostgresConnMaxLifetime)

	pingCtx, cancelPing := context.WithTimeout(context.Background(), postgresPingTimeout)
	defer cancelPing()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to postgres database (%s): %w", redacted, sanitizeDSNError(err, dsn, redacted))
	}

	if err := migratePostgres(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate postgres database (%s): %w", redacted, sanitizeDSNError(err, dsn, redacted))
	}
	return &Store{db: db, dialect: dialectPostgres, now: time.Now}, nil
}

func sanitizeDSNError(err error, rawDSN, redacted string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	trimmed := strings.TrimSpace(rawDSN)
	if trimmed != "" && strings.Contains(msg, trimmed) {
		return fmt.Errorf("%s", strings.ReplaceAll(msg, trimmed, redacted))
	}
	return err
}

func migratePostgres(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), postgresMigrationTimeout)
	defer cancel()

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, postgresMigrationLockID); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return fmt.Errorf("set migration lock_timeout: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}

	var current int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read current schema version: %w", err)
	}

	nowStamp := stamp(time.Now().UTC())
	for _, m := range postgresMigrations {
		if m.version <= current {
			continue
		}
		for i, stmt := range m.statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migration v%d statement %d: %w", m.version, i+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES($1, $2)`, m.version, nowStamp); err != nil {
			return fmt.Errorf("record migration v%d: %w", m.version, err)
		}
	}
	return tx.Commit()
}
