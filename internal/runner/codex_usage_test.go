package runner

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

func TestCodexUsageCollectorReadsFinalStructuredUsage(t *testing.T) {
	collector := newUsageCollector("codex", []string{"codex", "exec", "--json", "-"})
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
			collector := newUsageCollector("codex", []string{"codex", "exec", "--json"})
			_, _ = collector.Write([]byte(test.line + "\n"))
			if got := collector.tokenUsage(); got != nil {
				t.Fatalf("token usage = %d, want unavailable", *got)
			}
		})
	}
}

func TestCodexUsageCollectorUsesTheLastCompletedTurn(t *testing.T) {
	collector := newUsageCollector("codex", []string{"codex", "exec", "--json"})
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":5}}` + "\n"))
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":"invalid","output_tokens":5}}` + "\n"))
	if got := collector.tokenUsage(); got != nil {
		t.Fatalf("token usage = %d, want unavailable for malformed final event", *got)
	}
}

func TestCodexUsageCollectorInvalidatesUsageForTruncatedFinalCompletedTurn(t *testing.T) {
	collector := newUsageCollector("codex", []string{"codex", "exec", "--json"})
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":5}}` + "\n"))
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":8`))
	if got := collector.tokenUsage(); got != nil {
		t.Fatalf("token usage = %d, want unavailable for truncated final event", *got)
	}
}

func TestCodexUsageCollectorInvalidatesUsageForOversizedFinalCompletedTurn(t *testing.T) {
	collector := newUsageCollector("codex", []string{"codex", "exec", "--json"})
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":5}}` + "\n"))
	oversized := `{"type":"turn.completed","usage":{"input_tokens":8,"output_tokens":` + strings.Repeat("1", maxStructuredEventBytes) + `}}` + "\n"
	_, _ = collector.Write([]byte(oversized))
	if got := collector.tokenUsage(); got != nil {
		t.Fatalf("token usage = %d, want unavailable for oversized final event", *got)
	}
}

func TestCodexUsageCollectorIgnoresUnrelatedMalformedOutput(t *testing.T) {
	collector := newUsageCollector("codex", []string{"codex", "exec", "--json"})
	_, _ = collector.Write([]byte(`{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":5}}` + "\n"))
	_, _ = collector.Write([]byte(`{"type":"item.completed","item":`))
	if got := collector.tokenUsage(); got == nil || *got != 9 {
		t.Fatalf("token usage = %v, want 9", got)
	}
}

func TestStructuredUsageCollectorIgnoresNestedMalformedTerminalEvents(t *testing.T) {
	for _, resultType := range []string{"result", "turn.completed"} {
		t.Run(resultType, func(t *testing.T) {
			collector := &structuredUsageCollector{resultType: resultType, cache: resultType == "result"}
			valid := `{"type":"` + resultType + `","usage":{"input_tokens":4,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5}}`
			if resultType == "turn.completed" {
				valid = `{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":5}}`
			}
			_, _ = collector.Write([]byte(valid + "\n"))
			_, _ = collector.Write([]byte(`{"item":{"type":"` + resultType + `","usage":`))
			if got := collector.tokenUsage(); got == nil || *got != 9 {
				t.Fatalf("token usage = %v, want 9", got)
			}
		})
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
		{name: "wrapped renamed executable", executor: "codex-local", command: []string{"/usr/bin/env", "agent", "exec", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if collector := newUsageCollector(test.executor, test.command); collector == nil {
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
		if collector := newUsageCollector(test.executor, test.command); collector != nil {
			t.Fatalf("collector enabled for executor %q command %q", test.executor, test.command)
		}
	}
}

func TestStructuredCodexCommandAddsJSONToRecognizedLegacyCommands(t *testing.T) {
	for _, test := range []struct {
		name     string
		executor string
		command  []string
		want     []string
	}{
		{name: "direct", executor: "codex", command: []string{"codex", "exec", "-"}, want: []string{"codex", "exec", "--json", "-"}},
		{name: "root option value matches subcommand", executor: "codex", command: []string{"codex", "--profile", "exec", "exec", "-"}, want: []string{"codex", "--profile", "exec", "exec", "--json", "-"}},
		{name: "custom executor name", executor: "codex-local", command: []string{"agent", "exec", "-"}, want: []string{"agent", "exec", "--json", "-"}},
		{name: "wrapped", executor: "custom", command: []string{"/usr/bin/env", "codex", "exec", "-"}, want: []string{"/usr/bin/env", "codex", "exec", "--json", "-"}},
		{name: "wrapped renamed executable", executor: "codex-local", command: []string{"/usr/bin/env", "agent", "exec", "-"}, want: []string{"/usr/bin/env", "agent", "exec", "--json", "-"}},
		{name: "wrapped renamed executable after diagnostic option", executor: "codex-local", command: []string{"/usr/bin/env", "-v", "agent", "exec", "-"}, want: []string{"/usr/bin/env", "-v", "agent", "exec", "--json", "-"}},
		{name: "wrapped renamed executable after compact options", executor: "codex-local", command: []string{"/usr/bin/env", "-iv", "agent", "exec", "-"}, want: []string{"/usr/bin/env", "-iv", "agent", "exec", "--json", "-"}},
		{name: "wrapped renamed executable after compact unset", executor: "codex-local", command: []string{"/usr/bin/env", "-iuMISSING", "agent", "exec", "-"}, want: []string{"/usr/bin/env", "-iuMISSING", "agent", "exec", "--json", "-"}},
		{name: "wrapped renamed executable after argv zero", executor: "codex-local", command: []string{"/usr/bin/env", "--argv0=codex", "agent", "exec", "-"}, want: []string{"/usr/bin/env", "--argv0=codex", "agent", "exec", "--json", "-"}},
		{name: "wrapped renamed executable after short argv zero", executor: "codex-local", command: []string{"/usr/bin/env", "-a", "codex", "agent", "exec", "-"}, want: []string{"/usr/bin/env", "-a", "codex", "agent", "exec", "--json", "-"}},
		{name: "wrapped renamed executable after empty environment alias", executor: "codex-local", command: []string{"/usr/bin/env", "-", "agent", "exec", "-"}, want: []string{"/usr/bin/env", "-", "agent", "exec", "--json", "-"}},
		{name: "wrapper has its own exec", executor: "custom", command: []string{"mise", "exec", "--", "codex", "exec", "-"}, want: []string{"mise", "exec", "--", "codex", "exec", "--json", "-"}},
		{name: "wrapper has global options", executor: "custom", command: []string{"mise", "-q", "exec", "--", "codex", "exec", "-"}, want: []string{"mise", "-q", "exec", "--", "codex", "exec", "--json", "-"}},
		{name: "wrapper has compact global options", executor: "custom", command: []string{"mise", "-qC/tmp", "exec", "--", "codex", "exec", "-"}, want: []string{"mise", "-qC/tmp", "exec", "--", "codex", "exec", "--json", "-"}},
		{name: "nested wrappers", executor: "codex-local", command: []string{"env", "mise", "exec", "--", "codex", "exec", "-"}, want: []string{"env", "mise", "exec", "--", "codex", "exec", "--json", "-"}},
		{name: "reverse nested wrappers", executor: "codex-local", command: []string{"mise", "exec", "--", "env", "agent", "exec", "-"}, want: []string{"mise", "exec", "--", "env", "agent", "exec", "--json", "-"}},
		{name: "direnv wrapper", executor: "codex-local", command: []string{"direnv", "exec", ".", "codex", "exec", "-"}, want: []string{"direnv", "exec", ".", "codex", "exec", "--json", "-"}},
		{name: "direnv wrapper with renamed executable", executor: "codex-local", command: []string{"direnv", "exec", ".", "agent", "exec", "-"}, want: []string{"direnv", "exec", ".", "agent", "exec", "--json", "-"}},
		{name: "nice wrapper", executor: "codex-local", command: []string{"nice", "codex", "exec", "-"}, want: []string{"nice", "codex", "exec", "--json", "-"}},
		{name: "automatic review root option", executor: "codex", command: []string{"codex", "--approve-for-me", "exec", "-"}, want: []string{"codex", "--approve-for-me", "exec", "--json", "-"}},
		{name: "legacy automatic review root option", executor: "codex", command: []string{"codex", "--not-so-yolo", "exec", "-"}, want: []string{"codex", "--not-so-yolo", "exec", "--json", "-"}},
		{name: "already structured", executor: "codex", command: []string{"codex", "exec", "--json", "-"}, want: []string{"codex", "exec", "--json", "-"}},
		{name: "other executor", executor: "claude", command: []string{"claude", "exec", "-"}, want: []string{"claude", "exec", "-"}},
		{name: "Codex words are data", executor: "custom", command: []string{"echo", "codex", "exec"}, want: []string{"echo", "codex", "exec"}},
		{name: "Codex words are env split string data", executor: "custom", command: []string{"env", "-iSecho", "codex", "exec", "-"}, want: []string{"env", "-iSecho", "codex", "exec", "-"}},
		{name: "Codex words are long env split string data", executor: "custom", command: []string{"env", "--split-string=echo", "codex", "exec", "-"}, want: []string{"env", "--split-string=echo", "codex", "exec", "-"}},
		{name: "Codex words are mise task arguments", executor: "custom", command: []string{"mise", "run", "build", "--", "codex", "exec"}, want: []string{"mise", "run", "build", "--", "codex", "exec"}},
		{name: "other codex command", executor: "codex", command: []string{"codex", "serve"}, want: []string{"codex", "serve"}},
		{name: "exec argument to another Codex command", executor: "codex", command: []string{"codex", "review", "exec"}, want: []string{"codex", "review", "exec"}},
		{name: "unknown Codex root option", executor: "codex", command: []string{"codex", "--future-option", "exec", "-"}, want: []string{"codex", "--future-option", "exec", "-"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := slices.Clone(test.command)
			if got := structuredCodexCommand(test.executor, test.command); !slices.Equal(got, test.want) {
				t.Fatalf("command = %q, want %q", got, test.want)
			}
			if !slices.Equal(test.command, original) {
				t.Fatalf("input command mutated: %q", test.command)
			}
		})
	}
}

func TestExecuteEnablesStructuredUsageForLegacyCodexCommand(t *testing.T) {
	const output = "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":23}}\n"
	executable := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\ncat >/dev/null\nif [ \"$2\" != \"--json\" ]; then exit 8; fi\nprintf '%s' '" + output + "'\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	result, err := Execute(t.Context(), Options{
		Command: config.ResolvedCommand{
			Name: "build", Executor: "codex", Command: []string{executable, "exec", "-"}, Prompt: "complete prompt\n", Timeout: 5 * time.Second, Hash: "legacy-codex-test-hash",
		},
		Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: &stdout, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TokenUsage == nil || *result.TokenUsage != 123 {
		t.Fatalf("token usage = %v, want 123", result.TokenUsage)
	}
	if stdout.String() != output {
		t.Fatalf("stdout = %q, want %q", stdout.String(), output)
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
		{name: "wrapped renamed", executor: "codex-local", executableName: "agent", wrapped: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := codexJSONAgentCommand(t, test.executor, test.executableName, test.wrapped, `cat >/dev/null; printf 999 > "$MACHINIST_TOKEN_USAGE_PATH"; printf '%s' '`+output+`'`, 5*time.Second)
			var stdout bytes.Buffer
			result, err := Execute(t.Context(), Options{
				Command:       agent,
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
		Command:       agent,
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

func codexJSONAgent(t *testing.T, script string, timeout time.Duration) config.ResolvedCommand {
	return codexJSONAgentCommand(t, "codex", "codex", false, script, timeout)
}

func codexJSONAgentCommand(t *testing.T, executor, executableName string, wrapped bool, script string, timeout time.Duration) config.ResolvedCommand {
	t.Helper()
	executable := filepath.Join(t.TempDir(), executableName)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := []string{executable, "exec", "--json"}
	if wrapped {
		command = append([]string{"/usr/bin/env"}, command...)
	}
	return config.ResolvedCommand{
		Name:     "build",
		Executor: executor,
		Command:  command,
		Prompt:   "complete prompt\n",
		Timeout:  timeout,
		Hash:     "codex-test-hash",
	}
}
