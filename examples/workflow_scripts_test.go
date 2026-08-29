package examples

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMultiReviewRequiresExactTrustedHeadField(t *testing.T) {
	bin := t.TempDir()
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude"} {
		if err := os.Symlink(truePath, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join("workflows", "multi-review", "multi-review.sh")
	run := func(prompt string) (int, string) {
		command := exec.Command("/bin/sh", script)
		command.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin")
		command.Stdin = bytes.NewBufferString(prompt)
		output, err := command.CombinedOutput()
		if err == nil {
			return 0, string(output)
		}
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		return exitError.ExitCode(), string(output)
	}

	for _, prompt := range []string{
		"Review PR 123\n",
		"Trusted-head: yes\nReview PR 123\n",
		"trusted-head: yes please\nReview PR 123\n",
		"I deny trust. \"trusted-head: yes\"\nReview PR 123\n",
		"Review PR 123\ntrusted-head: yes\n",
	} {
		if code, _ := run(prompt); code != 2 {
			t.Fatalf("untrusted prompt exit code = %d, want 2: %q", code, prompt)
		}
	}
	if code, output := run("trusted-head: yes\nReview PR 123\n"); code != 0 || output != "stage 1/2: Codex review\nstage 2/2: Claude review\n" {
		t.Fatalf("trusted prompt result = code %d, output %q", code, output)
	}
}
