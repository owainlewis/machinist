// Package tickets stores tickets, their log, their runs and API tokens.
package tickets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/protocol"
	_ "modernc.org/sqlite"
)

const (
	StateWorking   = "working"
	StatePRReady   = "pr_ready"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"

	// A pull request's state, as Machinist last read it from GitHub.
	PROpen   = "open"
	PRMerged = "merged"
	PRClosed = "closed"

	RunKindAgent = "agent"

	RunQueued      = "queued"
	RunRunning     = "running"
	RunSucceeded   = "succeeded"
	RunFailed      = "failed"
	RunCancelled   = "cancelled"
	RunTimedOut    = "timed_out"
	RunInterrupted = "interrupted"

	EntryRequest = "request"
	EntryComment = "comment"
	EntryReview  = "review"
	EntryCI      = "ci"
	EntryReport  = "report"
	EntryMessage = "message"

	authorMachinist = "machinist"

	sourceSchedulePrefix = "schedule:"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrRunNotActive = errors.New("run is not active")
	ErrTicketEnded  = errors.New("ticket has ended")
	ErrNotWorking   = errors.New("ticket is not working")
	ErrTokenExists  = errors.New("an active token with this name exists")
)

const schema = `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS tickets (
  id TEXT PRIMARY KEY, source TEXT NOT NULL, repo TEXT NOT NULL, issue_number INTEGER, title TEXT NOT NULL,
  state TEXT NOT NULL, agent TEXT NOT NULL, pr_number INTEGER, created_by TEXT NOT NULL, human_commits INTEGER,
  merged_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, pr_state TEXT, pr_state_at TEXT,
  origin TEXT NOT NULL DEFAULT '', source_url TEXT NOT NULL DEFAULT '', shipping TEXT);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_ticket_per_issue ON tickets(repo, issue_number)
  WHERE issue_number IS NOT NULL AND state IN ('working','needs_you','pr_ready');
CREATE INDEX IF NOT EXISTS tickets_created_at ON tickets(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS tickets_updated_at ON tickets(updated_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS ticket_entries (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ticket_id TEXT NOT NULL, kind TEXT NOT NULL, author TEXT NOT NULL,
  body TEXT NOT NULL, source_key TEXT UNIQUE, created_at TEXT NOT NULL, consumed_by_run TEXT, posted_at TEXT);
CREATE INDEX IF NOT EXISTS ticket_entries_ticket ON ticket_entries(ticket_id, id);
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY, ticket_id TEXT NOT NULL, kind TEXT NOT NULL, reviewer TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL, runner TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL, lease_expires_at TEXT, tokens INTEGER, cost REAL, report TEXT, queued_at TEXT, started_at TEXT, ended_at TEXT,
  cancel_requested_at TEXT);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_agent_run ON runs(ticket_id) WHERE kind='agent' AND status IN ('queued','running');
CREATE INDEX IF NOT EXISTS runs_status ON runs(status);
CREATE INDEX IF NOT EXISTS runs_ticket_status ON runs(ticket_id, status);
CREATE TABLE IF NOT EXISTS tokens (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, last_used_at TEXT, revoked_at TEXT,
  role TEXT NOT NULL DEFAULT '', prefix TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX IF NOT EXISTS one_active_token_per_name ON tokens(name) WHERE revoked_at IS NULL;
CREATE TABLE IF NOT EXISTS runners (
  name TEXT PRIMARY KEY, kind TEXT NOT NULL DEFAULT '', harnesses TEXT NOT NULL DEFAULT '', slots INTEGER NOT NULL DEFAULT 1, last_contact TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS source_cursors (source TEXT NOT NULL, repo TEXT NOT NULL, cursor TEXT NOT NULL, PRIMARY KEY(source, repo));
CREATE TABLE IF NOT EXISTS tracker_reports (ticket_id TEXT NOT NULL, kind TEXT NOT NULL, reported_at TEXT NOT NULL, PRIMARY KEY(ticket_id, kind));
CREATE TABLE IF NOT EXISTS ignored_source_keys (key TEXT PRIMARY KEY, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS run_activity_events (run TEXT NOT NULL, sequence INTEGER NOT NULL, body TEXT NOT NULL, PRIMARY KEY(run, sequence));
CREATE TABLE IF NOT EXISTS run_progress (run TEXT PRIMARY KEY, step TEXT NOT NULL DEFAULT '', step_started_at TEXT, action TEXT NOT NULL DEFAULT '', partial TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS run_stages (
  run TEXT NOT NULL, position INTEGER NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, state TEXT NOT NULL, outcome TEXT NOT NULL DEFAULT '',
  tokens INTEGER, cost REAL, duration_millis INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', agents TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(run, position));
CREATE TABLE IF NOT EXISTS projects (repo TEXT PRIMARY KEY COLLATE NOCASE, added_by TEXT NOT NULL, added_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS run_patches (run TEXT PRIMARY KEY, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS human_touches (ticket_id TEXT NOT NULL, kind TEXT NOT NULL, author TEXT NOT NULL, at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS human_touches_ticket ON human_touches(ticket_id, at);`

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Ticket struct {
	ID string `json:"id"`
	// Source is where in Machinist the ticket came in: api, run, a tracker's
	// name, or schedule:<automation>. Origin is what the API calls its source.
	Source string `json:"intake"`
	// Origin is where the job came from: web, api, schedule, or a tracker's
	// name such as linear.
	Origin string `json:"source"`
	// SourceURL links to the issue or message the job came from, if any.
	SourceURL    string     `json:"source_url,omitempty"`
	Repo         string     `json:"repo"`
	IssueNumber  int        `json:"issue_number,omitempty"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	Agent        string     `json:"agent"`
	PRNumber     int        `json:"pr_number,omitempty"`
	CreatedBy    string     `json:"created_by"`
	HumanCommits *int       `json:"human_commits,omitempty"`
	MergedAt     *time.Time `json:"merged_at,omitempty"`
	// PRState is open, merged or closed, and PRStateAt is when Machinist saw
	// it change. Both are empty until the job has a pull request.
	PRState   string     `json:"pr_state,omitempty"`
	PRStateAt *time.Time `json:"pr_state_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Shipping is what the control plane keeps while it waits for the pull
	// request's checks, to merge it or report a failed one. It is empty when
	// nothing waits. The engine owns its contents.
	Shipping string `json:"-"`
}

// Branch is the one branch every run of this ticket works on.
func (t Ticket) Branch() string { return "machinist/" + t.ID }

// DefaultTicketLimit is how many tickets a list returns when no limit is given.
// MaxTicketLimit is the largest page size a caller can ask for.
// DefaultBoardDoneLimit is how many finished jobs the Board's Done column shows.
const (
	DefaultTicketLimit    = 200
	MaxTicketLimit        = 200
	DefaultBoardDoneLimit = 20
	maxListedTickets      = DefaultTicketLimit
)

// Automation reports whether the ticket came from a schedule. Such a ticket
// never waits for a person.
func (t Ticket) Automation() bool { return strings.HasPrefix(t.Source, sourceSchedulePrefix) }

type Entry struct {
	ID            int64      `json:"id"`
	TicketID      string     `json:"ticket_id"`
	Kind          string     `json:"kind"`
	Author        string     `json:"author"`
	Body          string     `json:"body"`
	CreatedAt     time.Time  `json:"created_at"`
	ConsumedByRun string     `json:"consumed_by_run,omitempty"`
	PostedAt      *time.Time `json:"posted_at,omitempty"`
}

// Run is one run of a ticket. Stages is filled only by Runs.
type Run struct {
	ID       string                 `json:"id"`
	TicketID string                 `json:"ticket_id"`
	Kind     string                 `json:"kind"`
	Reviewer string                 `json:"reviewer,omitempty"`
	Harness  string                 `json:"harness"`
	Runner   string                 `json:"runner,omitempty"`
	Status   string                 `json:"status"`
	Tokens   *int64                 `json:"tokens,omitempty"`
	Cost     *float64               `json:"cost,omitempty"`
	Report   *protocol.Report       `json:"report,omitempty"`
	Stages   []protocol.StageResult `json:"stages,omitempty"`
	// Patch says the run saved its change as a patch, which RunPatch reads.
	Patch bool `json:"patch,omitempty"`
	// CancelRequested says someone cancelled the run while it was running.
	// It stops at its next heartbeat and its result never moves the ticket.
	CancelRequested bool       `json:"cancel_requested,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
}

// NewTicket is one piece of intake. SourceKey makes it idempotent.
type NewTicket struct {
	Source string
	// Origin is where the job came from, such as web or linear. Empty works
	// it out from Source and SourceKey.
	Origin string
	// SourceURL links to the original issue or message. Anything but an http
	// or https URL is dropped.
	SourceURL   string
	Repo        string
	IssueNumber int
	Title       string
	Body        string
	Agent       string
	CreatedBy   string
	SourceKey   string
}

// Input is new text for an existing ticket. SourceKey makes it idempotent.
type Input struct {
	Kind      string
	Author    string
	Body      string
	SourceKey string
}

// FirstPR is what happens the first time a ticket has a PR.
type FirstPR struct {
	Link string
}

// Transition is the result of one row of the ticket state table.
type Transition struct {
	State    string
	Message  string
	Report   string
	PRNumber int
	FirstPR  *FirstPR
	// MergedAt is set when the run merged its own pull request, so the job
	// counts as merged without waiting for the pull request watcher.
	MergedAt *time.Time
	// Shipping, on a move to pr_ready, starts the wait for the pull request's
	// checks. Every other move ends any wait.
	Shipping string
	// PROpenedAt is when GitHub says the pull request was opened. It is kept
	// from the first time the job has a pull request.
	PROpenedAt *time.Time
	// Reviews, when set, are the people's reviews of a pull request that merged.
	Reviews *Reviews
}

// Reviews are the reviews people submitted on a pull request. Bots' reviews
// are left out.
type Reviews struct {
	Count int
	First *time.Time
}

// A human touch is a time a person had to step in on a job.
const (
	TouchReply = "reply"
	TouchRerun = "rerun"
)

// RunCompletion is what a runner reported when a run ended.
type RunCompletion struct {
	RunID  string
	Status string
	Report *protocol.Report
	Tokens *int64
	Cost   *float64
	Stages []protocol.StageResult
	// Patch is the change the run saved because it could not push it.
	Patch []byte
}

// Claim is a run given to a runner, with the ticket and log it works from.
type Claim struct {
	Run     Run
	Ticket  Ticket
	Entries []Entry
}

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err = addColumns(db, "run_stages", "agents TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, err
	}
	if err = addColumns(db, "tokens", "role TEXT NOT NULL DEFAULT ''", "prefix TEXT NOT NULL DEFAULT ''", "scope TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, err
	}
	if err = addColumns(db, "tickets", "pr_state TEXT", "pr_state_at TEXT", "shipping TEXT"); err != nil {
		db.Close()
		return nil, err
	}
	if err = addColumns(db, "tickets", "origin TEXT NOT NULL DEFAULT ''", "source_url TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, err
	}
	if err = backfillOrigins(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("record where existing jobs came from: %w", err)
	}
	if err = addColumns(db, "tickets", "pr_opened_at TEXT", "first_review_at TEXT", "human_reviews INTEGER"); err != nil {
		db.Close()
		return nil, err
	}
	if err = addColumns(db, "run_stages", "cost_unknown INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	if err = addColumns(db, "runners", "probed INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, err
	}
	if err = addColumns(db, "runs", "cancel_requested_at TEXT"); err != nil {
		db.Close()
		return nil, err
	}
	// Reviewer runs are gone. Cancel any a database from before still queues.
	// Jobs no longer wait for answers, so one left waiting has failed.
	for _, statement := range []string{
		`UPDATE runs SET status='cancelled' WHERE status='queued' AND kind<>'agent'`,
		`UPDATE tickets SET state='failed' WHERE state='needs_you'`,
	} {
		if _, err = db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err = os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// SetClock replaces the store's clock. Tests use it.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) inTx(ctx context.Context, work func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := work(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func randomHex(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// stamp has a fixed width, so stored times compare correctly as text.
func stamp(value time.Time) string { return value.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func parseTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

const ticketColumns = `id,source,origin,source_url,repo,issue_number,title,state,agent,pr_number,created_by,human_commits,merged_at,created_at,updated_at,COALESCE(pr_state,''),pr_state_at,COALESCE(shipping,'')`

type scanner interface{ Scan(...any) error }

func scanTicket(row scanner) (Ticket, error) {
	var ticket Ticket
	var issue, pr, commits sql.NullInt64
	var merged, prStateAt sql.NullString
	var created, updated string
	if err := row.Scan(&ticket.ID, &ticket.Source, &ticket.Origin, &ticket.SourceURL, &ticket.Repo, &issue, &ticket.Title, &ticket.State, &ticket.Agent, &pr, &ticket.CreatedBy, &commits, &merged, &created, &updated, &ticket.PRState, &prStateAt, &ticket.Shipping); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Ticket{}, ErrNotFound
		}
		return Ticket{}, err
	}
	ticket.IssueNumber, ticket.PRNumber = int(issue.Int64), int(pr.Int64)
	if commits.Valid {
		count := int(commits.Int64)
		ticket.HumanCommits = &count
	}
	ticket.MergedAt = parseTime(merged)
	ticket.PRStateAt = parseTime(prStateAt)
	ticket.CreatedAt = *parseTime(sql.NullString{String: created, Valid: true})
	ticket.UpdatedAt = *parseTime(sql.NullString{String: updated, Valid: true})
	return ticket, nil
}

func loadTicket(ctx context.Context, q querier, id string) (Ticket, error) {
	return scanTicket(q.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE id=?`, id))
}

func (s *Store) Ticket(ctx context.Context, id string) (Ticket, error) {
	return loadTicket(ctx, s.db, id)
}

const runColumns = `id,ticket_id,kind,reviewer,harness,runner,status,tokens,cost,report,started_at,ended_at,
  EXISTS(SELECT 1 FROM run_patches WHERE run_patches.run=runs.id),cancel_requested_at IS NOT NULL`

func scanRun(row scanner) (Run, error) {
	var run Run
	var tokens sql.NullInt64
	var cost sql.NullFloat64
	var report, started, ended sql.NullString
	if err := row.Scan(&run.ID, &run.TicketID, &run.Kind, &run.Reviewer, &run.Harness, &run.Runner, &run.Status, &tokens, &cost, &report, &started, &ended, &run.Patch, &run.CancelRequested); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, ErrNotFound
		}
		return Run{}, err
	}
	if tokens.Valid {
		run.Tokens = &tokens.Int64
	}
	if cost.Valid {
		run.Cost = &cost.Float64
	}
	if report.Valid {
		run.Report = &protocol.Report{}
		if err := json.Unmarshal([]byte(report.String), run.Report); err != nil {
			return Run{}, fmt.Errorf("decode stored report: %w", err)
		}
	}
	run.StartedAt, run.EndedAt = parseTime(started), parseTime(ended)
	return run, nil
}

func loadRun(ctx context.Context, q querier, id string) (Run, error) {
	return scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=?`, id))
}

func (s *Store) Run(ctx context.Context, id string) (Run, error) { return loadRun(ctx, s.db, id) }

// RunPatch is the change a run saved as a patch. ErrNotFound means it saved none.
func (s *Store) RunPatch(ctx context.Context, runID string) ([]byte, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, `SELECT body FROM run_patches WHERE run=?`, runID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return body, err
}

// Runs lists a ticket's runs with their stages, oldest first.
func (s *Store) Runs(ctx context.Context, ticketID string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE ticket_id=? ORDER BY rowid`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stages, err := s.db.QueryContext(ctx, `SELECT run,name,kind,state,outcome,tokens,cost,cost_unknown,duration_millis,error,agents FROM run_stages
	  WHERE run IN (SELECT id FROM runs WHERE ticket_id=?) ORDER BY run,position`, ticketID)
	if err != nil {
		return nil, err
	}
	defer stages.Close()
	byRun := map[string][]protocol.StageResult{}
	for stages.Next() {
		var run string
		var stage protocol.StageResult
		var tokens sql.NullInt64
		var cost sql.NullFloat64
		var agents string
		if err := stages.Scan(&run, &stage.Name, &stage.Kind, &stage.State, &stage.Outcome, &tokens, &cost, &stage.CostUnknown, &stage.DurationMillis, &stage.Error, &agents); err != nil {
			return nil, err
		}
		if agents != "" {
			// A stage's agents are a display aid, so an unreadable list shows none.
			_ = json.Unmarshal([]byte(agents), &stage.Agents)
		}
		if tokens.Valid {
			stage.Tokens = &tokens.Int64
		}
		if cost.Valid {
			stage.Cost = &cost.Float64
		}
		byRun[run] = append(byRun[run], stage)
	}
	for index := range runs {
		runs[index].Stages = byRun[runs[index].ID]
	}
	return runs, stages.Err()
}

func loadEntries(ctx context.Context, q querier, ticketID string) ([]Entry, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,ticket_id,kind,author,body,created_at,consumed_by_run,posted_at FROM ticket_entries WHERE ticket_id=? ORDER BY id`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		var entry Entry
		var created string
		var consumed, posted sql.NullString
		if err := rows.Scan(&entry.ID, &entry.TicketID, &entry.Kind, &entry.Author, &entry.Body, &created, &consumed, &posted); err != nil {
			return nil, err
		}
		entry.CreatedAt = *parseTime(sql.NullString{String: created, Valid: true})
		entry.ConsumedByRun = consumed.String
		entry.PostedAt = parseTime(posted)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// Entries lists a ticket's log, oldest first.
func (s *Store) Entries(ctx context.Context, ticketID string) ([]Entry, error) {
	return loadEntries(ctx, s.db, ticketID)
}

func insertEntry(ctx context.Context, q querier, ticketID, kind, author, body string, sourceKey any, now time.Time) error {
	_, err := q.ExecContext(ctx, `INSERT INTO ticket_entries(ticket_id,kind,author,body,source_key,created_at) VALUES(?,?,?,?,?,?)`, ticketID, kind, author, body, sourceKey, stamp(now))
	return err
}

// insertMessage adds an outbound message. Only jobs that came from an issue
// post comments back to GitHub; for all other jobs, the message is marked
// handled and shows only in Machinist.
func insertMessage(ctx context.Context, q querier, ticket Ticket, body string, sourceKey any, now time.Time) error {
	var posted any
	if ticket.IssueNumber == 0 {
		posted = stamp(now)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO ticket_entries(ticket_id,kind,author,body,source_key,created_at,posted_at) VALUES(?,?,?,?,?,?,?)`, ticket.ID, EntryMessage, authorMachinist, body, sourceKey, stamp(now), posted)
	return err
}

func ticketForSourceKey(ctx context.Context, q querier, key string) (Ticket, error) {
	var id string
	if err := q.QueryRowContext(ctx, `SELECT ticket_id FROM ticket_entries WHERE source_key=?`, key).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Ticket{}, ErrNotFound
		}
		return Ticket{}, err
	}
	return loadTicket(ctx, q, id)
}

// CreateTicket returns the existing ticket for a repeated source key or for an
// issue that already has an active ticket. The bool reports whether it created one.
func (s *Store) CreateTicket(ctx context.Context, request NewTicket) (Ticket, bool, error) {
	if request.Source == "" || request.Repo == "" || request.Body == "" || request.Agent == "" || request.SourceKey == "" {
		return Ticket{}, false, errors.New("source, repo, body, agent and source key are required")
	}
	id, err := randomHex(8)
	if err != nil {
		return Ticket{}, false, err
	}
	id = "t_" + id
	var ticket Ticket
	created := false
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		existing, err := ticketForSourceKey(ctx, tx, request.SourceKey)
		if err == nil {
			ticket = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if request.IssueNumber != 0 {
			existing, err = scanTicket(tx.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE repo=? AND issue_number=? AND state IN ('working','pr_ready')`, request.Repo, request.IssueNumber))
			if err == nil {
				ticket = existing
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		if strings.HasPrefix(request.Source, sourceSchedulePrefix) {
			// An automation runs one ticket at a time. Machinist no longer
			// follows pull requests, so an open one does not hold the schedule.
			existing, err = scanTicket(tx.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE source=? AND state=? LIMIT 1`, request.Source, StateWorking))
			if err == nil {
				ticket = existing
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		now := s.now()
		origin := request.Origin
		if origin == "" {
			origin = originOf(request.Source, request.SourceKey)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO tickets(id,source,origin,source_url,repo,issue_number,title,state,agent,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, request.Source, origin, SourceURL(request.SourceURL), request.Repo, nullableInt(request.IssueNumber), request.Title, StateWorking, request.Agent, request.CreatedBy, stamp(now), stamp(now)); err != nil {
			return err
		}
		if err = insertEntry(ctx, tx, id, EntryRequest, request.CreatedBy, request.Body, request.SourceKey, now); err != nil {
			return err
		}
		if request.IssueNumber != 0 {
			if err = insertEntry(ctx, tx, id, EntryMessage, authorMachinist, "Working on this as ticket "+id+".", nil, now); err != nil {
				return err
			}
		}
		created = true
		ticket, err = loadTicket(ctx, tx, id)
		return err
	})
	return ticket, created, err
}

// AddInput appends new text to an active ticket and applies its state row. The
// bool reports whether the entry was added.
func (s *Store) AddInput(ctx context.Context, ticketID string, input Input) (Ticket, bool, error) {
	var ticket Ticket
	added := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if ticket, err = loadTicket(ctx, tx, ticketID); err != nil {
			return err
		}
		if !activeState(ticket.State) {
			return nil
		}
		if input.SourceKey != "" {
			if _, err = ticketForSourceKey(ctx, tx, input.SourceKey); err == nil {
				return nil
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		now := s.now()
		if err = insertEntry(ctx, tx, ticketID, input.Kind, input.Author, input.Body, nullableString(input.SourceKey), now); err != nil {
			return err
		}
		added = true
		next := ticket.State
		if ticket.State == StatePRReady {
			next = StateWorking
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,shipping=NULL,updated_at=? WHERE id=?`, next, stamp(now), ticketID); err != nil {
			return err
		}
		ticket, err = loadTicket(ctx, tx, ticketID)
		return err
	})
	return ticket, added, err
}

// TicketBySourceKey finds the ticket that a piece of intake went to.
func (s *Store) TicketBySourceKey(ctx context.Context, key string) (Ticket, error) {
	return ticketForSourceKey(ctx, s.db, key)
}

// TicketFilter picks the tickets a list shows. An empty field matches all.
type TicketFilter struct {
	State string
	// States, when non-empty, matches tickets in any of these states, including
	// "running" and "queued" which distinguish working tickets held by a runner.
	States []string
	Repo   string
	// Source is where the jobs came from, such as web or linear.
	Source string
	// Sort orders results: "" or "created" puts newest created first;
	// "updated" puts most recently updated first.
	Sort string
	// Limit caps one page. Zero uses DefaultTicketLimit.
	Limit int
	// Offset skips the first Offset matching tickets.
	Offset int
}

// TicketCounts counts jobs in each UI quick filter within the current repo and source.
type TicketCounts struct {
	Queued      int `json:"queued"`
	Running     int `json:"running"`
	Done        int `json:"done"`
	Failed      int `json:"failed"`
	NeedsPerson int `json:"needs_person"`
	Cancelled   int `json:"cancelled"`
}

// TicketPage is one page of tickets, with the total matching count and
// whether more tickets follow this page.
type TicketPage struct {
	Tickets   []Ticket     `json:"tickets"`
	Total     int          `json:"total"`
	Limit     int          `json:"limit"`
	Offset    int          `json:"offset"`
	HasMore   bool         `json:"has_more"`
	DoneTotal *int         `json:"done_total,omitempty"`
	DoneLimit *int         `json:"done_limit,omitempty"`
	Counts    TicketCounts `json:"counts"`
	Sources   []string     `json:"sources"`
}

const runningTicketExpr = `EXISTS(SELECT 1 FROM runs WHERE runs.ticket_id=tickets.id AND runs.status='running')`

func ticketWhere(filter TicketFilter) (string, []any) {
	clauses := []string{}
	args := []any{}
	states := make([]string, 0, len(filter.States)+1)
	if filter.State != "" {
		states = append(states, filter.State)
	}
	states = append(states, filter.States...)
	if len(states) > 0 {
		parts := make([]string, 0, len(states))
		for _, state := range states {
			switch state {
			case "running":
				parts = append(parts, `(state='working' AND `+runningTicketExpr+`)`)
			case "queued":
				parts = append(parts, `(state='working' AND NOT `+runningTicketExpr+`)`)
			default:
				parts = append(parts, `state=?`)
				args = append(args, state)
			}
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if filter.Repo != "" {
		clauses = append(clauses, "repo=?")
		args = append(args, filter.Repo)
	}
	if filter.Source != "" {
		clauses = append(clauses, "origin=?")
		args = append(args, filter.Source)
	}
	if len(clauses) == 0 {
		return "1=1", args
	}
	return strings.Join(clauses, " AND "), args
}

func scanTickets(rows *sql.Rows) ([]Ticket, error) {
	found := []Ticket{}
	for rows.Next() {
		ticket, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, ticket)
	}
	return found, rows.Err()
}

func ticketSummary(ctx context.Context, q querier, repo, source string) (TicketCounts, []string, error) {
	var counts TicketCounts
	rows, err := q.QueryContext(ctx, `SELECT state,`+runningTicketExpr+`,COUNT(*) FROM tickets
  WHERE (?='' OR repo=?) AND (?='' OR origin=?) GROUP BY 1,2`, repo, repo, source, source)
	if err != nil {
		return counts, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var running bool
		var n int
		if err := rows.Scan(&state, &running, &n); err != nil {
			return counts, nil, err
		}
		switch state {
		case StateWorking:
			if running {
				counts.Running += n
			} else {
				counts.Queued += n
			}
		case StatePRReady:
			counts.Done += n
			counts.NeedsPerson += n
		case StateDone:
			counts.Done += n
		case StateCancelled:
			counts.Cancelled += n
		default:
			counts.Failed += n
		}
	}
	if err := rows.Err(); err != nil {
		return counts, nil, err
	}

	sourceRows, err := q.QueryContext(ctx, `SELECT DISTINCT origin FROM tickets WHERE origin<>'' AND (?='' OR repo=?) ORDER BY origin`, repo, repo)
	if err != nil {
		return counts, nil, err
	}
	defer sourceRows.Close()
	sources := []string{}
	for sourceRows.Next() {
		var origin string
		if err := sourceRows.Scan(&origin); err != nil {
			return counts, nil, err
		}
		sources = append(sources, origin)
	}
	return counts, sources, sourceRows.Err()
}

// ListTicketsPage lists one page of tickets matching the filter, with total
// matching count, filter counts, and available sources.
func (s *Store) ListTicketsPage(ctx context.Context, filter TicketFilter) (TicketPage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultTicketLimit
	}
	if limit > MaxTicketLimit {
		limit = MaxTicketLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	orderBy := "created_at DESC, id DESC"
	if filter.Sort == "updated" {
		orderBy = "updated_at DESC, id DESC"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TicketPage{}, err
	}
	defer tx.Rollback()

	where, args := ticketWhere(filter)
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tickets WHERE `+where, args...).Scan(&total); err != nil {
		return TicketPage{}, err
	}

	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := tx.QueryContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE `+where+` ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return TicketPage{}, err
	}
	found, err := scanTickets(rows)
	rows.Close()
	if err != nil {
		return TicketPage{}, err
	}

	counts, sources, err := ticketSummary(ctx, tx, filter.Repo, filter.Source)
	if err != nil {
		return TicketPage{}, err
	}
	return TicketPage{
		Tickets: found,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
		HasMore: offset+len(found) < total,
		Counts:  counts,
		Sources: sources,
	}, nil
}

// ListBoardTickets returns every matching active (queued, running, or in-review)
// ticket uncapped, plus at most doneLimit of the most recently updated finished tickets.
func (s *Store) ListBoardTickets(ctx context.Context, filter TicketFilter, doneLimit int) (TicketPage, error) {
	if doneLimit <= 0 {
		doneLimit = DefaultBoardDoneLimit
	}
	if doneLimit > MaxTicketLimit {
		doneLimit = MaxTicketLimit
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TicketPage{}, err
	}
	defer tx.Rollback()

	where, args := ticketWhere(filter)
	activeRows, err := tx.QueryContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE state IN ('working','pr_ready') AND (`+where+`) ORDER BY created_at ASC, id ASC`, args...)
	if err != nil {
		return TicketPage{}, err
	}
	active, err := scanTickets(activeRows)
	activeRows.Close()
	if err != nil {
		return TicketPage{}, err
	}

	var doneTotal int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tickets WHERE state NOT IN ('working','pr_ready') AND (`+where+`)`, args...).Scan(&doneTotal); err != nil {
		return TicketPage{}, err
	}

	doneArgs := append(append([]any{}, args...), doneLimit)
	doneRows, err := tx.QueryContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE state NOT IN ('working','pr_ready') AND (`+where+`) ORDER BY updated_at DESC, id DESC LIMIT ?`, doneArgs...)
	if err != nil {
		return TicketPage{}, err
	}
	done, err := scanTickets(doneRows)
	doneRows.Close()
	if err != nil {
		return TicketPage{}, err
	}

	counts, sources, err := ticketSummary(ctx, tx, filter.Repo, filter.Source)
	if err != nil {
		return TicketPage{}, err
	}

	combined := make([]Ticket, 0, len(active)+len(done))
	combined = append(combined, active...)
	combined = append(combined, done...)
	return TicketPage{
		Tickets:   combined,
		Total:     len(active) + doneTotal,
		Limit:     doneLimit,
		Offset:    0,
		HasMore:   doneTotal > len(done),
		DoneTotal: &doneTotal,
		DoneLimit: &doneLimit,
		Counts:    counts,
		Sources:   sources,
	}, nil
}

// ListTickets lists the newest tickets that match the filter, newest first.
func (s *Store) ListTickets(ctx context.Context, filter TicketFilter) ([]Ticket, error) {
	page, err := s.ListTicketsPage(ctx, filter)
	if err != nil {
		return nil, err
	}
	return page.Tickets, nil
}

// LatestMessage returns the newest thing Machinist said on a ticket.
func (s *Store) LatestMessage(ctx context.Context, ticketID string) (string, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM ticket_entries WHERE ticket_id=? AND kind=? ORDER BY id DESC LIMIT 1`, ticketID, EntryMessage).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body, err
}

// LatestTicketBySource returns the newest ticket from a source.
func (s *Store) LatestTicketBySource(ctx context.Context, source string) (Ticket, error) {
	return scanTicket(s.db.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE source=? ORDER BY rowid DESC LIMIT 1`, source))
}

func activeState(state string) bool {
	return state == StateWorking || state == StatePRReady
}

// Cancel ends a ticket. A queued run stops here. A running run is marked, and
// told to stop by its next heartbeat. The mark keeps it cancelled even if the
// ticket runs again before then, so its result never lands on the new run.
func (s *Store) Cancel(ctx context.Context, ticketID, by string) (Ticket, error) {
	var ticket Ticket
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if ticket, err = loadTicket(ctx, tx, ticketID); err != nil {
			return err
		}
		if !activeState(ticket.State) {
			return ErrTicketEnded
		}
		now := s.now()
		if err = cancelQueuedRuns(ctx, tx, ticketID, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET cancel_requested_at=? WHERE ticket_id=? AND status=? AND cancel_requested_at IS NULL`, stamp(now), ticketID, RunRunning); err != nil {
			return err
		}
		if err = applyTransition(ctx, tx, ticket, Transition{State: StateCancelled, Message: "Cancelled by " + by}, now); err != nil {
			return err
		}
		ticket, err = loadTicket(ctx, tx, ticketID)
		return err
	})
	return ticket, err
}

// ErrStillWorking means a ticket has a run going or about to, so it cannot run again yet.
var ErrStillWorking = errors.New("the job is still running")

// Rerun starts a ticket's preset again from the first step, on the same branch.
// It works on any ticket with nothing running: done, failed, cancelled, or with
// its pull request open. A cancelled run that has not stopped yet still counts
// as running. The request is logged as a comment from by, so the
// next run sees it and the lost-runner count starts again.
func (s *Store) Rerun(ctx context.Context, ticketID, by string) (Ticket, error) {
	return s.reply(ctx, ticketID, by, TouchRerun, []Comment{{Body: "Run the job again."}})
}

// Comment is one comment to add to a ticket's log. Key, when set, is unique:
// a comment whose key is already stored is never added twice.
type Comment struct {
	Body string
	Key  string
}

// Reply adds comments to an ended ticket and runs it again on the same
// branch. The new run's prompt is the whole log, so it sees them. It returns
// ErrStillWorking while any run of the ticket is queued or running.
func (s *Store) Reply(ctx context.Context, ticketID, by string, comments []Comment) (Ticket, error) {
	return s.reply(ctx, ticketID, by, TouchReply, comments)
}

// reply runs a ticket again and records it as one human touch of kind.
func (s *Store) reply(ctx context.Context, ticketID, by, kind string, comments []Comment) (Ticket, error) {
	var ticket Ticket
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if ticket, err = loadTicket(ctx, tx, ticketID); err != nil {
			return err
		}
		if ticket.State == StateWorking {
			return ErrStillWorking
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE ticket_id=? AND status IN (?,?)`, ticketID, RunQueued, RunRunning).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrStillWorking
		}
		now := s.now()
		for _, comment := range comments {
			var key any
			if comment.Key != "" {
				key = comment.Key
			}
			if err = insertEntry(ctx, tx, ticketID, EntryComment, by, comment.Body, key, now); err != nil {
				return err
			}
		}
		// The new run's pull request is followed afresh, and its tracker is told
		// again that it started and how it ends. A pull request that merged or
		// closed is finished with, so the new run opens and counts its own. An
		// open one stays, so the new run keeps working on it.
		if _, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,pr_state=NULL,pr_state_at=NULL,shipping=NULL,updated_at=? WHERE id=?`, StateWorking, stamp(now), ticketID); err != nil {
			return err
		}
		if ticket.MergedAt != nil || ticket.PRState == PRMerged || ticket.PRState == PRClosed {
			if _, err = tx.ExecContext(ctx, `UPDATE tickets SET pr_number=NULL,merged_at=NULL,pr_opened_at=NULL,first_review_at=NULL,human_reviews=NULL WHERE id=?`, ticketID); err != nil {
				return err
			}
			// Drop touches from the finished pull request so the next pull
			// request starts with its own count, just like reviews.
			if _, err = tx.ExecContext(ctx, `DELETE FROM human_touches WHERE ticket_id=?`, ticketID); err != nil {
				return err
			}
			// The tracker was told about the old pull request; it is told about
			// the new one too.
			if _, err = tx.ExecContext(ctx, `DELETE FROM tracker_reports WHERE ticket_id=? AND kind=?`, ticketID, ReportPR); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO human_touches(ticket_id,kind,author,at) VALUES(?,?,?,?)`, ticketID, kind, by, stamp(now)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM tracker_reports WHERE ticket_id=? AND kind IN (?,?,?,?,?)`, ticketID, ReportStarted, ReportFailed, ReportMerged, ReportClosed, ReportFinished); err != nil {
			return err
		}
		ticket, err = loadTicket(ctx, tx, ticketID)
		return err
	})
	return ticket, err
}

func cancelQueuedRuns(ctx context.Context, q querier, ticketID string, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE runs SET status=?,ended_at=? WHERE ticket_id=? AND status=?`, RunCancelled, stamp(now), ticketID, RunQueued)
	return err
}

func queueRun(ctx context.Context, q querier, ticketID, kind, reviewer, harness string, now time.Time) (string, error) {
	id, err := randomHex(12)
	if err != nil {
		return "", err
	}
	id = "run_" + id
	_, err = q.ExecContext(ctx, `INSERT INTO runs(id,ticket_id,kind,reviewer,harness,status,queued_at) VALUES(?,?,?,?,?,?,?)`, id, ticketID, kind, reviewer, harness, RunQueued, stamp(now))
	return id, err
}

func applyTransition(ctx context.Context, q querier, ticket Ticket, transition Transition, now time.Time) error {
	var shipping any
	if transition.State == StatePRReady {
		shipping = nullableString(transition.Shipping)
	}
	if _, err := q.ExecContext(ctx, `UPDATE tickets SET state=?,pr_number=COALESCE(?,pr_number),shipping=?,updated_at=? WHERE id=?`, transition.State, nullableInt(transition.PRNumber), shipping, stamp(now), ticket.ID); err != nil {
		return err
	}
	// A job that ends with an open pull request is watched until it merges or
	// closes.
	if transition.State == StatePRReady {
		if _, err := q.ExecContext(ctx, `UPDATE tickets SET pr_state=?,pr_state_at=? WHERE id=?`, PROpen, stamp(now), ticket.ID); err != nil {
			return err
		}
	}
	if transition.PRNumber != 0 {
		opened := now
		if transition.PROpenedAt != nil {
			opened = *transition.PROpenedAt
		}
		if _, err := q.ExecContext(ctx, `UPDATE tickets SET pr_opened_at=COALESCE(pr_opened_at,?) WHERE id=?`, stamp(opened), ticket.ID); err != nil {
			return err
		}
	}
	if transition.MergedAt != nil {
		if _, err := q.ExecContext(ctx, `UPDATE tickets SET pr_state=?,pr_state_at=?,merged_at=COALESCE(merged_at,?) WHERE id=?`, PRMerged, stamp(now), stamp(*transition.MergedAt), ticket.ID); err != nil {
			return err
		}
	}
	if err := setReviews(ctx, q, ticket.ID, transition.Reviews); err != nil {
		return err
	}
	if transition.Report != "" {
		if err := insertEntry(ctx, q, ticket.ID, EntryReport, ticket.Agent, transition.Report, nil, now); err != nil {
			return err
		}
	}
	// The PR link goes first, so the result stays the newest thing Machinist said.
	if transition.FirstPR != nil && transition.FirstPR.Link != "" {
		if err := insertMessage(ctx, q, ticket, transition.FirstPR.Link, nil, now); err != nil {
			return err
		}
	}
	if transition.Message != "" {
		if err := insertMessage(ctx, q, ticket, transition.Message, nil, now); err != nil {
			return err
		}
	}
	return nil
}

// Apply moves a ticket that is still in the expected state.
func (s *Store) Apply(ctx context.Context, ticketID, from string, transition Transition) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		ticket, err := loadTicket(ctx, tx, ticketID)
		if err != nil || ticket.State != from {
			return err
		}
		return applyTransition(ctx, tx, ticket, transition, s.now())
	})
}

// TicketsAwaitingRun lists working tickets with no queued or running agent run.
func (s *Store) TicketsAwaitingRun(ctx context.Context) ([]Ticket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ticketColumns+` FROM tickets t WHERE state=? AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.ticket_id=t.id AND r.kind=? AND r.status IN (?,?)) ORDER BY created_at`, StateWorking, RunKindAgent, RunQueued, RunRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tickets := []Ticket{}
	for rows.Next() {
		ticket, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, ticket)
	}
	return tickets, rows.Err()
}

// turnsSinceHuman counts agent runs since the last human input. Input not yet
// given to a run means none have happened since.
func turnsSinceHuman(ctx context.Context, q querier, ticketID string) (int, error) {
	var consumed sql.NullString
	err := q.QueryRowContext(ctx, `SELECT consumed_by_run FROM ticket_entries WHERE ticket_id=? AND kind IN (?,?,?) ORDER BY id DESC LIMIT 1`, ticketID, EntryRequest, EntryComment, EntryReview).Scan(&consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil || !consumed.Valid {
		return 0, err
	}
	var count int
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE ticket_id=? AND kind=? AND rowid>=(SELECT rowid FROM runs WHERE id=?)`, ticketID, RunKindAgent, consumed.String).Scan(&count)
	return count, err
}

// TurnsSinceHuman counts agent runs since the last human input.
func (s *Store) TurnsSinceHuman(ctx context.Context, ticketID string) (int, error) {
	return turnsSinceHuman(ctx, s.db, ticketID)
}

// LastReport returns the text of the ticket's newest agent report.
func (s *Store) LastReport(ctx context.Context, ticketID string) (string, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM ticket_entries WHERE ticket_id=? AND kind=? ORDER BY id DESC LIMIT 1`, ticketID, EntryReport).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body, err
}

// QueueRun adds an agent run for a working ticket. The state is read in the same
// transaction, so a ticket that was cancelled or merged a moment ago gets no run.
func (s *Store) QueueRun(ctx context.Context, ticketID, harness string) (Run, error) {
	var run Run
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		ticket, err := loadTicket(ctx, tx, ticketID)
		if err != nil {
			return err
		}
		if ticket.State != StateWorking {
			return ErrNotWorking
		}
		id, err := queueRun(ctx, tx, ticketID, RunKindAgent, "", harness, s.now())
		if err != nil {
			return err
		}
		run, err = loadRun(ctx, tx, id)
		return err
	})
	return run, err
}

// firstAccepted returns the first run the query lists whose ticket accept
// allows, or ErrNotFound.
func firstAccepted(ctx context.Context, tx *sql.Tx, query string, arguments []any, accept func(Ticket) bool) (Run, error) {
	// Page through the queue, so runs this runner cannot take never hide a
	// run it can behind them.
	for offset := 0; ; offset += claimPage {
		runs, err := queryRuns(ctx, tx, query+fmt.Sprintf(" LIMIT %d OFFSET %d", claimPage, offset), arguments)
		if err != nil {
			return Run{}, err
		}
		for _, run := range runs {
			if accept == nil {
				return run, nil
			}
			ticket, err := loadTicket(ctx, tx, run.TicketID)
			if err != nil {
				return Run{}, err
			}
			if accept(ticket) {
				return run, nil
			}
		}
		if len(runs) < claimPage {
			return Run{}, ErrNotFound
		}
	}
}

// claimPage is how many queued runs a claim reads at a time.
var claimPage = 500

func queryRuns(ctx context.Context, tx *sql.Tx, query string, arguments []any) ([]Run, error) {
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, errors.Join(rows.Err(), rows.Close())
}

// ClaimRun gives the oldest queued run for these harnesses whose ticket accept
// allows to a runner, and marks waiting input as read by it. A nil accept
// allows every ticket. It returns nil when no such run is queued.
func (s *Store) ClaimRun(ctx context.Context, runner string, harnesses []string, leaseUntil time.Time, accept func(Ticket) bool) (*Claim, error) {
	if len(harnesses) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(harnesses)), ",")
	arguments := []any{RunQueued}
	for _, harness := range harnesses {
		arguments = append(arguments, harness)
	}
	var claim *Claim
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		run, err := firstAccepted(ctx, tx, `SELECT `+runColumns+` FROM runs WHERE status=? AND kind='agent' AND harness IN (`+placeholders+`) ORDER BY rowid`, arguments, accept)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		now := s.now()
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET status=?,runner=?,lease_expires_at=?,started_at=? WHERE id=?`, RunRunning, runner, stamp(leaseUntil), stamp(now), run.ID); err != nil {
			return err
		}
		// The ticket moves from queued to running, so it has changed.
		if _, err = tx.ExecContext(ctx, `UPDATE tickets SET updated_at=? WHERE id=?`, stamp(now), run.TicketID); err != nil {
			return err
		}
		if run.Kind == RunKindAgent {
			if _, err = tx.ExecContext(ctx, `UPDATE ticket_entries SET consumed_by_run=? WHERE ticket_id=? AND consumed_by_run IS NULL AND kind IN (?,?,?,?)`, run.ID, run.TicketID, EntryRequest, EntryComment, EntryReview, EntryCI); err != nil {
				return err
			}
		}
		if run, err = loadRun(ctx, tx, run.ID); err != nil {
			return err
		}
		ticket, err := loadTicket(ctx, tx, run.TicketID)
		if err != nil {
			return err
		}
		entries, err := loadEntries(ctx, tx, run.TicketID)
		if err != nil {
			return err
		}
		claim = &Claim{Run: run, Ticket: ticket, Entries: entries}
		return nil
	})
	return claim, err
}

// RenewLease extends a running run's lease and reports whether it should stop.
func (s *Store) RenewLease(ctx context.Context, runID string, leaseUntil time.Time) (bool, error) {
	cancel := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		run, err := loadRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if run.Status != RunRunning {
			return ErrRunNotActive
		}
		ticket, err := loadTicket(ctx, tx, run.TicketID)
		if err != nil {
			return err
		}
		cancel = run.CancelRequested || !activeState(ticket.State) || (run.Kind == RunKindAgent && ticket.State != StateWorking)
		_, err = tx.ExecContext(ctx, `UPDATE runs SET lease_expires_at=? WHERE id=?`, stamp(leaseUntil), runID)
		return err
	})
	return cancel, err
}

// ExpireLeases marks running runs whose lease has passed as interrupted.
func (s *Store) ExpireLeases(ctx context.Context) (int64, error) {
	now := stamp(s.now())
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status=?,ended_at=? WHERE status=? AND lease_expires_at<=?`, RunInterrupted, now, RunRunning, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CompleteRun records a run's end. For an agent run on a working ticket, decide
// picks the next state. Input that arrived during the run returns a ticket with
// an open pull request to working.
func (s *Store) CompleteRun(ctx context.Context, completion RunCompletion, decide func(Ticket, string) Transition) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		run, err := loadRun(ctx, tx, completion.RunID)
		if err != nil {
			return err
		}
		if run.Status != RunRunning {
			return ErrRunNotActive
		}
		ticket, err := loadTicket(ctx, tx, run.TicketID)
		if err != nil {
			return err
		}
		status := completion.Status
		if run.CancelRequested {
			// Someone cancelled this run. Whatever it reports, it never moves
			// the ticket, which may already be running again.
			status = RunCancelled
		}
		isTurn := run.Kind == RunKindAgent && ticket.State == StateWorking && !run.CancelRequested
		if isTurn && status == RunCancelled {
			// Nobody cancelled this ticket, so its runner stopped. Treat it like a lost lease.
			status = RunInterrupted
		}
		var report any
		if completion.Report != nil {
			body, err := json.Marshal(completion.Report)
			if err != nil {
				return err
			}
			report = string(body)
		}
		now := s.now()
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET status=?,tokens=?,cost=?,report=?,ended_at=? WHERE id=?`, status, completion.Tokens, completion.Cost, report, stamp(now), run.ID); err != nil {
			return err
		}
		if len(completion.Patch) > 0 {
			if _, err = tx.ExecContext(ctx, `INSERT INTO run_patches(run,body) VALUES(?,?)`, run.ID, completion.Patch); err != nil {
				return err
			}
		}
		for position, stage := range completion.Stages {
			agents := ""
			if len(stage.Agents) > 0 {
				body, err := json.Marshal(stage.Agents)
				if err != nil {
					return err
				}
				agents = string(body)
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO run_stages(run,position,name,kind,state,outcome,tokens,cost,cost_unknown,duration_millis,error,agents) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				run.ID, position, stage.Name, stage.Kind, stage.State, stage.Outcome, stage.Tokens, stage.Cost, stage.CostUnknown, stage.DurationMillis, stage.Error, agents); err != nil {
				return err
			}
		}
		if !isTurn {
			return nil
		}
		transition := decide(ticket, status)
		if transition.State == StatePRReady {
			var waiting int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ticket_entries WHERE ticket_id=? AND consumed_by_run IS NULL AND kind IN (?,?,?,?)`, ticket.ID, EntryRequest, EntryComment, EntryReview, EntryCI).Scan(&waiting); err != nil {
				return err
			}
			if waiting > 0 {
				transition.State = StateWorking
			}
		}
		return applyTransition(ctx, tx, ticket, transition, now)
	})
}

// IngestRunEvents stores a run's activity in order and returns the highest
// stored sequence. Repeats of stored events are accepted.
func (s *Store) IngestRunEvents(ctx context.Context, runID string, events []json.RawMessage) (int64, error) {
	if len(events) > protocol.MaxRunEventBatch {
		return 0, errors.New("event batch too large")
	}
	var cursor int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		run, err := loadRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if run.Status != RunRunning {
			return ErrRunNotActive
		}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM run_activity_events WHERE run=?`, runID).Scan(&cursor); err != nil {
			return err
		}
		now, err := loadProgress(ctx, tx, runID)
		if err != nil {
			return err
		}
		for _, raw := range events {
			var event protocol.RunEvent
			if len(raw) > 128<<10 || json.Unmarshal(raw, &event) != nil || event.Sequence < 1 || event.Type == "" {
				return errors.New("invalid run event")
			}
			if event.Sequence <= cursor {
				var previous string
				if err = tx.QueryRowContext(ctx, `SELECT body FROM run_activity_events WHERE run=? AND sequence=?`, runID, event.Sequence).Scan(&previous); err != nil {
					return err
				}
				var left, right any
				_ = json.Unmarshal([]byte(previous), &left)
				_ = json.Unmarshal(raw, &right)
				a, _ := json.Marshal(left)
				b, _ := json.Marshal(right)
				if string(a) != string(b) {
					return errors.New("event sequence payload conflict")
				}
				continue
			}
			if event.Sequence != cursor+1 {
				return fmt.Errorf("event sequence gap after %d", cursor)
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO run_activity_events(run,sequence,body) VALUES(?,?,?)`, runID, event.Sequence, string(raw)); err != nil {
				return err
			}
			cursor = event.Sequence
			now.note(event)
		}
		return saveProgress(ctx, tx, runID, now)
	})
	return cursor, err
}

// RunEvents returns one page of a run's activity after a cursor.
func (s *Store) RunEvents(ctx context.Context, runID string, after int64) (protocol.RunEventPage, error) {
	page := protocol.RunEventPage{Cursor: after, Events: []json.RawMessage{}}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,body FROM run_activity_events WHERE run=? AND sequence>? ORDER BY sequence LIMIT 128`, runID, after)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err = rows.Scan(&page.Cursor, &body); err != nil {
			return page, err
		}
		page.Events = append(page.Events, json.RawMessage(body))
	}
	return page, rows.Err()
}

// ActiveTicketFor finds the active ticket on an issue or PR number.
func (s *Store) ActiveTicketFor(ctx context.Context, repo string, number int) (Ticket, error) {
	return scanTicket(s.db.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE repo=? AND (issue_number=? OR pr_number=?) AND state IN (?,?) ORDER BY created_at DESC LIMIT 1`, repo, number, number, StateWorking, StatePRReady))
}

// Cursor returns a source's stored position. The bool is false on first use.
func (s *Store) Cursor(ctx context.Context, source, repo string) (time.Time, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT cursor FROM source_cursors WHERE source=? AND repo=?`, source, repo).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil, err
}

// SetCursor stores a source's position.
func (s *Store) SetCursor(ctx context.Context, source, repo string, value time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO source_cursors(source,repo,cursor) VALUES(?,?,?) ON CONFLICT(source,repo) DO UPDATE SET cursor=excluded.cursor`, source, repo, stamp(value))
	return err
}

// Outbound is one message waiting to be posted.
type Outbound struct {
	EntryID int64
	Repo    string
	Number  int
	Body    string
}

// UnpostedMessages lists messages to post on GitHub, oldest first. Only jobs
// that came from an issue post comments to GitHub.
func (s *Store) UnpostedMessages(ctx context.Context) ([]Outbound, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,t.repo,t.issue_number,e.body FROM ticket_entries e JOIN tickets t ON t.id=e.ticket_id WHERE e.kind=? AND e.posted_at IS NULL AND t.issue_number IS NOT NULL ORDER BY e.id`, EntryMessage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []Outbound{}
	for rows.Next() {
		var message Outbound
		if err := rows.Scan(&message.EntryID, &message.Repo, &message.Number, &message.Body); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// MarkPosted records that a message reached GitHub.
func (s *Store) MarkPosted(ctx context.Context, entryID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ticket_entries SET posted_at=? WHERE id=?`, stamp(s.now()), entryID)
	return err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// The roles a token can have. A runner token polls for runs and reports on
// its own. An operator token is for people: the web app, the API and the CLI.
// A token from before roles has RoleAll and can do both.
const (
	RoleAll      = ""
	RoleOperator = "operator"
	RoleRunner   = "runner"
)

// The scopes an operator token can be limited to. ScopeAll (empty) keeps full
// operator access, including sign-in, settings and token management. ScopeRead
// can read jobs, automations and output. ScopeRun can also start and cancel
// jobs.
const (
	ScopeAll  = ""
	ScopeRead = "read"
	ScopeRun  = "run"
)

// Allows reports whether a token with this role may act as want.
func Allows(role, want string) bool { return role == RoleAll || role == want }

// AllowsScope reports whether a token with role and scope may perform an
// operator action requiring wantScope (ScopeAll, ScopeRun or ScopeRead).
func AllowsScope(role, scope, wantScope string) bool {
	if !Allows(role, RoleOperator) {
		return false
	}
	if scope == ScopeAll {
		return true
	}
	switch wantScope {
	case ScopeRead:
		return scope == ScopeRead || scope == ScopeRun
	case ScopeRun:
		return scope == ScopeRun
	default:
		return false
	}
}

// TokenPrefix starts every new API token so a person can recognise it in a
// config file or clipboard. tokenPrefixDisplayLength keeps TokenPrefix plus
// four hex characters (16 bits of the 256-bit secret) so rows can be matched
// to tokens without weakening them.
const (
	TokenPrefix              = "mch_"
	tokenPrefixDisplayLength = len(TokenPrefix) + 4
)

func newToken() (token, prefix string, err error) {
	secret, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	token = TokenPrefix + secret
	return token, token[:tokenPrefixDisplayLength], nil
}

// CreateToken stores a new token with a role and full scope, returning its
// only plaintext copy.
func (s *Store) CreateToken(ctx context.Context, name, role string) (string, error) {
	return s.CreateScopedToken(ctx, name, role, ScopeAll)
}

// CreateScopedToken stores a new token with a role and optional scope,
// returning its only plaintext copy.
func (s *Store) CreateScopedToken(ctx context.Context, name, role, scope string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("token name is required")
	}
	if strings.Contains(name, "/") {
		return "", errors.New("token name cannot contain /")
	}
	switch role {
	case RoleAll, RoleOperator, RoleRunner:
	default:
		return "", fmt.Errorf("unknown token role %q", role)
	}
	switch scope {
	case ScopeAll, ScopeRead, ScopeRun:
	default:
		return "", fmt.Errorf("unknown token scope %q; must be read or run", scope)
	}
	if scope != ScopeAll && role != RoleOperator {
		return "", fmt.Errorf("scope %q can only be set on an operator token", scope)
	}
	id, err := randomHex(8)
	if err != nil {
		return "", err
	}
	token, prefix, err := newToken()
	if err != nil {
		return "", err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var taken int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens WHERE name=? AND revoked_at IS NULL`, name).Scan(&taken)
		if err != nil {
			return err
		}
		if taken > 0 {
			return ErrTokenExists
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO tokens(id,name,token_hash,created_at,role,prefix,scope) VALUES(?,?,?,?,?,?,?)`, id, name, hashToken(token), stamp(s.now()), role, prefix, scope)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// RegenerateToken atomically revokes an active token and creates a new one
// with the same name, role and scope, returning its only plaintext copy.
func (s *Store) RegenerateToken(ctx context.Context, name string) (string, error) {
	id, err := randomHex(8)
	if err != nil {
		return "", err
	}
	token, prefix, err := newToken()
	if err != nil {
		return "", err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var role, scope string
		err := tx.QueryRowContext(ctx, `SELECT role,scope FROM tokens WHERE name=? AND revoked_at IS NULL`, name).Scan(&role, &scope)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		now := stamp(s.now())
		if _, err := tx.ExecContext(ctx, `UPDATE tokens SET revoked_at=? WHERE name=? AND revoked_at IS NULL`, now, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM runners WHERE name=?`, name); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO tokens(id,name,token_hash,created_at,role,prefix,scope) VALUES(?,?,?,?,?,?,?)`, id, name, hashToken(token), now, role, prefix, scope)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// TokenInfo describes a token without revealing it.
type TokenInfo struct {
	Name       string
	Role       string
	Scope      string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// ListTokens returns every token, oldest first.
func (s *Store) ListTokens(ctx context.Context) ([]TokenInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,role,scope,prefix,created_at,last_used_at,revoked_at FROM tokens ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []TokenInfo{}
	for rows.Next() {
		var info TokenInfo
		var created string
		var used, revoked sql.NullString
		if err := rows.Scan(&info.Name, &info.Role, &info.Scope, &info.Prefix, &created, &used, &revoked); err != nil {
			return nil, err
		}
		info.CreatedAt = *parseTime(sql.NullString{String: created, Valid: true})
		info.LastUsedAt, info.RevokedAt = parseTime(used), parseTime(revoked)
		tokens = append(tokens, info)
	}
	return tokens, rows.Err()
}

// RevokeToken ends an active token. Its sign-ins end with it, and a runner
// that used it leaves the fleet.
func (s *Store) RevokeToken(ctx context.Context, name string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at=? WHERE name=? AND revoked_at IS NULL`, stamp(s.now()), name)
	if err != nil {
		return err
	}
	revoked, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if revoked == 0 {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM runners WHERE name=?`, name)
	return err
}

// tokenUseInterval is how stale last_used_at may get before a request
// records a new use. Every authenticated request calls Authenticate, so writing
// each time would turn the web UI's polling and runner heartbeats into
// writes that compete with run event ingestion.
const tokenUseInterval = time.Minute

// TokenAuth is the identity and permissions of an authenticated token.
type TokenAuth struct {
	Name  string
	Role  string
	Scope string
}

// Authenticate returns the name, role and scope of a valid token and records
// its use, at most once per tokenUseInterval.
func (s *Store) Authenticate(ctx context.Context, token string) (TokenAuth, error) {
	now := s.now()
	hash := hashToken(token)
	// Stamps are fixed-width UTC, so they compare as strings.
	cutoff := stamp(now.Add(-tokenUseInterval))
	var auth TokenAuth
	var stale bool
	err := s.db.QueryRowContext(ctx, `SELECT name,role,scope,coalesce(last_used_at,'')<? FROM tokens WHERE token_hash=? AND revoked_at IS NULL`, cutoff, hash).Scan(&auth.Name, &auth.Role, &auth.Scope, &stale)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenAuth{}, ErrNotFound
	}
	if err != nil || !stale {
		return auth, err
	}
	// The guard repeats the staleness check so that concurrent requests
	// write once between them.
	_, err = s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at=? WHERE token_hash=? AND revoked_at IS NULL AND coalesce(last_used_at,'')<?`, stamp(now), hash, cutoff)
	return auth, err
}

// TokenName returns the name and role of a valid token and records its use,
// at most once per tokenUseInterval.
func (s *Store) TokenName(ctx context.Context, token string) (name, role string, err error) {
	auth, err := s.Authenticate(ctx, token)
	return auth.Name, auth.Role, err
}

// addColumns adds columns that a database from an older Machinist lacks.
func addColumns(db *sql.DB, table string, columns ...string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, column := range columns {
		name, _, _ := strings.Cut(column, " ")
		if have[name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column); err != nil {
			return err
		}
	}
	return nil
}
