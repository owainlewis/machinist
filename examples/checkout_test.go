package examples

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runCheckoutScript(t *testing.T, failCount int, leaveGarbage bool) (string, int, []byte, error) {
	t.Helper()
	binDir := t.TempDir()
	workDir := t.TempDir()
	stateFile := filepath.Join(binDir, "attempts")
	branchFile := filepath.Join(binDir, "branch")

	// Fake gh that fails failCount times before succeeding.
	garbageLine := ""
	if leaveGarbage {
		garbageLine = "mkdir -p .git && echo partial > .git/HEAD\n"
	}
	ghScript := "#!/bin/sh\n" +
		"set -e\n" +
		"n=0\n" +
		"[ -f \"" + stateFile + "\" ] && n=$(cat \"" + stateFile + "\")\n" +
		"n=$((n + 1))\n" +
		"echo \"$n\" > \"" + stateFile + "\"\n" +
		"if [ \"$n\" -le " + string(rune('0'+failCount)) + " ]; then\n" +
		garbageLine +
		"  echo \"transient clone error $n\" >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"if [ -n \"$(ls -A .)\" ]; then\n" +
		"  echo \"destination path '.' already exists and is not an empty directory\" >&2\n" +
		"  exit 128\n" +
		"fi\n" +
		"mkdir -p .git\n" +
		"echo cloned > README.md\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(ghScript), 0o700); err != nil {
		t.Fatal(err)
	}

	// Fake git that records git switch calls.
	gitScript := "#!/bin/sh\n" +
		"if [ \"$1\" = switch ] && [ \"$2\" = -c ]; then\n" +
		"  echo \"$3\" > \"" + branchFile + "\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(gitScript), 0o700); err != nil {
		t.Fatal(err)
	}

	scriptPath, err := filepath.Abs(filepath.Join("factory", "scripts", "checkout.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", scriptPath)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"MACHINIST_REPO=owner/repo",
		"MACHINIST_BRANCH=machinist/t_1",
		"MACHINIST_CLONE_RETRY_DELAY=0",
	)
	output, runErr := cmd.CombinedOutput()

	attempts := 0
	if raw, err := os.ReadFile(stateFile); err == nil {
		for _, ch := range strings.TrimSpace(string(raw)) {
			if ch >= '0' && ch <= '9' {
				attempts = attempts*10 + int(ch-'0')
			}
		}
	}
	branchBytes, _ := os.ReadFile(branchFile)
	return strings.TrimSpace(string(branchBytes)), attempts, output, runErr
}

func TestCheckoutRetriesTransientCloneFailures(t *testing.T) {
	branch, attempts, output, err := runCheckoutScript(t, 2, true)
	if err != nil {
		t.Fatalf("checkout.sh failed after 2 transient errors: %v\noutput:\n%s", err, output)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3\noutput:\n%s", attempts, output)
	}
	if branch != "machinist/t_1" {
		t.Fatalf("branch = %q, want machinist/t_1", branch)
	}
}

func TestCheckoutGivesUpAfterThreeCloneFailures(t *testing.T) {
	_, attempts, output, err := runCheckoutScript(t, 5, false)
	if err == nil {
		t.Fatalf("expected checkout.sh to fail after 3 attempts, output:\n%s", output)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3\noutput:\n%s", attempts, output)
	}
}

func TestCheckoutSucceedsOnFirstAttempt(t *testing.T) {
	branch, attempts, output, err := runCheckoutScript(t, 0, false)
	if err != nil {
		t.Fatalf("checkout.sh failed: %v\noutput:\n%s", err, output)
	}
	if attempts != 1 || branch != "machinist/t_1" {
		t.Fatalf("attempts = %d, branch = %q", attempts, branch)
	}
}
