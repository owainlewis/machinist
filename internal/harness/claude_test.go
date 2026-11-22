package harness

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

func TestFormatReadsAResultWhoseTypeIsNotTheFirstKey(t *testing.T) {
	var out bytes.Buffer
	line := `{"duration_api_ms":185000,"type":"result","subtype":"success","is_error":false,"num_turns":3,"duration_ms":4000,"total_cost_usd":0.25,"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":2,"cache_read_input_tokens":100}}`
	usage, err := Format(strings.NewReader(line+"\n"), &out, "")
	if err != nil {
		t.Fatal(err)
	}
	if usage == nil || usage.Tokens != 117 || usage.Cost != 0.25 {
		t.Fatalf("usage = %+v", usage)
	}
	if out.String() != "Finished after 3 turns in 4s, $0.25\n" {
		t.Fatalf("got %q", out.String())
	}
}

func TestFormatPassesOtherOutputThrough(t *testing.T) {
	var out bytes.Buffer
	usage, err := Format(strings.NewReader("hello\n{\"not\":\"an event\"}\n{\"type\":broken\n{\"type\":\"progress\",\"text\":\"working\"}\n{\"text\":\"x\",\"type\":\"progress\"}\nnull\n[1]\n"), &out, "")
	if err != nil || usage != nil {
		t.Fatalf("usage = %v, err = %v", usage, err)
	}
	if out.String() != "hello\n{\"not\":\"an event\"}\n{\"type\":broken\n{\"type\":\"progress\",\"text\":\"working\"}\n{\"text\":\"x\",\"type\":\"progress\"}\nnull\n[1]\n" {
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
	code, err := Run(t.Context(), Format, []string{"sh", "-c", script}, strings.NewReader(""), &out, &errs, usagePath)
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
		if code, err := Run(t.Context(), Format, []string{"sh", "-c", script}, strings.NewReader(""), io.Discard, io.Discard, usagePath); err != nil || code != 0 {
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
	if code, err := Run(t.Context(), Format, nil, nil, nil, nil, ""); err == nil || code != 2 {
		t.Fatalf("code = %d, err = %v", code, err)
	}
}

// Claude Code names its model when it starts, so a step that stops before
// the result still says which model ran.
func TestFormatKeepsTheModelClaudeStartedWith(t *testing.T) {
	stream := `{"type":"system","subtype":"init","model":"claude-opus-5-5"}` + "\n"
	usage, err := Format(strings.NewReader(stream), io.Discard, "/work")
	if err != nil || usage == nil || usage.Model != "claude-opus-5-5" || usage.Tokens != 0 {
		t.Fatalf("usage = %+v, err = %v", usage, err)
	}
	stream += `{"type":"result","subtype":"success","total_cost_usd":0.1,"usage":{"input_tokens":3}}` + "\n"
	usage, err = Format(strings.NewReader(stream), io.Discard, "/work")
	if err != nil || usage == nil || usage.Model != "claude-opus-5-5" || usage.Tokens != 3 || usage.Cost != 0.1 || usage.CostUnknown {
		t.Fatalf("usage = %+v, err = %v", usage, err)
	}
}

// Claude Code reports its rate limit as it goes. Within the limit it says
// nothing; near or over it, a short notice, never the raw JSON.
func TestFormatTurnsRateLimitEventsIntoNotices(t *testing.T) {
	var out bytes.Buffer
	stream := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1795400000,"rateLimitType":"five_hour"},"uuid":"u1","session_id":"s1"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1795400000,"rateLimitType":"seven_day","utilization":0.91},"uuid":"u2","session_id":"s1"}
{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1795400000,"rateLimitType":"five_hour"},"uuid":"u3","session_id":"s1"}
{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}
{"type":"rate_limit_event"}
`
	if _, err := Format(strings.NewReader(stream), &out, ""); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"Near the seven-day rate limit (91% used)",
		"Hit the five-hour rate limit, resets at 2026-11-23 02:13 UTC",
		"Hit the rate limit",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

// Agents link files they mention with file:// URLs to the runner's workspace,
// which no one reading the log can open, so the log shows the path in the
// repository instead.
func TestFormatTurnsWorkspaceFileLinksIntoRepositoryPaths(t *testing.T) {
	var out bytes.Buffer
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"Fixed [upload.ts](file:///work/src/upload.ts#L12) and file:///work/README.md. See file:///work, https://example.com/file:///work/x, file:///workspace/other.go and file:///etc/hosts."}]}}`
	if _, err := Format(strings.NewReader(line+"\n"), &out, "/work"); err != nil {
		t.Fatal(err)
	}
	want := "Fixed [upload.ts](src/upload.ts#L12) and README.md. See ., https://example.com/file:///work/x, file:///workspace/other.go and file:///etc/hosts.\n"
	if out.String() != want {
		t.Fatalf("got  %q\nwant %q", out.String(), want)
	}
}

func TestAgyFormatTurnsWorkspaceFileLinksIntoRepositoryPaths(t *testing.T) {
	var out bytes.Buffer
	stream := `{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"agent_response","text_delta":"Changed [retry.go](file:///work/internal/"}}
{"event":"step_update","step_update":{"state":"DONE","step_type":"agent_response","text_delta":"retry.go)."}}
`
	if _, err := (Agy{}).Format(strings.NewReader(stream), &out, "/work/"); err != nil {
		t.Fatal(err)
	}
	if want := "Changed [retry.go](internal/retry.go).\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}
