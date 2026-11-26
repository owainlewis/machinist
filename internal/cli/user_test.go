package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/tickets"
)

func TestUserCLILifecycle(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	dbPath := filepath.Join(dir, ".machinist", "tickets.db")
	cfgYAML := "github:\n  handle: machinist-bot\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// Seed 2 users directly via Store.
	store, err := tickets.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveGitHubLogin(t.Context(), tickets.GitHubProfile{ID: "1", Login: "owain", Name: "Owain", PrimaryEmail: "o@e.com", VerifiedEmails: []string{"o@e.com"}}, "github:owain", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateInvite(t.Context(), tickets.UserRoleMember, "alice", "", "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveGitHubLogin(t.Context(), tickets.GitHubProfile{ID: "2", Login: "alice", Name: "Alice", PrimaryEmail: "a@e.com", VerifiedEmails: []string{"a@e.com"}}, "", ""); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	runCLI := func(args ...string) (string, string, int) {
		var stdout, stderr bytes.Buffer
		full := append([]string{"--config", cfgPath}, args...)
		code := Execute(t.Context(), full, strings.NewReader(""), &stdout, &stderr, "v1")
		return stdout.String(), stderr.String(), code
	}

	out, errOut, code := runCLI("user", "list")
	if code != 0 || !strings.Contains(out, "owain") || !strings.Contains(out, "alice") {
		t.Fatalf("user list code=%d out=%q err=%q", code, out, errOut)
	}

	// Sole admin cannot be disabled via CLI.
	_, errOut, code = runCLI("user", "disable", "owain")
	if code == 0 || !strings.Contains(errOut, "last active admin") {
		t.Fatalf("disable sole admin code=%d err=%q", code, errOut)
	}

	out, errOut, code = runCLI("user", "set-role", "alice", "viewer")
	if code != 0 || !strings.Contains(out, "role to viewer") {
		t.Fatalf("set-role code=%d out=%q err=%q", code, out, errOut)
	}

	out, errOut, code = runCLI("user", "disable", "alice")
	if code != 0 || !strings.Contains(out, "disabled alice") {
		t.Fatalf("disable alice code=%d out=%q err=%q", code, out, errOut)
	}

	out, errOut, code = runCLI("user", "enable", "alice")
	if code != 0 || !strings.Contains(out, "enabled alice") {
		t.Fatalf("enable alice code=%d out=%q err=%q", code, out, errOut)
	}
}
