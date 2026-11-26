package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInviteCLICreateListRevoke(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := "server:\n  public_url: https://machinist.example.com\ngithub:\n  handle: machinist-bot\n"
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	runCLI := func(args ...string) (string, string, int) {
		var stdout, stderr bytes.Buffer
		full := append([]string{"--config", cfgPath}, args...)
		code := Execute(t.Context(), full, strings.NewReader(""), &stdout, &stderr, "v1")
		return stdout.String(), stderr.String(), code
	}

	out, errOut, code := runCLI("invite", "create", "--role", "member", "--github", "Alice")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "https://machinist.example.com/#/invite#minv_") {
		t.Fatalf("invite create code=%d out=%q err=%q", code, out, errOut)
	}

	out, errOut, code = runCLI("invite", "list")
	if code != 0 || !strings.Contains(out, "github:alice") || !strings.Contains(out, "member") {
		t.Fatalf("invite list code=%d out=%q err=%q", code, out, errOut)
	}

	// Extract invite ID from the second line of tabwriter output.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected list output: %q", out)
	}
	invID := strings.Fields(lines[1])[0]

	out, errOut, code = runCLI("invite", "revoke", invID)
	if code != 0 || !strings.Contains(out, "revoked "+invID) {
		t.Fatalf("invite revoke code=%d out=%q err=%q", code, out, errOut)
	}

	out, _, code = runCLI("invite", "list")
	if code != 0 || strings.Contains(out, invID) {
		t.Fatalf("revoked invite still listed: %q", out)
	}
}
