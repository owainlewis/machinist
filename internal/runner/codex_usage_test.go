package runner

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

func TestCodexUsageCollectorReadsFinalStructuredUsage(t *testing.T) {
	collector := newCodexUsageCollector("codex", []string{"codex", "exec", "--json", "-"})
	chunks := []string{
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":8,"output_tokens":2}}` + "\n" + `{"type":"item.completed",`,
		`"item":{"type":"agent_message","text":"done"}}` + "\n" + `{"type":"turn.completed","usage":{"input_tokens":40,`,
		`"cached_input_tokens":35,"output_tokens":3}}`,
	}
	for _, chunk := range chunks {
		if written, err := collector.Write([]byte(chunk)); err != nil || written != len(chunk) {
			t.Fatalf("write = %d, %v", written, err)
		}
	}
	if got := collector.tokenUsage(); got == nil || *got != 43 {
		t.Fatalf("token usage = %v, want 43", got)
	}
}

func TestCodexUsageCollectorLeavesInvalidUsageUnavailable(t *testing.T) {
	for _, test := range []struct {
		name string
		line string
	}{
		{name: "missing event", line: `{"type":"item.completed","item":{}}`},
		{name: "malformed JSON", line: `{"type":"turn.completed","usage":`},
		{name: "missing input", line: `{"type":"turn.completed","usage":{"output_tokens":2}}`},
		{name: "negative input", line: `{"type":"turn.completed","usage":{"input_tokens":-1,"output_tokens":2}}`},
		{name: "overflow", line: `{"type":"turn.completed","usage":{"input_tokens":` + "9223372036854775807" + `,"output_tokens":1}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := newCodexUsageCollector("codex", []string{"codex", "exec", "--json"})
			_, _ = collector.Write([]byte(test.line + "\n"))
			if got := collector.tokenUsage(); got != nil {
				t.Fatalf("token usage = %d, want unavailable", *got)
			}
		})
	}
}

func TestCodexUsageCollectorUsesTheLastCompletedTurn(t *testing.T) {
	collector := newCodexUsageCollector("codex", []string{"codex", "exec", "--json"})
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":5}}` + "\n"))
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":"invalid","output_tokens":5}}` + "\n"))
	if got := collector.tokenUsage(); got != nil {
		t.Fatalf("token usage = %d, want unavailable for malformed final event", *got)
	}
}

func TestCodexUsageCollectorInvalidatesUsageForTruncatedFinalCompletedTurn(t *testing.T) {
	collector := newCodexUsageCollector("codex", []string{"codex", "exec", "--json"})
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":5}}` + "\n"))
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":8`))
	if got := collector.tokenUsage(); got != nil {
		t.Fatalf("token usage = %d, want unavailable for truncated final event", *got)
	}
}

func TestCodexUsageCollectorIgnoresUnrelatedMalformedOutput(t *testing.T) {
	collector := newCodexUsageCollector("codex", []string{"codex", "exec", "--json"})
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":5}}` + "\n"))
	_, _ = collector.Write([]byte(`{"type":"item.completed","item":`))
	if got := collector.tokenUsage(); got == nil || *got != 9 {
		t.Fatalf("token usage = %v, want 9", got)
	}
}

func TestCodexUsageCollectorIsEnabledOnlyForStructuredCodexOutput(t *testing.T) {
	for _, test := range []struct {
		name     string
		executor string
		command  []string
	}{
		{name: "custom executor name", executor: "codex-local", command: []string{"codex", "exec", "--json"}},
		{name: "wrapped executable", executor: "codex", command: []string{"/usr/bin/env", "codex", "exec", "--json"}},
		{name: "renamed executable", executor: "codex", command: []string{"agent", "exec", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if collector := newCodexUsageCollector(test.executor, test.command); collector == nil {
				t.Fatalf("collector disabled for executor %q command %q", test.executor, test.command)
			}
		})
	}
	for _, test := range []struct {
		executor string
		command  []string
	}{
		{executor: "claude", command: []string{"claude", "--json"}},
		{executor: "codex", command: []string{"codex", "exec"}},
		{executor: "codex", command: []string{"agent", "serve", "--json"}},
		{executor: "custom", command: []string{"agent", "exec", "--json"}},
	} {
		if collector := newCodexUsageCollector(test.executor, test.command); collector != nil {
			t.Fatalf("collector enabled for executor %q command %q", test.executor, test.command)
		}
	}
}

func TestExecuteCollectsCodexUsageWithoutChangingOutput(t *testing.T) {
	const output = "{\"type\":\"item.completed\",\"item\":{}}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":100,\"cached_input_tokens\":80,\"output_tokens\":23}}\n"
	for _, test := range []struct {
		name           string
		executor       string
		executableName string
		wrapped        bool
	}{
		{name: "direct", executor: "codex-local", executableName: "codex"},
		{name: "wrapped", executor: "codex", executableName: "codex", wrapped: true},
		{name: "renamed", executor: "codex", executableName: "agent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := codexJSONAgentCommand(t, test.executor, test.executableName, test.wrapped, `cat >/dev/null; printf 999 > "$MACHINIST_TOKEN_USAGE_PATH"; printf '%s' '`+output+`'`, 5*time.Second)
			var stdout bytes.Buffer
			result, err := Execute(t.Context(), Options{
				Agent:         agent,
				Repository:    newGitRepository(t),
				DataDirectory: t.TempDir(),
				Stdout:        &stdout,
				Stderr:        io.Discard,
			})
			if err != nil {
				t.Fatal(err)
			}
			if stdout.String() != output {
				t.Fatalf("stdout = %q, want %q", stdout.String(), output)
			}
			if result.TokenUsage == nil || *result.TokenUsage != 123 {
				t.Fatalf("token usage = %v, want 123", result.TokenUsage)
			}
			if got := outputFor(t, readEvents(t, result.EventsPath), "stdout"); got != output {
				t.Fatalf("recorded stdout = %q, want %q", got, output)
			}
		})
	}
}

func TestExecuteIgnoresMalformedCodexUsageWithoutChangingFailure(t *testing.T) {
	agent := codexJSONAgent(t, `cat >/dev/null; printf 999 > "$MACHINIST_TOKEN_USAGE_PATH"; printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":"unknown","output_tokens":2}}'; exit 7`, 5*time.Second)
	result, err := Execute(t.Context(), Options{
		Agent:         agent,
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.State != StateFailed || outcome.ExitCode != 7 {
		t.Fatalf("error = %#v", err)
	}
	if result.TokenUsage != nil {
		t.Fatalf("token usage = %d, want unavailable", *result.TokenUsage)
	}
}

func codexJSONAgent(t *testing.T, script string, timeout time.Duration) config.ResolvedAgent {
	return codexJSONAgentCommand(t, "codex", "codex", false, script, timeout)
}

func codexJSONAgentCommand(t *testing.T, executor, executableName string, wrapped bool, script string, timeout time.Duration) config.ResolvedAgent {
	t.Helper()
	executable := filepath.Join(t.TempDir(), executableName)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := []string{executable, "exec", "--json"}
	if wrapped {
		command = append([]string{"/usr/bin/env"}, command...)
	}
	return config.ResolvedAgent{
		Name:     "build",
		Executor: executor,
		Command:  command,
		Prompt:   "complete prompt\n",
		Timeout:  timeout,
		Hash:     "codex-test-hash",
	}
}
