package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/tickets"
	_ "modernc.org/sqlite"
)

func runToken(t *testing.T, path string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), append(append([]string{"token"}, args...), "--config="+path), strings.NewReader(""), &stdout, &stderr, "test")
	return code, stdout.String(), stderr.String()
}

func TestTokenLifecycle(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "config.yaml", "server:\n  database: tickets.db\n\ngithub:\n  handle: bot\n")
	code, stdout, stderr := runToken(t, path, "create", "alice")
	token := strings.TrimSpace(stdout)
	if code != 0 || !strings.HasPrefix(token, tickets.TokenPrefix) || len(token) != len(tickets.TokenPrefix)+64 {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	machinistConfig, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if machinistConfig.Server.Database != filepath.Join(filepath.Dir(path), "tickets.db") {
		t.Fatalf("database = %s", machinistConfig.Server.Database)
	}
	valid := func() error {
		store, err := tickets.Open(machinistConfig.Server.Database)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		_, _, err = store.TokenName(context.Background(), token)
		return err
	}
	if err := valid(); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := runToken(t, path, "create", "alice"); code != 2 || !strings.Contains(stderr, "exists") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	code, stdout, _ = runToken(t, path, "list")
	wantPrefix := token[:len(tickets.TokenPrefix)+4] + "…"
	if code != 0 || !strings.Contains(stdout, "alice") || !strings.Contains(stdout, "PREFIX") || !strings.Contains(stdout, wantPrefix) || strings.Contains(stdout, token) {
		t.Fatalf("code = %d, stdout = %q", code, stdout)
	}
	if code, stdout, _ := runToken(t, path, "revoke", "alice"); code != 0 || !strings.Contains(stdout, "revoked alice") {
		t.Fatalf("code = %d, stdout = %q", code, stdout)
	}
	if err := valid(); err == nil {
		t.Fatal("revoked token still works")
	}
	if code, _, stderr := runToken(t, path, "revoke", "alice"); code != 2 || !strings.Contains(stderr, "no active token") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if code, stdout, _ := runToken(t, path, "create", "alice"); code != 0 || strings.TrimSpace(stdout) == token {
		t.Fatalf("code = %d, stdout = %q", code, stdout)
	}
}

func TestTokenListShowsNoPrefixForLegacyTokens(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "config.yaml", "server:\n  database: tickets.db\n\ngithub:\n  handle: bot\n")
	machinistConfig, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tickets.Open(machinistConfig.Server.Database)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	legacy := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	sum := sha256.Sum256([]byte(legacy))
	db, err := sql.Open("sqlite", machinistConfig.Server.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO tokens(id,name,token_hash,created_at,role) VALUES('legacy1','old-token',?,'2026-10-01T12:00:00.000000000Z','')`, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runToken(t, path, "list")
	if code != 0 || !regexp.MustCompile(`old-token\s+any\s+No prefix`).MatchString(stdout) {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestTokenCreateGivesARole(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "config.yaml", "server:\n  database: tickets.db\n\ngithub:\n  handle: bot\n")
	if code, _, stderr := runToken(t, path, "create", "alice"); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if code, _, stderr := runToken(t, path, "create", "mac-mini", "--runner"); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	code, stdout, _ := runToken(t, path, "list")
	if code != 0 || !regexp.MustCompile(`alice\s+operator`).MatchString(stdout) || !regexp.MustCompile(`mac-mini\s+runner`).MatchString(stdout) {
		t.Fatalf("code = %d, stdout = %q", code, stdout)
	}
}

// The Linear credentials live in the service's environment, which a login
// shell does not have. The token commands only open the database, so they
// must not need them.
func TestTokenCommandsRunWithoutLinearCredentials(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "config.yaml", "server:\n  database: tickets.db\n\ngithub:\n  handle: bot\n\nlinear:\n  app_user_id: app-user\n  allowed_users: [owain]\n  teams:\n    ENG: owner/repo\n")
	t.Setenv(config.LinearTokenEnv, "")
	t.Setenv(config.LinearClientIDEnv, "")
	t.Setenv(config.LinearClientSecretEnv, "")
	if code, stdout, stderr := runToken(t, path, "create", "owain"); code != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), tickets.TokenPrefix) || len(strings.TrimSpace(stdout)) != len(tickets.TokenPrefix)+64 {
		t.Fatalf("create: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runToken(t, path, "list"); code != 0 || !strings.Contains(stdout, "owain") {
		t.Fatalf("list: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runToken(t, path, "revoke", "owain"); code != 0 || !strings.Contains(stdout, "revoked owain") {
		t.Fatalf("revoke: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestTokenCreateWithScopeAndRevokeIndividually(t *testing.T) {
	path := writeConfigFile(t, t.TempDir(), "config.yaml", "server:\n  database: tickets.db\n\ngithub:\n  handle: bot\n")
	code, outRun, stderr := runToken(t, path, "create", "grokbot", "--scope", "run")
	if code != 0 {
		t.Fatalf("create run: code = %d, stderr = %q", code, stderr)
	}
	code, outRead, stderr := runToken(t, path, "create", "watcher", "--scope", "read")
	if code != 0 {
		t.Fatalf("create read: code = %d, stderr = %q", code, stderr)
	}
	if code, _, stderr := runToken(t, path, "create", "bad", "--scope", "admin"); code == 0 || !strings.Contains(stderr, "scope") {
		t.Fatalf("invalid scope: code = %d, stderr = %q", code, stderr)
	}
	if code, _, stderr := runToken(t, path, "create", "bad2", "--runner", "--scope", "run"); code == 0 || !strings.Contains(stderr, "cannot be used together") {
		t.Fatalf("runner+scope: code = %d, stderr = %q", code, stderr)
	}

	code, stdout, stderr := runToken(t, path, "list")
	if code != 0 {
		t.Fatalf("list: code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "SCOPE") || !regexp.MustCompile(`grokbot\s+operator\s+mch_\S+\s+run`).MatchString(stdout) || !regexp.MustCompile(`watcher\s+operator\s+mch_\S+\s+read`).MatchString(stdout) {
		t.Fatalf("list output = %q", stdout)
	}

	// Revoking grokbot leaves watcher active.
	if code, stdout, stderr := runToken(t, path, "revoke", "grokbot"); code != 0 || !strings.Contains(stdout, "revoked grokbot") {
		t.Fatalf("revoke: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	machinistConfig, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tickets.Open(machinistConfig.Server.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Authenticate(context.Background(), strings.TrimSpace(outRun)); err == nil {
		t.Fatal("revoked grokbot token still authenticates")
	}
	if auth, err := store.Authenticate(context.Background(), strings.TrimSpace(outRead)); err != nil || auth.Name != "watcher" || auth.Scope != tickets.ScopeRead {
		t.Fatalf("watcher auth = %+v, err = %v", auth, err)
	}
}
