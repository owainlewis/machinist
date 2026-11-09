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
	StateNeedsYou  = "needs_you"
	StatePRReady   = "pr_ready"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"

	RunKindAgent    = "agent"
	RunKindReviewer = "reviewer"

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
)

var (
	ErrNotFound     = errors.New("not found")
	ErrRunNotActive = errors.New("run is not active")
	ErrTicketEnded  = errors.New("ticket has ended")
)

const schema = `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS tickets (
  id TEXT PRIMARY KEY, source TEXT NOT NULL, repo TEXT NOT NULL, issue_number INTEGER, title TEXT NOT NULL,
  state TEXT NOT NULL, agent TEXT NOT NULL, pr_number INTEGER, created_by TEXT NOT NULL, human_commits INTEGER,
  merged_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_ticket_per_issue ON tickets(repo, issue_number)
  WHERE issue_number IS NOT NULL AND state IN ('working','needs_you','pr_ready');
CREATE TABLE IF NOT EXISTS ticket_entries (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ticket_id TEXT NOT NULL, kind TEXT NOT NULL, author TEXT NOT NULL,
  body TEXT NOT NULL, source_key TEXT UNIQUE, created_at TEXT NOT NULL, consumed_by_run TEXT, posted_at TEXT);
CREATE INDEX IF NOT EXISTS ticket_entries_ticket ON ticket_entries(ticket_id, id);
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY, ticket_id TEXT NOT NULL, kind TEXT NOT NULL, reviewer TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL, runner TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL, lease_expires_at TEXT, tokens INTEGER, cost REAL, report TEXT, started_at TEXT, ended_at TEXT);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_agent_run ON runs(ticket_id) WHERE kind='agent' AND status IN ('queued','running');
CREATE INDEX IF NOT EXISTS runs_status ON runs(status);
CREATE TABLE IF NOT EXISTS tokens (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, last_used_at TEXT, revoked_at TEXT);
CREATE TABLE IF NOT EXISTS source_cursors (source TEXT NOT NULL, repo TEXT NOT NULL, cursor TEXT NOT NULL, PRIMARY KEY(source, repo));
CREATE TABLE IF NOT EXISTS run_activity_events (run TEXT NOT NULL, sequence INTEGER NOT NULL, body TEXT NOT NULL, PRIMARY KEY(run, sequence));`

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Ticket struct {
	ID           string     `json:"id"`
	Source       string     `json:"source"`
	Repo         string     `json:"repo"`
	IssueNumber  int        `json:"issue_number,omitempty"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	Agent        string     `json:"agent"`
	PRNumber     int        `json:"pr_number,omitempty"`
	CreatedBy    string     `json:"created_by"`
	HumanCommits *int       `json:"human_commits,omitempty"`
	MergedAt     *time.Time `json:"merged_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Branch is the one branch every run of this ticket works on.
func (t Ticket) Branch() string { return "machinist/" + t.ID }

// Automation reports whether the ticket came from a schedule. Such a ticket
// never waits for a person.
func (t Ticket) Automation() bool { return strings.HasPrefix(t.Source, "schedule:") }

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

type Run struct {
	ID        string           `json:"id"`
	TicketID  string           `json:"ticket_id"`
	Kind      string           `json:"kind"`
	Reviewer  string           `json:"reviewer,omitempty"`
	Harness   string           `json:"harness"`
	Runner    string           `json:"runner,omitempty"`
	Status    string           `json:"status"`
	Tokens    *int64           `json:"tokens,omitempty"`
	Cost      *float64         `json:"cost,omitempty"`
	Report    *protocol.Report `json:"report,omitempty"`
	StartedAt *time.Time       `json:"started_at,omitempty"`
	EndedAt   *time.Time       `json:"ended_at,omitempty"`
}

// NewTicket is one piece of intake. SourceKey makes it idempotent.
type NewTicket struct {
	Source      string
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
	Link      string
	Harness   string
	Reviewers []string
}

// Transition is the result of one row of the ticket state table.
type Transition struct {
	State    string
	Message  string
	Report   string
	PRNumber int
	FirstPR  *FirstPR
}

// RunCompletion is what a runner reported when a run ended.
type RunCompletion struct {
	RunID  string
	Status string
	Report *protocol.Report
	Tokens *int64
	Cost   *float64
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
	if err = os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) Close() error { return s.db.Close() }

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

const ticketColumns = `id,source,repo,issue_number,title,state,agent,pr_number,created_by,human_commits,merged_at,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scanTicket(row scanner) (Ticket, error) {
	var ticket Ticket
	var issue, pr, commits sql.NullInt64
	var merged sql.NullString
	var created, updated string
	if err := row.Scan(&ticket.ID, &ticket.Source, &ticket.Repo, &issue, &ticket.Title, &ticket.State, &ticket.Agent, &pr, &ticket.CreatedBy, &commits, &merged, &created, &updated); err != nil {
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

const runColumns = `id,ticket_id,kind,reviewer,harness,runner,status,tokens,cost,report,started_at,ended_at`

func scanRun(row scanner) (Run, error) {
	var run Run
	var tokens sql.NullInt64
	var cost sql.NullFloat64
	var report, started, ended sql.NullString
	if err := row.Scan(&run.ID, &run.TicketID, &run.Kind, &run.Reviewer, &run.Harness, &run.Runner, &run.Status, &tokens, &cost, &report, &started, &ended); err != nil {
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

// Runs lists a ticket's runs, oldest first.
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
	return runs, rows.Err()
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

// insertMessage adds an outbound message. With no issue or PR there is nowhere
// to post it, so it is marked handled and shows only in Machinist.
func insertMessage(ctx context.Context, q querier, ticket Ticket, prNumber int, body string, sourceKey any, now time.Time) error {
	var posted any
	if ticket.IssueNumber == 0 && ticket.PRNumber == 0 && prNumber == 0 {
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
			existing, err = scanTicket(tx.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE repo=? AND issue_number=? AND state IN ('working','needs_you','pr_ready')`, request.Repo, request.IssueNumber))
			if err == nil {
				ticket = existing
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		now := s.now()
		if _, err = tx.ExecContext(ctx, `INSERT INTO tickets(id,source,repo,issue_number,title,state,agent,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, request.Source, request.Repo, nullableInt(request.IssueNumber), request.Title, StateWorking, request.Agent, request.CreatedBy, stamp(now), stamp(now)); err != nil {
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
		if ticket.State == StatePRReady || (ticket.State == StateNeedsYou && input.Kind == EntryComment) {
			next = StateWorking
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,updated_at=? WHERE id=?`, next, stamp(now), ticketID); err != nil {
			return err
		}
		ticket, err = loadTicket(ctx, tx, ticketID)
		return err
	})
	return ticket, added, err
}

func activeState(state string) bool {
	return state == StateWorking || state == StateNeedsYou || state == StatePRReady
}

// Cancel ends a ticket. A queued run stops here. A running run is told to stop
// by its next heartbeat.
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
		if err = applyTransition(ctx, tx, ticket, Transition{State: StateCancelled, Message: "Cancelled by " + by}, now); err != nil {
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

// PRMerged ends a ticket whose PR merged. A queued run stops here. A running run
// is told to stop by its next heartbeat.
func (s *Store) PRMerged(ctx context.Context, ticketID string, humanCommits int, mergedAt time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		ticket, err := loadTicket(ctx, tx, ticketID)
		if err != nil || !activeState(ticket.State) {
			return err
		}
		now := s.now()
		if err = cancelQueuedRuns(ctx, tx, ticketID, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,human_commits=?,merged_at=?,updated_at=? WHERE id=?`, StateDone, humanCommits, stamp(mergedAt), stamp(now), ticketID)
		return err
	})
}

// PRClosed records a PR closed without merging. The key makes each close count
// once, so a reply to the message can continue the ticket on the closed PR.
func (s *Store) PRClosed(ctx context.Context, ticketID, key, next, message string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		ticket, err := loadTicket(ctx, tx, ticketID)
		if err != nil || !activeState(ticket.State) {
			return err
		}
		if _, err = ticketForSourceKey(ctx, tx, key); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := s.now()
		if err = cancelQueuedRuns(ctx, tx, ticketID, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tickets SET state=?,updated_at=? WHERE id=?`, next, stamp(now), ticketID); err != nil {
			return err
		}
		return insertMessage(ctx, tx, ticket, 0, message, key, now)
	})
}

func queueRun(ctx context.Context, q querier, ticketID, kind, reviewer, harness string) (string, error) {
	id, err := randomHex(12)
	if err != nil {
		return "", err
	}
	id = "run_" + id
	_, err = q.ExecContext(ctx, `INSERT INTO runs(id,ticket_id,kind,reviewer,harness,status) VALUES(?,?,?,?,?,?)`, id, ticketID, kind, reviewer, harness, RunQueued)
	return id, err
}

func applyTransition(ctx context.Context, q querier, ticket Ticket, transition Transition, now time.Time) error {
	if _, err := q.ExecContext(ctx, `UPDATE tickets SET state=?,pr_number=COALESCE(?,pr_number),updated_at=? WHERE id=?`, transition.State, nullableInt(transition.PRNumber), stamp(now), ticket.ID); err != nil {
		return err
	}
	if transition.Report != "" {
		if err := insertEntry(ctx, q, ticket.ID, EntryReport, ticket.Agent, transition.Report, nil, now); err != nil {
			return err
		}
	}
	if transition.Message != "" {
		if err := insertMessage(ctx, q, ticket, transition.PRNumber, transition.Message, nil, now); err != nil {
			return err
		}
	}
	if transition.FirstPR == nil {
		return nil
	}
	if transition.FirstPR.Link != "" {
		if err := insertMessage(ctx, q, ticket, transition.PRNumber, transition.FirstPR.Link, nil, now); err != nil {
			return err
		}
	}
	for _, reviewer := range transition.FirstPR.Reviewers {
		if _, err := queueRun(ctx, q, ticket.ID, RunKindReviewer, reviewer, transition.FirstPR.Harness); err != nil {
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

// WaitingCI returns the newest CI signal no run has read yet.
func (s *Store) WaitingCI(ctx context.Context, ticketID string) (string, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM ticket_entries WHERE ticket_id=? AND kind=? AND consumed_by_run IS NULL ORDER BY id DESC LIMIT 1`, ticketID, EntryCI).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return body, err
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

// QueueRun adds an agent run for a working ticket.
func (s *Store) QueueRun(ctx context.Context, ticketID, harness string) (Run, error) {
	var run Run
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		id, err := queueRun(ctx, tx, ticketID, RunKindAgent, "", harness)
		if err != nil {
			return err
		}
		run, err = loadRun(ctx, tx, id)
		return err
	})
	return run, err
}

// ClaimRun gives the oldest queued run for these harnesses to a runner and marks
// waiting input as read by it. It returns nil when no run is queued.
func (s *Store) ClaimRun(ctx context.Context, runner string, harnesses []string, leaseUntil time.Time) (*Claim, error) {
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
		run, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE status=? AND harness IN (`+placeholders+`) ORDER BY rowid LIMIT 1`, arguments...))
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
		cancel = !activeState(ticket.State) || (run.Kind == RunKindAgent && ticket.State != StateWorking)
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
// picks the next state. Waiting input then returns the ticket to working, unless
// it ended or reached its turn limit.
func (s *Store) CompleteRun(ctx context.Context, completion RunCompletion, maxTurns int, decide func(Ticket, string) Transition) error {
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
		isTurn := run.Kind == RunKindAgent && ticket.State == StateWorking
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
		if !isTurn {
			return nil
		}
		transition := decide(ticket, status)
		if transition.State == StateNeedsYou || transition.State == StatePRReady {
			var waiting int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ticket_entries WHERE ticket_id=? AND consumed_by_run IS NULL AND kind IN (?,?,?,?)`, ticket.ID, EntryRequest, EntryComment, EntryReview, EntryCI).Scan(&waiting); err != nil {
				return err
			}
			turns, err := turnsSinceHuman(ctx, tx, ticket.ID)
			if err != nil {
				return err
			}
			// From pr_ready the engine applies the turn limit and writes its message.
			if waiting > 0 && (turns < maxTurns || transition.State == StatePRReady) {
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
		}
		return nil
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
	return scanTicket(s.db.QueryRowContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE repo=? AND (issue_number=? OR pr_number=?) AND state IN (?,?,?) ORDER BY created_at DESC LIMIT 1`, repo, number, number, StateWorking, StateNeedsYou, StatePRReady))
}

// ActiveWithPR lists the active tickets that have a PR.
func (s *Store) ActiveWithPR(ctx context.Context) ([]Ticket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ticketColumns+` FROM tickets WHERE pr_number IS NOT NULL AND state IN (?,?,?) ORDER BY created_at`, StateWorking, StateNeedsYou, StatePRReady)
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

// HasSourceKey reports whether a piece of intake was already recorded.
func (s *Store) HasSourceKey(ctx context.Context, key string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM ticket_entries WHERE source_key=?`, key).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
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

// UnpostedMessages lists messages to post, oldest first, on the issue or
// otherwise the PR.
func (s *Store) UnpostedMessages(ctx context.Context) ([]Outbound, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,t.repo,COALESCE(t.issue_number,t.pr_number),e.body FROM ticket_entries e JOIN tickets t ON t.id=e.ticket_id WHERE e.kind=? AND e.posted_at IS NULL AND COALESCE(t.issue_number,t.pr_number) IS NOT NULL ORDER BY e.id`, EntryMessage)
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

// CreateToken stores a new token and returns its only plaintext copy.
func (s *Store) CreateToken(ctx context.Context, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("token name is required")
	}
	id, err := randomHex(8)
	if err != nil {
		return "", err
	}
	token, err := randomHex(32)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO tokens(id,name,token_hash,created_at) VALUES(?,?,?,?)`, id, name, hashToken(token), stamp(s.now()))
	return token, err
}

// TokenName returns the name of a valid token and records its use.
func (s *Store) TokenName(ctx context.Context, token string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `UPDATE tokens SET last_used_at=? WHERE token_hash=? AND revoked_at IS NULL RETURNING name`, stamp(s.now()), hashToken(token)).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
