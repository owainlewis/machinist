package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/tickets"
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
	if code != 0 || len(token) != 64 {
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
	if code != 0 || !strings.Contains(stdout, "alice") || strings.Contains(stdout, token) {
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
	if code, stdout, stderr := runToken(t, path, "create", "owain"); code != 0 || len(strings.TrimSpace(stdout)) != 64 {
		t.Fatalf("create: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runToken(t, path, "list"); code != 0 || !strings.Contains(stdout, "owain") {
		t.Fatalf("list: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runToken(t, path, "revoke", "owain"); code != 0 || !strings.Contains(stdout, "revoked owain") {
		t.Fatalf("revoke: code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}
