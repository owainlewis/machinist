package tickets

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestDialectRebind(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "basic placeholders",
			in:   "SELECT * FROM tickets WHERE id=? AND state=?",
			want: "SELECT * FROM tickets WHERE id=$1 AND state=$2",
		},
		{
			name: "preserves question mark inside single quotes and escaped quotes",
			in:   "SELECT 'don''t?' FROM t WHERE a=? AND b='a?b' AND c=?",
			want: "SELECT 'don''t?' FROM t WHERE a=$1 AND b='a?b' AND c=$2",
		},
		{
			name: "preserves question marks in line and block comments",
			in:   "SELECT ? -- why?\n/* what? */ FROM t WHERE x=?",
			want: "SELECT $1 -- why?\n/* what? */ FROM t WHERE x=$2",
		},
		{
			name: "no placeholders unchanged",
			in:   "SELECT 1",
			want: "SELECT 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dialectSQLite.rebind(tc.in); got != tc.in {
				t.Fatalf("sqlite rebind mutated query: %q", got)
			}
			if got := dialectPostgres.rebind(tc.in); got != tc.want {
				t.Fatalf("postgres rebind =\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
	if dialectSQLite.forUpdate() != "" {
		t.Fatalf("sqlite forUpdate = %q, want empty", dialectSQLite.forUpdate())
	}
	if dialectPostgres.forUpdate() != " FOR UPDATE" {
		t.Fatalf("postgres forUpdate = %q, want FOR UPDATE", dialectPostgres.forUpdate())
	}
	if boolToInt(false) != 0 || boolToInt(true) != 1 {
		t.Fatalf("boolToInt unexpected results")
	}
	if !isUniqueViolation(errors.New("constraint failed: UNIQUE constraint failed: tokens.name")) {
		t.Fatal("expected sqlite unique violation match")
	}
	if !isUniqueViolation(errors.New("ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)")) {
		t.Fatal("expected postgres unique violation match")
	}
	if isUniqueViolation(nil) || isUniqueViolation(errors.New("other error")) {
		t.Fatal("unexpected unique violation match")
	}
}

func TestSQLiteSeqBackfillPreservesOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Create legacy tickets/runs tables without seq column and insert rows with identical timestamps.
	now := stamp(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	if _, err := db.Exec(`
		CREATE TABLE tickets (
			id TEXT PRIMARY KEY, source TEXT NOT NULL, repo TEXT NOT NULL, issue_number INTEGER, title TEXT NOT NULL,
			state TEXT NOT NULL, agent TEXT NOT NULL, pr_number INTEGER, created_by TEXT NOT NULL, human_commits INTEGER,
			merged_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE runs (
			id TEXT PRIMARY KEY, ticket_id TEXT NOT NULL, kind TEXT NOT NULL, reviewer TEXT NOT NULL DEFAULT '',
			harness TEXT NOT NULL, runner TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, lease_expires_at TEXT,
			tokens INTEGER, cost REAL, report TEXT, queued_at TEXT, started_at TEXT, ended_at TEXT
		);
		INSERT INTO tickets(id,source,repo,title,state,agent,created_by,created_at,updated_at) VALUES
			('t_first','schedule:nightly','org/repo','first','done','default','sched','` + now + `','` + now + `'),
			('t_second','schedule:nightly','org/repo','second','working','default','sched','` + now + `','` + now + `');
		INSERT INTO runs(id,ticket_id,kind,harness,status,queued_at) VALUES
			('run_1','t_second','agent','codex','failed','` + now + `'),
			('run_2','t_second','agent','codex','queued','` + now + `');
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	latest, err := store.LatestTicketBySource(ctx, "schedule:nightly")
	if err != nil {
		t.Fatalf("LatestTicketBySource: %v", err)
	}
	if latest.ID != "t_second" {
		t.Fatalf("LatestTicketBySource = %s, want t_second", latest.ID)
	}
	runs, err := store.Runs(ctx, "t_second")
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != "run_1" || runs[1].ID != "run_2" {
		t.Fatalf("Runs order = %+v, want [run_1, run_2]", runs)
	}
	// New ticket inserted after upgrade must get seq > backfilled rows.
	third, created, err := store.CreateTicket(ctx, NewTicket{
		Source:    "api",
		Repo:      "org/repo",
		Title:     "third",
		Body:      "do thing",
		Agent:     "default",
		CreatedBy: "alice",
		SourceKey: "req:third",
	})
	if err != nil || !created {
		t.Fatalf("CreateTicket: %v (created=%v)", err, created)
	}
	queued, err := store.QueuedTickets(ctx)
	if err != nil {
		t.Fatalf("QueuedTickets: %v", err)
	}
	if len(queued) != 2 || queued[0].ID != "t_second" || queued[1].ID != third.ID {
		t.Fatalf("QueuedTickets order = %+v, want [t_second, %s]", queued, third.ID)
	}
}
