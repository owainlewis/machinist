package harness

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestParseAgentReadsFrontMatterAndPrompt(t *testing.T) {
	agent, err := ParseAgent("review", "---\r\nharness: codex\r\nmodel: gpt-5\r\ncommand: /opt/bin/codex\r\n---\r\nReview the branch.\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if agent != (Agent{Name: "review", Harness: "codex", Model: "gpt-5", Program: "/opt/bin/codex", Prompt: "Review the branch."}) {
		t.Fatalf("agent = %+v", agent)
	}
	args, stdin := agent.Command("Rules.", "Fix it.")
	if args[0] != "/opt/bin/codex" || !slices.Contains(args, "gpt-5") || stdin != "Rules.\n\n# The task\n\nFix it." {
		t.Fatalf("args = %q, stdin = %q", args, stdin)
	}
}

func TestParseAgentRejectsBadFiles(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"no front matter", "Review.\n", "must start with ---"},
		{"unclosed", "---\nharness: claude\nReview.\n", "not closed"},
		{"no harness", "---\nmodel: x\n---\nReview.\n", "harness must be one of agy, claude, codex, pi"},
		{"unknown harness", "---\nharness: gemini\n---\nReview.\n", `not "gemini"`},
		{"unknown key", "---\nharness: claude\nsteps: 3\n---\nReview.\n", "steps"},
		{"empty prompt", "---\nharness: claude\n---\n\n", "prompt after the front matter is empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseAgent("a", test.body); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEachHarnessBuildsItsCommand(t *testing.T) {
	request := Request{System: "Rules.", Prompt: "Fix it.", Model: "m"}
	for _, test := range []struct {
		harness Harness
		args    []string
		stdin   string
	}{
		{Claude{}, []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions", "--append-system-prompt", "Rules.", "--model", "m"}, "Fix it."},
		{Codex{}, []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--model", "m", "-"}, "Rules.\n\n# The task\n\nFix it."},
		{Pi{}, []string{"pi", "--print", "--mode", "json", "--no-session", "--append-system-prompt", "Rules.", "--model", "m", "Fix it."}, ""},
		{Agy{}, []string{"agy", "--print", "Rules.\n\n# The task\n\nFix it.", "--output-format", "stream-json", "--dangerously-skip-permissions", "--model", "m"}, ""},
	} {
		args, stdin := test.harness.Command(request)
		if !slices.Equal(args, test.args) || stdin != test.stdin {
			t.Errorf("%s: args = %q, stdin = %q", test.harness.Name(), args, stdin)
		}
		if found, ok := Find(test.harness.Name()); !ok || found.Name() != test.harness.Name() {
			t.Errorf("Find(%q) = %v, %v", test.harness.Name(), found, ok)
		}
	}
	if args, _ := (Claude{}).Command(Request{System: "Rules."}); slices.Contains(args, "--model") {
		t.Fatalf("an empty model still passed --model: %q", args)
	}
}

const codexStream = `{"type":"thread.started","thread_id":"t1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"Looking at the retry.\n"}}
{"type":"item.started","item":{"id":"i1","type":"command_execution","command":"go test ./...","status":"in_progress"}}
{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"go test ./...","exit_code":1,"status":"failed"}}
{"type":"item.completed","item":{"id":"i2","type":"file_change","changes":[{"path":"/work/upload.go","kind":"update"}],"status":"completed"}}
{"type":"item.completed","item":{"id":"i3","type":"mcp_tool_call","server":"linear","tool":"get_issue","status":"completed"}}
not json at all
{"type":"turn.completed","usage":{"input_tokens":120,"cached_input_tokens":100,"output_tokens":30}}
`

func TestCodexFormatTurnsEventsIntoLines(t *testing.T) {
	var out bytes.Buffer
	usage, err := Codex{}.Format(strings.NewReader(codexStream), &out, "/work")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Codex started",
		"Looking at the retry.",
		ActionPrefix + "Run go test ./...",
		"  failed with status 1",
		ActionPrefix + "Edit upload.go",
		ActionPrefix + "Use linear get_issue",
		"not json at all",
		"Finished turn 1",
	}, "\n") + "\n"
	if out.String() != want {
		t.Fatalf("output =\n%s\nwant\n%s", out.String(), want)
	}
	if usage == nil || usage.Tokens != 150 || usage.Cost != 0 {
		t.Fatalf("usage = %+v", usage)
	}
}

const piStream = `{"type":"agent_start"}
{"type":"turn_start"}
{"type":"tool_execution_start","toolCallId":"c1","toolName":"read","args":{"path":"/work/upload.go"}}
{"type":"tool_execution_end","toolCallId":"c1","toolName":"read","isError":false}
{"type":"tool_execution_start","toolCallId":"c2","toolName":"bash","args":{"command":"go test ./...\necho done"}}
{"type":"tool_execution_end","toolCallId":"c2","toolName":"bash","isError":true}
{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"ignored"}]}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Fixed the retry."}],"usage":{"input":100,"output":20,"cacheRead":5,"cacheWrite":5,"cost":{"total":0.25}},"stopReason":"stop"}}
{"type":"turn_end"}
{"type":"agent_end"}
`

func TestPiFormatTurnsEventsIntoLines(t *testing.T) {
	var out bytes.Buffer
	usage, err := Pi{}.Format(strings.NewReader(piStream), &out, "/work")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Pi started",
		ActionPrefix + "Read upload.go",
		ActionPrefix + "Run go test ./...",
		"  failed: bash",
		"Fixed the retry.",
		"Finished after 1 turns",
	}, "\n") + "\n"
	if out.String() != want {
		t.Fatalf("output =\n%s\nwant\n%s", out.String(), want)
	}
	if usage == nil || usage.Tokens != 130 || usage.Cost != 0.25 {
		t.Fatalf("usage = %+v", usage)
	}
}

// agyStream is a real agy 1.5.14 run with gemini-4-neon-high, with the init
// tool list cut short, a failing command and an edit added.
const agyStream = `{"event":"init","conversation_id":"c1","init":{"model":"gemini-4-neon-high","cwd":"/work","tools":["run_command","view_file"],"permission_mode":"always-proceed"}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":0,"state":"DONE","step_type":"user_input"}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"Looking at the retry.","usage":{"input_tokens":8636,"output_tokens":73,"total_tokens":8709}}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go test ./...\necho done"}}}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":2,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go test ./..."},"output":"Created At: 2026-11-21T22:47:16Z\nCommand exited with code 1.\n\nCommand output:\nFAIL\r\n"}}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":3,"state":"ACTIVE","step_type":"tool","tool_name":"replace_file_content","tool_info":{"name":"replace_file_content","parameters":{"TargetFile":"/work/upload.go"}}}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":3,"state":"DONE","step_type":"tool","tool_name":"replace_file_content","tool_info":{"name":"replace_file_content","output":"ok"}}}
not json at all
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":4,"state":"ACTIVE","step_type":"agent_response","text_delta":"done"}}
{"event":"step_update","step_update":{"conversation_id":"c1","step_index":4,"state":"DONE","step_type":"agent_response","text_delta":"\n","usage":{"input_tokens":2214,"output_tokens":10,"cache_read_tokens":6579,"total_tokens":2224}}}
{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"done\n","num_turns":1,"usage":{"input_tokens":10850,"output_tokens":83,"thinking_tokens":42,"cache_read_tokens":6579,"total_tokens":10933}}}
`

func TestAgyFormatTurnsEventsIntoLines(t *testing.T) {
	var out bytes.Buffer
	usage, err := Agy{}.Format(strings.NewReader(agyStream), &out, "/work")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Antigravity started with gemini-4-neon-high",
		"Looking at the retry.",
		ActionPrefix + "Run go test ./...",
		"  failed with status 1",
		ActionPrefix + "Edit upload.go",
		"not json at all",
		"done",
		"Finished after 1 turns",
	}, "\n") + "\n"
	if out.String() != want {
		t.Fatalf("output =\n%s\nwant\n%s", out.String(), want)
	}
	if usage == nil || usage.Tokens != 10933+6579 || usage.Cost != 0 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestAgyFormatReportsAFailedRun(t *testing.T) {
	var out bytes.Buffer
	stream := `{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"agent_response","text_delta":"Half an"}}
a warning line
{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"agent_response","text_delta":" answer"}}
{"event":"result","result":{"status":"ERROR","error":{"message":"authentication required"},"usage":{"total_tokens":10}}}
`
	usage, err := Agy{}.Format(strings.NewReader(stream), &out, "/work")
	if err != nil {
		t.Fatal(err)
	}
	// Text cut off by a failure is kept, and an unknown line between its
	// pieces does not lose it.
	if want := "a warning line\nStopped: authentication required\nHalf an answer\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
	if usage == nil || usage.Tokens != 10 {
		t.Fatalf("usage = %+v", usage)
	}
}

// A step that runs two agents lists both, in order, and adds their usage.
func TestRecordAgentListsEveryAgentInAStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), UsageFile)
	RecordAgent(path, protocol.Agent{Name: "revise", Harness: "claude"})
	addUsage(path, Usage{Tokens: 10, Cost: 0.5})
	RecordAgent(path, protocol.Agent{Name: "review", Harness: "agy", Model: "gemini-4-neon-high"})
	addUsage(path, Usage{Tokens: 5})
	usage := readUsage(path)
	want := []protocol.Agent{{Name: "revise", Harness: "claude"}, {Name: "review", Harness: "agy", Model: "gemini-4-neon-high"}}
	if !slices.Equal(usage.Agents, want) || usage.Tokens != 15 || usage.Cost != 0.5 {
		t.Fatalf("usage = %+v", usage)
	}
}
