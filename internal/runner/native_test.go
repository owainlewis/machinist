package runner

import (
	"encoding/json"
	"github.com/owainlewis/machinist/internal/config"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type nativeInput struct {
	strings.Builder
	closed bool
}

func (i *nativeInput) Close() error { i.closed = true; return nil }
func testNative(t *testing.T, adapter string) (*nativeStream, *nativeInput, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := newEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.close() })
	input := &nativeInput{}
	return &nativeStream{adapter: adapter, log: log, stdin: input, root: "primary"}, input, path
}
func feedNative(t *testing.T, n *nativeStream, text string) {
	t.Helper()
	for _, part := range []string{text[:len(text)/2], text[len(text)/2:]} {
		if _, err := io.WriteString(n, part); err != nil {
			t.Fatal(err)
		}
	}
}
func TestNativeCodexChildLifecycleAndPrimary(t *testing.T) {
	n, input, path := testNative(t, "codex-app-server")
	feedNative(t, n, `{"method":"item/started","params":{"threadId":"primary","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child1","agentPath":"/root/read"}}}`+"\n")
	feedNative(t, n, `{"method":"item/completed","params":{"threadId":"primary","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child1"}}}`+"\n")
	if n.active["child1"].Status != "running" {
		t.Fatal("start notification completed child")
	}
	feedNative(t, n, `{"method":"item/started","params":{"threadId":"primary","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child2","agentPath":"/root/read"}}}`+"\n")
	feedNative(t, n, `{"method":"item/started","params":{"threadId":"primary","item":{"type":"subAgentActivity","kind":"failed","agentThreadId":"child1"}}}`+"\n")
	feedNative(t, n, `{"method":"item/completed","params":{"threadId":"child1","item":{"type":"agentMessage","text":"CHILD"}}}`+"\n")
	feedNative(t, n, `{"method":"turn/completed","params":{"threadId":"child1","turn":{"status":"failed"}}}`+"\n")
	if input.closed || n.final != "" {
		t.Fatal("child completed primary")
	}
	feedNative(t, n, `{"method":"item/completed","params":{"threadId":"primary","item":{"type":"agentMessage","text":"PARENT"}}}`+"\n")
	feedNative(t, n, `{"method":"turn/completed","params":{"threadId":"primary","turn":{"status":"completed"}}}`+"\n")
	if !input.closed || n.final != "PARENT" || len(n.active) != 2 {
		t.Fatalf("bad parent state: %+v", n)
	}
	n.finish(false)
	if n.active["child2"].Status != "unknown" {
		t.Fatal("unobserved completion fabricated")
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "agent.activity") {
		t.Fatal("activity not written live")
	}
}
func TestNativeClaudeFailureIsOwnedByPrimary(t *testing.T) {
	n, _, _ := testNative(t, "claude-stream")
	for _, line := range []string{`{"type":"system","subtype":"task_started","task_id":"child1","task_type":"local_agent","description":"Reader"}`, `{"type":"system","subtype":"task_notification","task_id":"child1","status":"failed","summary":"read failed"}`, `{"type":"result","parent_tool_use_id":"child1","is_error":true,"result":"CHILD"}`, `{"type":"result","is_error":false,"result":"PARENT","structured_output":{"outcome":"no_change","summary":"PARENT"}}`} {
		feedNative(t, n, line+"\n")
	}
	if !strings.Contains(n.final, "PARENT") || n.active["child1"].Status != "failed" {
		t.Fatal("child failure escaped")
	}
}
func TestNativeInterruptAndUnknownEvents(t *testing.T) {
	n, _, _ := testNative(t, "claude-stream")
	feedNative(t, n, "bad json\n{\"type\":\"future\",\"v\":9}\n")
	_ = n.observe(Activity{NativeID: "child", Status: "running"})
	n.finish(true)
	if n.active["child"].Status != "interrupted" {
		t.Fatal("child not interrupted")
	}
}
func TestNativeResultSmallContract(t *testing.T) {
	result, err := parseNativeResult("```json\n" + `{"outcome":"no_change","summary":"answer"}` + "\n```")
	if err != nil || result.Outcome != "no_change" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err = parseNativeResult(`{"outcome":"no_change","summary":""}`); err == nil {
		t.Fatal("empty summary accepted")
	}
}
func TestNativeRPCPreflight(t *testing.T) {
	n, input, _ := testNative(t, "codex-app-server")
	n.model = "unavailable"
	if err := n.start(); err != nil {
		t.Fatal(err)
	}
	feedNative(t, n, "{\"id\":1,\"result\":{}}\n")
	feedNative(t, n, "{\"id\":2,\"result\":{\"account\":{\"type\":\"chatgpt\"}}}\n")
	if err := n.line([]byte(`{"id":3,"result":{"data":[{"id":"available"}]}}`)); err == nil {
		t.Fatal("missing model accepted")
	}
	if strings.Contains(input.String(), "thread/start") {
		t.Fatal("started before model validation")
	}
	var v any
	if json.Unmarshal([]byte(strings.Split(input.String(), "\n")[0]), &v) != nil {
		t.Fatal("invalid RPC")
	}
}

func TestNativeOversizedActivityFitsTransport(t *testing.T) {
	n, _, path := testNative(t, "claude-stream")
	body, _ := json.Marshal(map[string]any{"type": "system", "subtype": "task_notification", "task_id": "child", "status": "completed", "summary": strings.Repeat("x", 200<<10)})
	feedNative(t, n, string(body)+"\n")
	content, _ := os.ReadFile(path)
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if len(line) > 128<<10 {
			t.Fatalf("unshippable normalized event: %d", len(line))
		}
	}
}

func TestNativeEOFErrorIsNotSuccess(t *testing.T) {
	n, _, _ := testNative(t, "claude-stream")
	if _, err := io.WriteString(n, `{"type":"result","is_error":true,"result":"failed"}`); err != nil {
		t.Fatal(err)
	}
	if err := n.finish(false); err == nil || n.completed {
		t.Fatal("EOF error lost")
	}
}
func TestNativeEscapedActivityFitsTransport(t *testing.T) {
	n, _, path := testNative(t, "claude-stream")
	if err := n.observe(Activity{NativeID: strings.Repeat("\x00", 2000), Name: strings.Repeat("\x00", 10000), Status: strings.Repeat("\x00", 200000), Summary: strings.Repeat("\x00", 200000), ParentToolUseID: strings.Repeat("\x00", 2000)}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if len(body) > 128<<10 {
		t.Fatalf("unshippable escaped record %d", len(body))
	}
}

func TestNativeDefaultModelTravelsInRequest(t *testing.T) {
	n, input, _ := testNative(t, "codex-app-server")
	if err := n.line([]byte(`{"id":3,"result":{"data":[{"id":"account-default","model":"account-default","isDefault":true}]}}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.String(), `"model":"account-default"`) {
		t.Fatalf("default absent from request: %s", input.String())
	}
}

func TestClaudeOnlyKnownAgentsAndStableLabels(t *testing.T) {
	n, _, path := testNative(t, "claude-stream")
	for _, line := range []string{
		`{"type":"system","subtype":"task_started","task_id":"shell","task_type":"local_bash","description":"npm test"}`,
		`{"type":"system","subtype":"task_started","task_id":"reader","task_type":"local_agent","description":"Review code","tool_use_id":"tool-reader"}`,
		`{"type":"system","subtype":"task_progress","task_id":"reader","description":"Running git diff"}`,
		`{"type":"system","subtype":"task_notification","task_id":"shell","status":"completed"}`,
		`{"type":"assistant","agent_id":"reader","parent_tool_use_id":"tool-reader","message":{"content":[{"type":"text","text":"Report"}]}}`,
		`{"type":"system","subtype":"task_notification","task_id":"reader","status":"completed","description":"Running tests","summary":"Accepted"}`,
	} {
		feedNative(t, n, line+"\n")
	}
	if len(n.active) != 1 || n.active["reader"].Name != "Review code" || n.active["reader"].Status != "completed" {
		t.Fatalf("bad projection: %+v", n.active)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `\"native_id\":\"reader\"`) || strings.Contains(string(raw), `\"native_id\":\"tool-reader\"`) {
		t.Fatal("wrong child message identity")
	}
}
func TestCodexInteractionDoesNotCreateRootOrResetLifecycle(t *testing.T) {
	n, _, _ := testNative(t, "codex-app-server")
	for _, item := range []string{
		`{"type":"subAgentActivity","kind":"started","agentThreadId":"child","agentPath":"/root/reviewer"}`,
		`{"type":"subAgentActivity","kind":"interacted","agentThreadId":"child"}`,
		`{"type":"subAgentActivity","kind":"interacted","agentThreadId":"primary","agentPath":"/root"}`,
	} {
		feedNative(t, n, `{"method":"item/started","params":{"item":`+item+`}}`+"\n")
	}
	if len(n.active) != 1 || n.active["child"].Status != "running" {
		t.Fatalf("bad interaction: %+v", n.active)
	}
}

func TestNativeClaudeReportsConfiguredRole(t *testing.T) {
	n, _, _ := testNative(t, "claude-stream")
	feedNative(t, n, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool","name":"Agent","input":{"subagent_type":"reviewer","description":"An arbitrary task name"}}]}}`+"\n")
	feedNative(t, n, `{"type":"system","subtype":"task_started","task_id":"child","tool_use_id":"tool","task_type":"local_agent","description":"An arbitrary task name"}`+"\n")
	if n.active["child"].Role != "reviewer" {
		t.Fatalf("lost configured role: %+v", n.active)
	}
}
func TestNativeCodexReadsRoleWithoutChangingLifecycle(t *testing.T) {
	n, input, _ := testNative(t, "codex-app-server")
	feedNative(t, n, `{"method":"item/started","params":{"item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child","agentPath":"/root/arbitrary"}}}`+"\n")
	if !strings.Contains(input.String(), `"method":"thread/read"`) {
		t.Fatal("did not request role metadata")
	}
	feedNative(t, n, `{"id":101,"result":{"thread":{"id":"child","agentRole":"reviewer"}}}`+"\n")
	if n.active["child"].Role != "reviewer" || n.active["child"].Status != "running" {
		t.Fatalf("bad role update: %+v", n.active)
	}
	feedNative(t, n, `{"method":"item/started","params":{"item":{"type":"subAgentActivity","kind":"started","agentThreadId":"other","agentPath":"/root/other"}}}`+"\n")
	feedNative(t, n, `{"id":102,"error":{"code":-1,"message":"metadata unavailable"}}`+"\n")
	if n.active["other"].Role != "" {
		t.Fatal("invented missing role")
	}
}

func TestClaudeUsesStructuredReportRatherThanProse(t *testing.T) {
	const report = `{"outcome":"pr_ready","summary":"Delivered"}`
	output := `{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}` + "\n" + `{"type":"result","is_error":false,"result":"Human explanation","total_cost_usd":0.25,"structured_output":` + report + `}` + "\n"
	command := claudeAgent(t, `cat >/dev/null; found=0; for arg in "$@"; do if [ "$arg" = --json-schema ]; then found=1; fi; done; test "$found" = 1 || exit 8; printf '%s' '`+output+`'`, 5*time.Second)
	command.Adapter = "claude-stream"
	result, err := Execute(t.Context(), Options{Environment: os.Environ(), Prompt: testPrompt, Command: command, Repository: newGitRepository(t), DataDirectory: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report == nil || result.Report.Outcome != "pr_ready" || result.Report.Summary != "Delivered" {
		t.Fatalf("report lost: %+v", result.Report)
	}
	if result.Cost == nil || *result.Cost != 0.25 || result.CredentialSource != "ANTHROPIC_API_KEY" {
		t.Fatalf("cost = %v, source = %q", result.Cost, result.CredentialSource)
	}
}

func TestClaudeReportCannotFallBackToProseOrChild(t *testing.T) {
	for _, raw := range []string{"", `,"structured_output":null`, `,"structured_output":{"outcome":"no_change","summary":"","unexpected":true}`} {
		n, _, _ := testNative(t, "claude-stream")
		line := `{"type":"result","is_error":false,"result":"{\"outcome\":\"no_change\",\"summary\":\"prose fallback\"}"` + raw + `}`
		err := n.line([]byte(line))
		if err == nil {
			_, err = parseNativeResult(n.lastMessage())
		}
		if err == nil {
			t.Fatal("missing or invalid structured report accepted")
		}
	}
	n, _, _ := testNative(t, "claude-stream")
	feedNative(t, n, `{"type":"result","parent_tool_use_id":"child","is_error":false,"structured_output":{"outcome":"no_change","summary":"child"}}`+"\n")
	if n.completed || n.lastMessage() != "" {
		t.Fatal("child delivered primary result")
	}
	if _, err := parseNativeResult(`Explanation: {"outcome":"no_change","summary":"Done"}`); err == nil || !strings.Contains(err.Error(), "expected one JSON object") {
		t.Fatalf("unclear result error: %v", err)
	}
}

func TestNativeSchemaAlwaysAppliesToClaude(t *testing.T) {
	argv, err := nativeCommand(config.ResolvedCommand{Executor: "claude", Adapter: "claude-stream", Command: []string{"claude", "-p"}, Model: "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--json-schema "+nativeResultSchema) || !strings.HasSuffix(joined, "--model sonnet") {
		t.Fatalf("argv = %v", argv)
	}
}

func TestClaudeAcceptsKeyCredentialAndBareMode(t *testing.T) {
	if _, err := nativeCommand(config.ResolvedCommand{Executor: "claude", Adapter: "claude-stream", Command: []string{"claude", "-p", "--bare"}}); err != nil {
		t.Fatal(err)
	}
	n, _, _ := testNative(t, "claude-stream")
	feedNative(t, n, `{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}`+"\n")
	if n.credentialSource != "ANTHROPIC_API_KEY" {
		t.Fatalf("source = %q", n.credentialSource)
	}
}

func TestCodexRecordsCredentialSourceWithoutRequiringLogin(t *testing.T) {
	for body, want := range map[string]string{`{"account":{"type":"apiKey"}}`: "apiKey", `{"account":null}`: "none", `{"account":{"type":"chatgpt"}}`: "chatgpt"} {
		n, input, _ := testNative(t, "codex-app-server")
		if err := n.line([]byte(`{"id":2,"result":` + body + `}`)); err != nil {
			t.Fatal(err)
		}
		if n.credentialSource != want || !strings.Contains(input.String(), "model/list") {
			t.Fatalf("%s: source = %q, input = %s", body, n.credentialSource, input.String())
		}
	}
}

func fakeCodexServer(t *testing.T, directory, finalMessage string) string {
	t.Helper()
	message, err := json.Marshal(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "primary", "item": map[string]string{"type": "agentMessage", "text": finalMessage}}})
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(directory, "server.sh")
	body := `while IFS= read -r line; do
case "$line" in
*'"id":1,'*) echo '{"id":1,"result":{}}';;
*'"id":2,'*) echo '{"id":2,"result":{"account":{"type":"apiKey"}}}';;
*'"id":3,'*) echo '{"id":3,"result":{"data":[{"id":"fixture","model":"fixture","isDefault":true}]}}';;
*'"id":4,'*) echo '{"id":4,"result":{"thread":{"id":"primary"}}}';;
*'"id":5,'*) echo '` + string(message) + `'; echo '{"method":"turn/completed","params":{"threadId":"primary","turn":{"status":"completed"}}}';;
esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestCodexFinalMessageBecomesReport(t *testing.T) {
	directory := t.TempDir()
	script := fakeCodexServer(t, directory, `{"outcome":"needs_input","summary":"Stuck","question":"Which API?"}`)
	result, err := Execute(t.Context(), Options{Environment: os.Environ(), Prompt: testPrompt, Repository: ".", DataDirectory: directory, Command: config.ResolvedCommand{Name: "fake", Adapter: "codex-app-server", Command: []string{"sh", script, "app-server"}, Timeout: 10 * time.Second}, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report == nil || result.Report.Question != "Which API?" || result.CredentialSource != "apiKey" {
		t.Fatalf("result = %+v", result)
	}
}

func TestMalformedReportFailsTheRunWithTheReason(t *testing.T) {
	directory := t.TempDir()
	script := fakeCodexServer(t, directory, "All done, no JSON here.")
	result, err := Execute(t.Context(), Options{Environment: os.Environ(), Prompt: testPrompt, Repository: ".", DataDirectory: directory, Command: config.ResolvedCommand{Name: "fake", Adapter: "codex-app-server", Command: []string{"sh", script, "app-server"}, Timeout: 10 * time.Second}, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "invalid report") || result.State != StateFailed || result.Report != nil {
		t.Fatalf("state = %s, report = %+v, err = %v", result.State, result.Report, err)
	}
}
