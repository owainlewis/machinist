package runner

import (
	"strings"
	"testing"
	"time"
)

func TestHarnessCommandsAreAcceptedByTheirAdapters(t *testing.T) {
	claude, err := HarnessCommand(HarnessClaude, "sonnet", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := nativeCommand(claude)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--print", "--verbose", "--output-format stream-json", "--include-partial-messages", "--dangerously-skip-permissions", "--json-schema", "--model sonnet"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	if strings.Count(joined, "--output-format") != 1 {
		t.Fatalf("output format repeated: %s", joined)
	}

	codex, err := HarnessCommand(HarnessCodex, "fixture", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	argv, err = nativeCommand(codex)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); got != `codex app-server --stdio -c features.multi_agent=true -c sandbox_mode="danger-full-access"` {
		t.Fatalf("codex argv = %s", got)
	}
}

func TestHarnessCommandRejectsUnknownHarness(t *testing.T) {
	if _, err := HarnessCommand("gemini", "", time.Minute); err == nil {
		t.Fatal("unknown harness accepted")
	}
}
