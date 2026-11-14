package cli

import (
	"bytes"
	"context"
	"path/filepath"
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
	path := writeConfigFile(t, t.TempDir(), "config.toml", "[server]\ndatabase = \"tickets.db\"\n\n[github]\nhandle = \"bot\"\n")
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
		_, err = store.TokenName(context.Background(), token)
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
