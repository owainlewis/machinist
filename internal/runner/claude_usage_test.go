package runner

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

func TestStructuredClaudeCommandAddsStreamJSONToPrintCommands(t *testing.T) {
	for _, test := range []struct {
		name     string
		executor string
		command  []string
		want     []string
	}{
		{name: "direct", executor: "claude", command: []string{"claude", "--print"}, want: []string{"claude", "--print", "--verbose", "--output-format", "stream-json"}},
		{name: "short print", executor: "claude", command: []string{"claude", "-p", "--dangerously-skip-permissions"}, want: []string{"claude", "-p", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions"}},
		{name: "renamed executable", executor: "claude-local", command: []string{"agent", "--print"}, want: []string{"agent", "--print", "--verbose", "--output-format", "stream-json"}},
		{name: "env wrapper", executor: "custom", command: []string{"env", "claude", "--print"}, want: []string{"env", "claude", "--print", "--verbose", "--output-format", "stream-json"}},
		{name: "mise wrapper", executor: "custom", command: []string{"mise", "exec", "--", "claude", "--print"}, want: []string{"mise", "exec", "--", "claude", "--print", "--verbose", "--output-format", "stream-json"}},
		{name: "nice wrapper", executor: "custom", command: []string{"nice", "claude", "--print"}, want: []string{"nice", "claude", "--print", "--verbose", "--output-format", "stream-json"}},
		{name: "nice long adjustment", executor: "custom", command: []string{"nice", "--adjustment=5", "claude", "--print"}, want: []string{"nice", "--adjustment=5", "claude", "--print", "--verbose", "--output-format", "stream-json"}},
		{name: "nice separate long adjustment", executor: "custom", command: []string{"nice", "--adjustment", "5", "claude", "--print"}, want: []string{"nice", "--adjustment", "5", "claude", "--print", "--verbose", "--output-format", "stream-json"}},
		{name: "max turns", executor: "claude", command: []string{"claude", "--print", "--max-turns", "3"}, want: []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--max-turns", "3"}},
		{name: "bare resume", executor: "claude", command: []string{"claude", "--resume", "--output-format=json", "--print"}, want: []string{"claude", "--resume", "--output-format=json", "--print"}},
		{name: "named resume", executor: "claude", command: []string{"claude", "--resume", "session-name", "--print"}, want: []string{"claude", "--resume", "session-name", "--print", "--verbose", "--output-format", "stream-json"}},
		{name: "variadic allowed tools", executor: "claude", command: []string{"claude", "--print", "--allowedTools", "Bash", "Edit"}, want: []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--allowedTools", "Bash", "Edit"}},
		{name: "variadic add dirs", executor: "claude", command: []string{"claude", "--add-dir", "../apps", "../lib", "--print"}, want: []string{"claude", "--add-dir", "../apps", "../lib", "--print", "--verbose", "--output-format", "stream-json"}},
		{name: "agent", executor: "claude", command: []string{"claude", "--print", "--agent", "reviewer"}, want: []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--agent", "reviewer"}},
		{name: "strict MCP config", executor: "claude", command: []string{"claude", "--print", "--strict-mcp-config"}, want: []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--strict-mcp-config"}},
		{name: "prompt suggestions flag", executor: "claude", command: []string{"claude", "--print", "--prompt-suggestions"}, want: []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--prompt-suggestions"}},
		{name: "prompt suggestions separate value", executor: "claude", command: []string{"claude", "--print", "--prompt-suggestions", "false"}, want: []string{"claude", "--print", "--prompt-suggestions", "false"}},
		{name: "prompt suggestions equals value", executor: "claude", command: []string{"claude", "--print", "--prompt-suggestions=false"}, want: []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--prompt-suggestions=false"}},
		{name: "existing verbose", executor: "claude", command: []string{"claude", "--print", "--verbose"}, want: []string{"claude", "--print", "--output-format", "stream-json", "--verbose"}},
		{name: "explicit text", executor: "claude", command: []string{"claude", "--print", "--output-format", "text"}, want: []string{"claude", "--print", "--output-format", "text"}},
		{name: "explicit json", executor: "claude", command: []string{"claude", "--output-format=json", "--print"}, want: []string{"claude", "--output-format=json", "--print"}},
		{name: "explicit stream json", executor: "claude", command: []string{"claude", "--print", "--output-format", "stream-json"}, want: []string{"claude", "--print", "--output-format", "stream-json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := slices.Clone(test.command)
			if got := structuredClaudeCommand(test.executor, test.command); !slices.Equal(got, test.want) {
				t.Fatalf("command = %q, want %q", got, test.want)
			}
			if !slices.Equal(test.command, original) {
				t.Fatalf("input command mutated: %q", test.command)
			}
		})
	}
}

func TestClaudeDebugFlagPreservesExplicitOutputFormat(t *testing.T) {
	command := []string{"claude", "--debug", "--output-format=json", "--print"}
	if got := structuredClaudeCommand("claude", command); !slices.Equal(got, command) {
		t.Fatalf("command = %q, want unchanged", got)
	}
	if got := newUsageCollector("claude", command); got == nil {
		t.Fatal("collector = nil, want enabled for explicit JSON output")
	}
}

func TestClaudeDebugFilterSeparateValueIsRejected(t *testing.T) {
	command := []string{"claude", "--debug", "api,hooks", "--print"}
	if got := structuredClaudeCommand("claude", command); !slices.Equal(got, command) {
		t.Fatalf("command = %q, want unchanged", got)
	}
	if got := newUsageCollector("claude", command); got != nil {
		t.Fatalf("collector = %#v, want disabled", got)
	}
}

func TestClaudeDebugFilterEqualsValueIsRecognized(t *testing.T) {
	command := []string{"claude", "--debug=api,hooks", "--print"}
	want := []string{"claude", "--debug=api,hooks", "--print", "--verbose", "--output-format", "stream-json"}
	if got := structuredClaudeCommand("claude", command); !slices.Equal(got, want) {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestClaudeUsageCollectorAcceptsCurrentPrintOptions(t *testing.T) {
	for _, command := range [][]string{
		{"claude", "--print", "--agent", "reviewer"},
		{"claude", "--print", "--strict-mcp-config"},
		{"claude", "--print", "--prompt-suggestions=false"},
	} {
		if got := newUsageCollector("claude", command); got == nil {
			t.Fatalf("collector disabled for command %q", command)
		}
	}
}

func TestClaudeCommandRecognitionRejectsTrailingNiceWrappers(t *testing.T) {
	for _, command := range [][]string{
		{"env", "nice"},
		{"mise", "exec", "--", "nice"},
		{"direnv", "exec", ".", "nice"},
		{"nice", "--adjustment"},
	} {
		if got := structuredClaudeCommand("claude", command); !slices.Equal(got, command) {
			t.Fatalf("command = %q, want unchanged", got)
		}
		if got := newUsageCollector("claude", command); got != nil {
			t.Fatalf("collector = %#v, want disabled", got)
		}
	}
}

func TestClaudeCommandRecognitionRejectsAmbiguousArguments(t *testing.T) {
	for _, test := range []struct {
		name     string
		executor string
		command  []string
	}{
		{name: "missing print", executor: "claude", command: []string{"claude", "--verbose"}},
		{name: "prompt argument", executor: "claude", command: []string{"claude", "--print", "prompt"}},
		{name: "short version option is not verbose", executor: "claude", command: []string{"claude", "--print", "-v"}},
		{name: "unknown option", executor: "claude", command: []string{"claude", "--future-option", "--print"}},
		{name: "missing option value", executor: "claude", command: []string{"claude", "--print", "--model"}},
		{name: "missing variadic option value", executor: "claude", command: []string{"claude", "--print", "--allowedTools"}},
		{name: "prompt suggestions optional value", executor: "claude", command: []string{"claude", "--print", "--prompt-suggestions", "false"}},
		{name: "invalid output format", executor: "claude", command: []string{"claude", "--print", "--output-format", "yaml"}},
		{name: "misleading data", executor: "custom", command: []string{"echo", "claude", "--print"}},
		{name: "misleading data with Claude executor", executor: "claude", command: []string{"echo", "claude", "--print"}},
		{name: "malformed env wrapper", executor: "custom", command: []string{"env", "--split-string=claude --print"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := structuredClaudeCommand(test.executor, test.command); !slices.Equal(got, test.command) {
				t.Fatalf("command = %q, want unchanged", got)
			}
			if got := newUsageCollector(test.executor, test.command); got != nil {
				t.Fatalf("collector = %#v, want disabled", got)
			}
		})
	}
}

func TestClaudeUsageCollectorReadsAllUsageFields(t *testing.T) {
	collector := newUsageCollector("claude", []string{"claude", "--print"})
	output := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"result","usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":4}}`
	if _, err := collector.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	if got := collector.tokenUsage(); got == nil || *got != 154 {
		t.Fatalf("token usage = %v, want 154", got)
	}
}

func TestClaudeUsageCollectorRejectsInvalidFinalUsage(t *testing.T) {
	for _, test := range []struct {
		name string
		line string
	}{
		{name: "missing field", line: `{"type":"result","usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}`},
		{name: "malformed", line: `{"type":"result","usage":`},
		{name: "negative", line: `{"type":"result","usage":{"input_tokens":-1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}}`},
		{name: "fractional", line: `{"type":"result","usage":{"input_tokens":1.5,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}}`},
		{name: "overflow", line: `{"type":"result","usage":{"input_tokens":9223372036854775807,"cache_creation_input_tokens":1,"cache_read_input_tokens":0,"output_tokens":0}}`},
		{name: "malformed field before type", line: `{"usage":invalid,"type":"result"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector := newUsageCollector("claude", []string{"claude", "--print"})
			_, _ = collector.Write([]byte(`{"type":"result","usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}}` + "\n" + test.line + "\n"))
			if got := collector.tokenUsage(); got != nil {
				t.Fatalf("token usage = %d, want unavailable", *got)
			}
		})
	}
}

func TestExecuteCollectsClaudeUsageAndPreservesOutput(t *testing.T) {
	const output = `{"type":"result","usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":4}}` + "\n"
	claude := claudeAgent(t, `cat >/dev/null; if [ "$2" != "--verbose" ] || [ "$3" != "--output-format" ] || [ "$4" != "stream-json" ]; then exit 8; fi; printf '%s' '`+output+`'; printf 'claude stderr\n' >&2`, 5*time.Second)
	var stdout, stderr bytes.Buffer
	result, err := Execute(t.Context(), Options{
		Command: claude, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TokenUsage == nil || *result.TokenUsage != 154 {
		t.Fatalf("token usage = %v, want 154", result.TokenUsage)
	}
	if stdout.String() != output || stderr.String() != "claude stderr\n" {
		t.Fatalf("live output = stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	if got := outputFor(t, readEvents(t, result.EventsPath), "stdout"); got != output {
		t.Fatalf("recorded stdout = %q, want %q", got, output)
	}
}

func TestExecuteUsesClaudeFileFallbackForExplicitText(t *testing.T) {
	claude := claudeAgentWithCommand(t, []string{"--print", "--output-format", "text"}, `cat >/dev/null; if [ "$1" != "--print" ] || [ "$2" != "--output-format" ] || [ "$3" != "text" ] || [ -n "$4" ]; then exit 8; fi; printf 'text output'; printf 2468 > "$MACHINIST_TOKEN_USAGE_PATH"`, 5*time.Second)
	result, err := Execute(t.Context(), Options{
		Command: claude, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TokenUsage == nil || *result.TokenUsage != 2468 {
		t.Fatalf("token usage = %v, want 2468", result.TokenUsage)
	}
}

func TestExecuteUsesClaudeFileFallbackForExplicitTextAfterTimeout(t *testing.T) {
	claude := claudeAgentWithCommand(t, []string{"--print", "--output-format", "text"}, `cat >/dev/null; printf 2468 > "$MACHINIST_TOKEN_USAGE_PATH"; sleep 60`, time.Second)
	result, err := Execute(t.Context(), Options{
		Command: claude, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.State != StateTimedOut || outcome.ExitCode != 124 {
		t.Fatalf("error = %#v", err)
	}
	if result.TokenUsage == nil || *result.TokenUsage != 2468 {
		t.Fatalf("token usage = %v, want 2468 after timeout", result.TokenUsage)
	}
}

func TestExecuteCollectsClaudeExplicitJSONUsageWithoutNormalizing(t *testing.T) {
	const output = `{"type":"result","usage":{"input_tokens":5,"cache_creation_input_tokens":6,"cache_read_input_tokens":7,"output_tokens":8}}`
	claude := claudeAgentWithCommand(t, []string{"--print", "--output-format", "json"}, `cat >/dev/null; if [ "$1" != "--print" ] || [ "$2" != "--output-format" ] || [ "$3" != "json" ] || [ -n "$4" ]; then exit 8; fi; printf '%s' '`+output+`'`, 5*time.Second)
	result, err := Execute(t.Context(), Options{
		Command: claude, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TokenUsage == nil || *result.TokenUsage != 26 {
		t.Fatalf("token usage = %v, want 26", result.TokenUsage)
	}
}

func TestExecutePreservesClaudeFailureAndTimeoutWithUsage(t *testing.T) {
	const output = `{"type":"result","usage":{"input_tokens":1,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}}` + "\n"
	for _, test := range []struct {
		name    string
		script  string
		timeout time.Duration
		state   State
		exit    int
	}{
		{name: "failed", script: `cat >/dev/null; printf '%s' '` + output + `'; exit 7`, timeout: 5 * time.Second, state: StateFailed, exit: 7},
		{name: "timed out", script: `cat >/dev/null; printf '%s' '` + output + `'; sleep 60`, timeout: time.Second, state: StateTimedOut, exit: 124},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := Execute(t.Context(), Options{
				Command: claudeAgent(t, test.script, test.timeout), Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
			})
			var outcome *OutcomeError
			if !errors.As(err, &outcome) || outcome.State != test.state || outcome.ExitCode != test.exit {
				t.Fatalf("error = %#v", err)
			}
			if result.TokenUsage == nil || *result.TokenUsage != 10 {
				t.Fatalf("token usage = %v, want 10", result.TokenUsage)
			}
		})
	}
}

func TestExecuteInvalidClaudeUsagePreservesFailure(t *testing.T) {
	claude := claudeAgent(t, `cat >/dev/null; printf '%s' '{"type":"result","usage":{"input_tokens":1.5,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":4}}'; exit 7`, 5*time.Second)
	result, err := Execute(t.Context(), Options{
		Command: claude, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	var outcome *OutcomeError
	if !errors.As(err, &outcome) || outcome.State != StateFailed || outcome.ExitCode != 7 {
		t.Fatalf("error = %#v", err)
	}
	if result.TokenUsage != nil {
		t.Fatalf("token usage = %d, want unavailable", *result.TokenUsage)
	}
}

func TestExecuteLeavesClaudeUsageUnavailableWhenResultHasNoUsage(t *testing.T) {
	claude := claudeAgent(t, `cat >/dev/null; printf '%s' '{"type":"result","subtype":"success"}'`, 5*time.Second)
	result, err := Execute(t.Context(), Options{
		Command: claude, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TokenUsage != nil {
		t.Fatalf("token usage = %d, want unavailable", *result.TokenUsage)
	}
}

func claudeAgent(t *testing.T, script string, timeout time.Duration) config.ResolvedCommand {
	return claudeAgentWithCommand(t, []string{"--print"}, script, timeout)
}

func claudeAgentWithCommand(t *testing.T, arguments []string, script string, timeout time.Duration) config.ResolvedCommand {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return config.ResolvedCommand{
		Name: "build", Executor: "claude", Command: append([]string{executable}, arguments...), Prompt: "complete prompt\n", Timeout: timeout, Hash: "claude-test-hash",
	}
}
