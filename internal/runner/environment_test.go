package runner

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/config"
)

func TestExecuteUsesOnlyTheGivenEnvironment(t *testing.T) {
	t.Setenv("MACHINIST_PARENT_SECRET", "must-not-leak")
	var stdout bytes.Buffer
	_, err := Execute(t.Context(), Options{
		Environment:   []string{"PATH=" + os.Getenv("PATH"), "GH_TOKEN=for-the-agent", "GIT_CONFIG_COUNT=1"},
		Prompt:        testPrompt,
		Command:       config.ResolvedCommand{Name: "plan", Command: []string{"/bin/sh", "-c", "cat >/dev/null; env"}, Timeout: testTimeout},
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        &stdout,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if strings.Contains(output, "must-not-leak") {
		t.Fatalf("parent environment leaked: %s", output)
	}
	for _, want := range []string{"GH_TOKEN=for-the-agent", "GIT_CONFIG_COUNT=1", "MACHINIST_RUN_ID="} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %s in: %s", want, output)
		}
	}
}

func TestSanitizedEnvironmentRemovesRepositoryOverrides(t *testing.T) {
	joined := strings.Join(SanitizedEnvironment([]string{"PATH=/bin", "GIT_DIR=/other", "GIT_WORK_TREE=/other", "GIT_CONFIG_KEY_0=x", "GH_TOKEN=kept"}), "\n")
	if joined != "PATH=/bin\nGH_TOKEN=kept" {
		t.Fatalf("environment = %q", joined)
	}
}
