package agentlog

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stream = `{"type":"system","subtype":"init","model":"claude-sonnet","cwd":"/work"}
{"type":"assistant","message":{"content":[{"type":"text","text":"I'll look at the upload retry first.\n"},{"type":"tool_use","name":"Read","input":{"file_path":"/work/src/upload.ts"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"export function upload() {}","is_error":false}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"npm test -- upload\nnpm run lint"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"FAIL src/upload.test.ts\nmore"}],"is_error":true}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/work/src/upload.ts"}},{"type":"tool_use","name":"Grep","input":{"pattern":"retry"}},{"type":"tool_use","name":"mcp__linear__get_issue","input":{}}]}}
plain output from a hook
{"type":"result","subtype":"success","is_error":false,"num_turns":7,"duration_ms":192400,"total_cost_usd":0.4213,"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":20,"cache_read_input_tokens":1000}}
`

func TestFormatTurnsStreamJSONIntoLines(t *testing.T) {
	var out bytes.Buffer
	usage, err := Format(strings.NewReader(stream), &out, "/work")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Claude Code started with claude-sonnet",
		"I'll look at the upload retry first.",
		"▸ Read src/upload.ts",
		"▸ Run npm test -- upload",
		"  failed: FAIL src/upload.test.ts",
		"▸ Edit src/upload.ts",
		"▸ Search for retry",
		"▸ Use mcp__linear__get_issue",
		"plain output from a hook",
		"Finished after 7 turns in 3m12s, $0.42",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
	if usage == nil || usage.Tokens != 1170 || usage.Cost != 0.4213 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestFormatPassesOtherOutputThrough(t *testing.T) {
	var out bytes.Buffer
	usage, err := Format(strings.NewReader("hello\n{\"not\":\"an event\"}\n{\"type\":broken\n{\"type\":\"progress\",\"text\":\"working\"}\n"), &out, "")
	if err != nil || usage != nil {
		t.Fatalf("usage = %v, err = %v", usage, err)
	}
	if out.String() != "hello\n{\"not\":\"an event\"}\n{\"type\":broken\n{\"type\":\"progress\",\"text\":\"working\"}\n" {
		t.Fatalf("got %q", out.String())
	}
}

func TestFormatSaysWhenClaudeStopsWithAnError(t *testing.T) {
	var out bytes.Buffer
	if _, err := Format(strings.NewReader(`{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":30,"duration_ms":60000}`+"\n"), &out, ""); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Stopped (error_max_turns) after 30 turns in 1m0s\n" {
		t.Fatalf("got %q", out.String())
	}
}

func TestRunKeepsTheExitCodeAndWritesUsage(t *testing.T) {
	usagePath := filepath.Join(t.TempDir(), UsageFile)
	script := `printf '%s\n' '{"type":"result","subtype":"success","num_turns":1,"duration_ms":1000,"total_cost_usd":0.01,"usage":{"input_tokens":3,"output_tokens":4}}'; echo oops >&2; exit 3`
	var out, errs bytes.Buffer
	code, err := Run(t.Context(), []string{"sh", "-c", script}, strings.NewReader(""), &out, &errs, usagePath)
	if err != nil || code != 3 {
		t.Fatalf("code = %d, err = %v", code, err)
	}
	if out.String() != "Finished after 1 turns in 1s, $0.01\n" || errs.String() != "oops\n" {
		t.Fatalf("stdout = %q, stderr = %q", out.String(), errs.String())
	}
	data, err := os.ReadFile(usagePath)
	if err != nil {
		t.Fatal(err)
	}
	var usage Usage
	if err := json.Unmarshal(data, &usage); err != nil || usage.Tokens != 7 || usage.Cost != 0.01 {
		t.Fatalf("usage = %+v, err = %v", usage, err)
	}
}

func TestRunAddsUpEveryAgentInAStep(t *testing.T) {
	usagePath := filepath.Join(t.TempDir(), UsageFile)
	for _, tokens := range []string{"10", "20"} {
		script := `printf '%s\n' '{"type":"result","subtype":"success","total_cost_usd":0.1,"usage":{"input_tokens":` + tokens + `}}'`
		if code, err := Run(t.Context(), []string{"sh", "-c", script}, strings.NewReader(""), io.Discard, io.Discard, usagePath); err != nil || code != 0 {
			t.Fatalf("code = %d, err = %v", code, err)
		}
	}
	data, _ := os.ReadFile(usagePath)
	var usage Usage
	if err := json.Unmarshal(data, &usage); err != nil || usage.Tokens != 30 || math.Abs(usage.Cost-0.2) > 1e-9 {
		t.Fatalf("usage = %+v, err = %v", usage, err)
	}
}

func TestRunNeedsACommand(t *testing.T) {
	if code, err := Run(t.Context(), nil, nil, nil, nil, ""); err == nil || code != 2 {
		t.Fatalf("code = %d, err = %v", code, err)
	}
}
