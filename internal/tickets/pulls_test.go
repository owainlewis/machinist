package tickets

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestADatabaseFromBeforePRStatesOpensAndKeepsItsJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tickets.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE tickets (
  id TEXT PRIMARY KEY, source TEXT NOT NULL, repo TEXT NOT NULL, issue_number INTEGER, title TEXT NOT NULL,
  state TEXT NOT NULL, agent TEXT NOT NULL, pr_number INTEGER, created_by TEXT NOT NULL, human_commits INTEGER,
  merged_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO tickets VALUES ('t_old','api','owner/repo',NULL,'Old','pr_ready','default',7,'alice',NULL,NULL,'2026-11-01T00:00:00Z','2026-11-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	for range 2 {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		open, err := store.TicketsWithOpenPRs(t.Context())
		store.Close()
		if err != nil || len(open) != 1 || open[0].ID != "t_old" || open[0].PRNumber != 7 || open[0].PRState != "" {
			t.Fatalf("open = %+v, err = %v", open, err)
		}
	}
}
