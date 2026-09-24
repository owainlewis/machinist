package runner

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestCollectorKeepsTheLastCodexAgentMessage(t *testing.T) {
	collector := newUsageCollector("codex", []string{"codex", "exec", "--json", "-"})
	_, _ = collector.Write([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"Looking at issues."}}` + "\n" +
		`{"type":"item.completed","item":{"type":"command_execution","command":"gh issue list"}}` + "\n" +
		`{"type":"item.completed","item":{"type":"agent_message","text":"  Labelled #496 as bug.  "}}`))
	if got := collector.lastMessage(); got != "Labelled #496 as bug." {
		t.Fatalf("last message = %q", got)
	}
}

func TestCollectorKeepsTheClaudeResult(t *testing.T) {
	collector := newUsageCollector("claude", []string{"claude", "--print", "--output-format", "stream-json"})
	_, _ = collector.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Working."}]}}` + "\n" +
		`{"type":"result","result":"All done.","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}` + "\n"))
	if got := collector.lastMessage(); got != "All done." {
		t.Fatalf("last message = %q", got)
	}
}

func TestCollectorTruncatesLongMessagesOnARuneBoundary(t *testing.T) {
	collector := newUsageCollector("codex", []string{"codex", "exec", "--json"})
	text := strings.Repeat("é", maxFinalMessageBytes)
	_, _ = collector.Write([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"` + text + `"}}` + "\n"))
	got := collector.lastMessage()
	if !strings.HasSuffix(got, "[truncated]") || len(got) > maxFinalMessageBytes+len("\n\n[truncated]") || !strings.HasPrefix(got, "éé") {
		t.Fatalf("truncated message has length %d and suffix %q", len(got), got[len(got)-20:])
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("truncation split a rune")
	}
}

func TestExecuteRecordsTheFinalAgentMessage(t *testing.T) {
	const output = `{"type":"item.completed","item":{"type":"agent_message","text":"Triaged 9 issues."}}` + "\n"
	agent := codexJSONAgentCommand(t, "codex", "codex", false, `cat >/dev/null; printf '%s' '`+output+`'`, 5*time.Second)
	result, err := Execute(t.Context(), Options{
		Command:       agent,
		Repository:    newGitRepository(t),
		DataDirectory: t.TempDir(),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalMessage != "Triaged 9 issues." {
		t.Fatalf("final message = %q", result.FinalMessage)
	}
}
