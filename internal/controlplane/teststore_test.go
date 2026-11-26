package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/owainlewis/machinist/internal/tickets"
)

// openTestStore opens a fresh SQLite store by default, or an isolated
// per-test PostgreSQL schema when TEST_POSTGRES_URL is set.
func openTestStore(t *testing.T) *tickets.Store {
	t.Helper()
	if pgURL := strings.TrimSpace(os.Getenv("TEST_POSTGRES_URL")); pgURL != "" {
		return openPostgresTestStore(t, pgURL)
	}
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func openPostgresTestStore(t *testing.T, pgURL string) *tickets.Store {
	t.Helper()
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	schemaName := "test_" + hex.EncodeToString(buf)

	adminCfg, err := pgx.ParseConfig(pgURL)
	if err != nil {
		t.Fatalf("parse TEST_POSTGRES_URL: %v", err)
	}
	adminCfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	adminDB := stdlib.OpenDB(*adminCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		_ = adminDB.Close()
		t.Fatalf("CREATE SCHEMA %s: %v", schemaName, err)
	}

	parsed, err := url.Parse(pgURL)
	if err != nil {
		_, _ = adminDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE")
		_ = adminDB.Close()
		t.Fatalf("url.Parse TEST_POSTGRES_URL: %v", err)
	}
	q := parsed.Query()
	q.Set("search_path", schemaName)
	parsed.RawQuery = q.Encode()

	store, err := tickets.Open(parsed.String())
	if err != nil {
		_, _ = adminDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE")
		_ = adminDB.Close()
		t.Fatalf("Open postgres schema %s: %v", schemaName, err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = adminDB.ExecContext(dropCtx, "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE")
		_ = adminDB.Close()
	})
	return store
}
