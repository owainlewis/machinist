package controlplane

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/owainlewis/machinist/internal/triggers"
)

func (f *fixture) mcpCall(t *testing.T, token string, payload any) (*http.Response, []byte) {
	t.Helper()
	return f.call(t, http.MethodPost, "/mcp", token, payload)
}

func (f *fixture) mcpTool(t *testing.T, token, name string, args map[string]any) mcpCallResult {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
	resp, body := f.mcpCall(t, token, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("MCP tools/call %s: status = %d: %s", name, resp.StatusCode, body)
	}
	var rpc struct {
		JSONRPC string         `json:"jsonrpc"`
		Error   *jsonRPCError  `json:"error"`
		Result  *mcpCallResult `json:"result"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		t.Fatalf("decode RPC response: %v: %s", err, body)
	}
	if rpc.Error != nil {
		t.Fatalf("unexpected JSON-RPC error for %s: %+v", name, rpc.Error)
	}
	if rpc.Result == nil || len(rpc.Result.Content) == 0 {
		t.Fatalf("empty result for %s: %s", name, body)
	}
	return *rpc.Result
}

func decodeMCPToolJSON[T any](t *testing.T, result mcpCallResult) T {
	t.Helper()
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", result.Content[0].Text)
	}
	var out T
	if err := json.Unmarshal([]byte(result.Content[0].Text), &out); err != nil {
		t.Fatalf("decode tool JSON: %v: %s", err, result.Content[0].Text)
	}
	return out
}

func outputEvent(seq int64, stream, text string) json.RawMessage {
	raw, _ := json.Marshal(protocol.RunEvent{
		Sequence: seq,
		Time:     "2026-11-25T10:00:00Z",
		Type:     "process.output",
		Stream:   stream,
		Encoding: "base64",
		Data:     base64.StdEncoding.EncodeToString([]byte(text)),
	})
	return raw
}

func stepEvent(seq int64, name string) json.RawMessage {
	raw, _ := json.Marshal(protocol.RunEvent{
		Sequence: seq,
		Time:     "2026-11-25T10:00:00Z",
		Type:     "step.started",
		Message:  "name=" + name,
	})
	return raw
}

func TestMCPHandshakeAndProtocol(t *testing.T) {
	f := newFixture(t)
	runToken := f.newScopedToken(t, "grokbot", tickets.ScopeRun)

	// initialize negotiates protocol version and exposes tools capability.
	resp, body := f.mcpCall(t, runToken, map[string]any{
		"jsonrpc": "2.0",
		"id":      "init-1",
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "grokbot", "version": "1.0"},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d: %s", resp.StatusCode, body)
	}
	var initResp struct {
		ID     string `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			Capabilities struct {
				Tools map[string]any `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &initResp); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if initResp.ID != "init-1" || initResp.Result.ProtocolVersion != "2025-03-26" || initResp.Result.ServerInfo.Name != "machinist" || initResp.Result.ServerInfo.Version != "v1.2.3" || initResp.Result.Capabilities.Tools == nil {
		t.Fatalf("init = %+v", initResp)
	}

	// notifications/initialized returns 202 Accepted with no body.
	resp, body = f.mcpCall(t, runToken, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	if resp.StatusCode != http.StatusAccepted || len(bytes.TrimSpace(body)) != 0 {
		t.Fatalf("notification status = %d, body = %q", resp.StatusCode, body)
	}

	// ping succeeds.
	resp, body = f.mcpCall(t, runToken, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"result":{}`) {
		t.Fatalf("ping = %d: %s", resp.StatusCode, body)
	}

	// tools/list returns all eight tools.
	resp, body = f.mcpCall(t, runToken, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"})
	var listResp struct {
		Result struct {
			Tools []mcpTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list = %d: %s, err = %v", resp.StatusCode, body, err)
	}
	gotNames := map[string]bool{}
	for _, tool := range listResp.Result.Tools {
		gotNames[tool.Name] = true
	}
	for _, want := range []string{"list_jobs", "list_automations", "run_job", "get_job", "get_job_output", "cancel_job", "rerun_job", "get_stats"} {
		if !gotNames[want] {
			t.Fatalf("tools/list missing %q: %+v", want, listResp.Result.Tools)
		}
	}

	// Unknown method returns -32601; unknown tool returns -32602.
	resp, body = f.mcpCall(t, runToken, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "nope/method"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), fmt.Sprintf(`"code":%d`, jsonRPCMethodNotFound)) {
		t.Fatalf("unknown method = %d: %s", resp.StatusCode, body)
	}
	resp, body = f.mcpCall(t, runToken, map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{"name": "delete_everything"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), fmt.Sprintf(`"code":%d`, jsonRPCInvalidParams)) {
		t.Fatalf("unknown tool = %d: %s", resp.StatusCode, body)
	}

	// GET /mcp returns 405 Method Not Allowed when authenticated.
	resp, _ = f.call(t, http.MethodGet, "/mcp", runToken, nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp status = %d, want 405", resp.StatusCode)
	}

	// Session cookie alone does not authenticate /mcp (bearer token required).
	req, err := http.NewRequest(http.MethodPost, f.http.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.token})
	cookieResp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cookieResp.Body.Close()
	if cookieResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie auth on /mcp = %d, want 401", cookieResp.StatusCode)
	}
}

func TestMCPRunListGetOutputAndCancelCustomJob(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	f.server.factory = &factory.Factory{
		Presets: map[string]factory.Preset{
			"default": {
				Name: "default",
				Steps: []runner.Step{
					{Name: "checkout", Run: "true"},
					{Name: "implement", Run: "true"},
					{Name: "publish", Run: "true"},
				},
			},
		},
	}
	botToken := f.newScopedToken(t, "grokbot", tickets.ScopeRun)

	// 1. run_job starts a custom job with repo and prompt.
	created := decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, botToken, "run_job", map[string]any{
		"repo":   "owner/repo",
		"prompt": "Add MCP support\n\nInclude scope checks.",
		"key":    "slack:C1:100",
	}))
	if !created.Created || created.State != JobQueued || created.Repo != "owner/repo" || created.Source != tickets.OriginMCP || created.CreatedBy != "grokbot" {
		t.Fatalf("created = %+v", created)
	}
	if len(created.Steps) != 3 || created.Steps[0].Name != "checkout" || created.Steps[0].State != "pending" {
		t.Fatalf("queued steps = %+v", created.Steps)
	}

	// Reusing the same idempotency key returns the same job with created=false.
	repeated := decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, botToken, "run_job", map[string]any{
		"repo":   "owner/repo",
		"prompt": "Add MCP support\n\nInclude scope checks.",
		"key":    "slack:C1:100",
	}))
	if repeated.Created || repeated.ID != created.ID {
		t.Fatalf("repeated = %+v", repeated)
	}

	// get_job_output before any run returns empty queued output cleanly.
	beforeRun := decodeMCPToolJSON[mcpJobOutput](t, f.mcpTool(t, botToken, "get_job_output", map[string]any{"id": created.ID}))
	if beforeRun.State != JobQueued || beforeRun.Output != "" || beforeRun.Cursor != 0 || beforeRun.HasMore {
		t.Fatalf("beforeRun = %+v", beforeRun)
	}

	// 2. Runner claims the job and streams events across two steps.
	if err := f.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	pollResp, pollBody := f.call(t, http.MethodPost, "/api/v1/runners/poll", f.token, protocol.PollRequest{Harnesses: []string{protocol.StepsHarness}})
	if pollResp.StatusCode != http.StatusOK {
		t.Fatalf("poll = %d: %s", pollResp.StatusCode, pollBody)
	}
	var runReq runner.RunRequest
	if err := json.Unmarshal(pollBody, &runReq); err != nil {
		t.Fatal(err)
	}

	batch := protocol.RunEventBatch{Events: []json.RawMessage{
		stepEvent(1, "checkout"),
		outputEvent(2, "stdout", "Cloning owner/repo...\n"),
		stepEvent(3, "implement"),
		outputEvent(4, "stdout", "▸ Reading internal/controlplane/server.go\nWriting mcp_http.go\n"),
		outputEvent(5, "stderr", "go test ok\n"),
	}}
	if resp, body := f.call(t, http.MethodPost, "/api/v1/runs/"+runReq.RunID+"/events", f.token, batch); resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest events = %d: %s", resp.StatusCode, body)
	}

	// 3. get_job shows running state, steps, current step, and latest action.
	running := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, botToken, "get_job", map[string]any{"id": created.ID}))
	if running.State != JobRunning || running.CurrentStep != "implement" || running.LatestAction != "Reading internal/controlplane/server.go" || running.LatestRunID != runReq.RunID {
		t.Fatalf("running = %+v", running)
	}
	if len(running.Steps) != 3 || running.Steps[0].State != "succeeded" || running.Steps[1].State != "running" || running.Steps[2].State != "pending" {
		t.Fatalf("running steps = %+v", running.Steps)
	}

	// 4. list_jobs finds the running job and supports filtering.
	listed := decodeMCPToolJSON[struct {
		Jobs  []mcpJobSummary `json:"jobs"`
		Total int             `json:"total"`
	}](t, f.mcpTool(t, botToken, "list_jobs", map[string]any{"repo": "owner/repo", "state": "running", "source": "mcp"}))
	if listed.Total != 1 || len(listed.Jobs) != 1 || listed.Jobs[0].ID != created.ID || listed.Jobs[0].CurrentStep != "implement" {
		t.Fatalf("listed = %+v", listed)
	}
	emptyList := decodeMCPToolJSON[struct {
		Jobs []mcpJobSummary `json:"jobs"`
	}](t, f.mcpTool(t, botToken, "list_jobs", map[string]any{"state": "failed"}))
	if len(emptyList.Jobs) != 0 {
		t.Fatalf("expected 0 failed jobs, got %+v", emptyList.Jobs)
	}

	// 5. get_job_output returns decoded output, supports step filter and cursor tailing.
	out := decodeMCPToolJSON[mcpJobOutput](t, f.mcpTool(t, botToken, "get_job_output", map[string]any{"id": created.ID}))
	if out.Cursor != 5 || out.HasMore || !strings.Contains(out.Output, "Cloning owner/repo...") || !strings.Contains(out.Output, "go test ok") {
		t.Fatalf("out = %+v", out)
	}
	if len(out.Steps) != 2 || out.Steps[0].Name != "checkout" || out.Steps[1].Name != "implement" {
		t.Fatalf("out.Steps = %+v", out.Steps)
	}

	// Filter to one step only.
	implOut := decodeMCPToolJSON[mcpJobOutput](t, f.mcpTool(t, botToken, "get_job_output", map[string]any{"id": created.ID, "step": "implement"}))
	if strings.Contains(implOut.Output, "Cloning") || !strings.Contains(implOut.Output, "Writing mcp_http.go") || !strings.Contains(implOut.Output, "go test ok") {
		t.Fatalf("implOut = %+v", implOut)
	}

	// Incremental tailing using after=cursor sees only new output.
	moreBatch := protocol.RunEventBatch{Events: []json.RawMessage{
		outputEvent(6, "stdout", "▸ Running go test ./...\nPASS\n"),
	}}
	if resp, body := f.call(t, http.MethodPost, "/api/v1/runs/"+runReq.RunID+"/events", f.token, moreBatch); resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest more events = %d: %s", resp.StatusCode, body)
	}
	tail := decodeMCPToolJSON[mcpJobOutput](t, f.mcpTool(t, botToken, "get_job_output", map[string]any{"id": created.ID, "after": out.Cursor}))
	if tail.Cursor != 6 || strings.Contains(tail.Output, "Cloning") || !strings.Contains(tail.Output, "PASS") || len(tail.Steps) != 1 || tail.Steps[0].Name != "implement" {
		t.Fatalf("tail = %+v", tail)
	}

	// max_bytes paginates without dropping bytes.
	page1 := decodeMCPToolJSON[mcpJobOutput](t, f.mcpTool(t, botToken, "get_job_output", map[string]any{"id": created.ID, "max_bytes": 30}))
	if !page1.HasMore || page1.Cursor >= 6 || page1.Output == "" {
		t.Fatalf("page1 = %+v", page1)
	}
	page2 := decodeMCPToolJSON[mcpJobOutput](t, f.mcpTool(t, botToken, "get_job_output", map[string]any{"id": created.ID, "after": page1.Cursor, "max_bytes": 1024}))
	if page2.HasMore || page2.Cursor != 6 || page1.Output+page2.Output != out.Output+tail.Output {
		t.Fatalf("paginated mismatch:\np1=%q\np2=%q\nfull=%q", page1.Output, page2.Output, out.Output+tail.Output)
	}

	// 6. Complete the run with a PR and verify get_job returns the PR link and recorded steps.
	f.github.SetBranchPR("machinist/"+created.ID, 42, "OPEN", false)
	tok, cost := int64(5000), 0.75
	completion := protocol.Completion{
		State:  tickets.RunSucceeded,
		Tokens: &tok,
		Cost:   &cost,
		Report: &protocol.Report{Outcome: protocol.OutcomeNoChange, Summary: "Opened PR #42"},
		Stages: []protocol.StageResult{
			{Name: "checkout", Kind: "step", State: tickets.RunSucceeded, DurationMillis: 1200},
			{Name: "implement", Kind: "step", State: tickets.RunSucceeded, DurationMillis: 9000},
			{Name: "publish", Kind: "step", State: tickets.RunSucceeded, DurationMillis: 800},
		},
	}
	if resp, body := f.call(t, http.MethodPost, "/api/v1/runs/"+runReq.RunID+"/complete", f.token, completion); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("complete = %d: %s", resp.StatusCode, body)
	}

	finished := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, botToken, "get_job", map[string]any{"id": created.ID}))
	if finished.State != mcpJobNeedsYou || finished.PRNumber != 42 || finished.PRState != tickets.PROpen || finished.PRURL != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("finished = %+v", finished)
	}
	if finished.LatestAction != "Running go test ./..." || finished.Summary != "Opened PR #42" || finished.Tokens != 5000 || finished.CostUSD == nil || *finished.CostUSD != 0.75 {
		t.Fatalf("finished details = %+v", finished)
	}
	if len(finished.Steps) != 3 || finished.Steps[2].State != tickets.RunSucceeded || finished.Steps[1].DurationMillis != 9000 {
		t.Fatalf("finished steps = %+v", finished.Steps)
	}

	// 7. Start a second job and cancel it via cancel_job.
	second := decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, botToken, "run_job", map[string]any{
		"repo":   "owner/repo",
		"prompt": "Cancel me",
	}))
	cancelled := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, botToken, "cancel_job", map[string]any{"id": second.ID}))
	if cancelled.State != JobCancelled || !strings.Contains(cancelled.LatestMessage, "Cancelled by grokbot") {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	// Cancelling an already ended job returns a tool error.
	again := f.mcpTool(t, botToken, "cancel_job", map[string]any{"id": second.ID})
	if !again.IsError || !strings.Contains(again.Content[0].Text, "already ended") {
		t.Fatalf("cancel ended = %+v", again)
	}
}

func TestMCPAutomationsListAndRun(t *testing.T) {
	f := newFixture(t)
	if err := f.store.AddProject(t.Context(), "owner/repo", "alice"); err != nil {
		t.Fatal(err)
	}
	cron, err := triggers.ParseCron("0 9 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	f.server.schedule.Automations = []factory.Automation{
		{Name: "nightly-deps", Repo: "owner/repo", Preset: "default", Prompt: "Update dependencies", Cron: cron},
	}
	botToken := f.newScopedToken(t, "grokbot", tickets.ScopeRun)

	// list_automations returns the configured automation.
	before := decodeMCPToolJSON[struct {
		Automations []mcpAutomation `json:"automations"`
	}](t, f.mcpTool(t, botToken, "list_automations", nil))
	if len(before.Automations) != 1 || before.Automations[0].Name != "nightly-deps" || before.Automations[0].Repo != "owner/repo" || before.Automations[0].Preset != "default" || before.Automations[0].LastJob != nil {
		t.Fatalf("before = %+v", before)
	}

	// run_job with automation name starts the automation.
	started := decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, botToken, "run_job", map[string]any{"automation": "nightly-deps"}))
	if !started.Created || started.State != JobQueued || started.Source != tickets.OriginSchedule || started.CreatedBy != "grokbot" {
		t.Fatalf("started = %+v", started)
	}

	// Running the same automation while active returns the active job with created=false.
	dedup := decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, botToken, "run_job", map[string]any{"automation": "nightly-deps"}))
	if dedup.Created || dedup.ID != started.ID {
		t.Fatalf("dedup = %+v", dedup)
	}

	// list_automations now includes last_job.
	after := decodeMCPToolJSON[struct {
		Automations []mcpAutomation `json:"automations"`
	}](t, f.mcpTool(t, botToken, "list_automations", map[string]any{}))
	if len(after.Automations) != 1 || after.Automations[0].LastJob == nil || after.Automations[0].LastJob.ID != started.ID || after.Automations[0].LastJob.State != JobQueued {
		t.Fatalf("after = %+v", after)
	}

	// Validation errors return isError=true.
	for _, bad := range []map[string]any{
		{"automation": "missing"},
		{"automation": "nightly-deps", "repo": "owner/repo", "prompt": "both"},
		{"repo": "owner/repo"},
		{},
	} {
		res := f.mcpTool(t, botToken, "run_job", bad)
		if !res.IsError {
			t.Fatalf("expected error for %+v, got %+v", bad, res)
		}
	}
}

func TestMCPScopeChecksAndTokenRevocation(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	cron, err := triggers.ParseCron("0 9 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	f.server.schedule.Automations = []factory.Automation{
		{Name: "nightly", Repo: "owner/repo", Preset: "default", Prompt: "Check deps", Cron: cron},
	}

	readToken := f.newScopedToken(t, "watcher", tickets.ScopeRead)
	runToken := f.newScopedToken(t, "grokbot", tickets.ScopeRun)
	operatorToken := f.newToken(t, "alice", tickets.RoleOperator)
	runnerToken := f.newToken(t, "vm-runner", tickets.RoleRunner)

	// Start a job using the run-scoped token.
	started := decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, runToken, "run_job", map[string]any{
		"repo":   "owner/repo",
		"prompt": "Investigate latency",
	}))

	// A read-scoped token can use all read tools...
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"list_jobs", nil},
		{"list_automations", nil},
		{"get_job", map[string]any{"id": started.ID}},
		{"get_job_output", map[string]any{"id": started.ID}},
		{"get_stats", nil},
	} {
		res := f.mcpTool(t, readToken, tc.tool, tc.args)
		if res.IsError {
			t.Fatalf("read token calling %s failed: %s", tc.tool, res.Content[0].Text)
		}
	}

	// ...but cannot call run_job, cancel_job, or rerun_job.
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"run_job", map[string]any{"repo": "owner/repo", "prompt": "Unauthorized"}},
		{"run_job", map[string]any{"automation": "nightly"}},
		{"cancel_job", map[string]any{"id": started.ID}},
		{"rerun_job", map[string]any{"job_id": started.ID}},
	} {
		res := f.mcpTool(t, readToken, tc.tool, tc.args)
		if !res.IsError || !strings.Contains(res.Content[0].Text, "scope") {
			t.Fatalf("read token calling %s = %+v, want scope error", tc.tool, res)
		}
	}
	// Confirm the job is still queued (not cancelled by the read token).
	check := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, readToken, "get_job", map[string]any{"id": started.ID}))
	if check.State != JobQueued {
		t.Fatalf("job state after denied cancel = %q", check.State)
	}

	// Existing unscoped operator and legacy tokens keep full MCP access.
	for _, token := range []string{operatorToken, f.token} {
		job := decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, token, "run_job", map[string]any{
			"repo":   "owner/repo",
			"prompt": "Started by full token",
		}))
		cancelled := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, token, "cancel_job", map[string]any{"id": job.ID}))
		if cancelled.State != JobCancelled {
			t.Fatalf("full token cancel = %+v", cancelled)
		}
	}

	// A runner token is rejected on /mcp with 403 Forbidden.
	resp, body := f.mcpCall(t, runnerToken, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("runner on /mcp = %d: %s", resp.StatusCode, body)
	}

	// Revoking grokbot immediately blocks grokbot on /mcp with 401 while watcher still works.
	if err := f.store.RevokeToken(t.Context(), "grokbot"); err != nil {
		t.Fatal(err)
	}
	resp, body = f.mcpCall(t, runToken, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("revoked token on /mcp = %d, header=%q, body=%s", resp.StatusCode, resp.Header.Get("WWW-Authenticate"), body)
	}
	if res := f.mcpTool(t, readToken, "list_jobs", nil); res.IsError {
		t.Fatalf("watcher token failed after revoking grokbot: %s", res.Content[0].Text)
	}

	// Verify batch JSON-RPC request works over /mcp.
	batchResp, batchBody := f.mcpCall(t, readToken, []map[string]any{
		{"jsonrpc": "2.0", "id": 10, "method": "ping"},
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
		{"jsonrpc": "2.0", "id": 11, "method": "tools/list"},
	})
	if batchResp.StatusCode != http.StatusOK {
		t.Fatalf("batch status = %d: %s", batchResp.StatusCode, batchBody)
	}
	var replies []jsonRPCResponse
	if err := json.Unmarshal(batchBody, &replies); err != nil || len(replies) != 2 {
		t.Fatalf("batch replies = %+v, err = %v", replies, err)
	}
}

func TestMCPPRReadyStatesAndMergeGate(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	readToken := f.newScopedToken(t, "watcher", tickets.ScopeRead)

	// Create tickets covering waiting_for_ci, held needs_you, unshipped needs_you,
	// non-object shipping needs_you, and done.
	waiting := f.assign(t, "mcp-ci")
	if _, _, err := f.store.Apply(t.Context(), waiting.ID, tickets.StateWorking, tickets.Transition{
		State:    tickets.StatePRReady,
		PRNumber: 101,
		Shipping: `{"since":"2026-11-27T15:00:00Z","head":"abc123"}`,
		MergeGate: &tickets.MergeGate{
			Decision: "auto_merge",
			Risk:     "low",
			Reason:   "Scoped documentation and test update.",
		},
	}); err != nil {
		t.Fatal(err)
	}

	held := f.assign(t, "mcp-held")
	if _, _, err := f.store.Apply(t.Context(), held.ID, tickets.StateWorking, tickets.Transition{
		State:    tickets.StatePRReady,
		PRNumber: 102,
		Shipping: `{"since":"2026-11-27T15:00:00Z","hold":"merge gate requires human review"}`,
		MergeGate: &tickets.MergeGate{
			Decision: "human_review",
			Risk:     "medium",
			Reason:   "Modifies authentication middleware.",
		},
	}); err != nil {
		t.Fatal(err)
	}

	unshipped := f.assign(t, "mcp-unshipped")
	if _, _, err := f.store.Apply(t.Context(), unshipped.ID, tickets.StateWorking, tickets.Transition{
		State:    tickets.StatePRReady,
		PRNumber: 103,
	}); err != nil {
		t.Fatal(err)
	}

	badWatch := f.assign(t, "mcp-bad-watch")
	if _, _, err := f.store.Apply(t.Context(), badWatch.ID, tickets.StateWorking, tickets.Transition{
		State:    tickets.StatePRReady,
		PRNumber: 104,
		Shipping: "null",
	}); err != nil {
		t.Fatal(err)
	}

	done := f.assign(t, "mcp-done")
	if _, _, err := f.store.Apply(t.Context(), done.ID, tickets.StateWorking, tickets.Transition{
		State: tickets.StateDone,
	}); err != nil {
		t.Fatal(err)
	}

	// get_job reports distinct states and merge_gate when present, omitting merge_gate when absent.
	waitingRes := f.mcpTool(t, readToken, "get_job", map[string]any{"id": waiting.ID})
	waitingJob := decodeMCPToolJSON[mcpJobDetail](t, waitingRes)
	if waitingJob.State != mcpJobWaitingForCI || waitingJob.MergeGate == nil || waitingJob.MergeGate.Decision != "auto_merge" || waitingJob.MergeGate.Risk != "low" || waitingJob.MergeGate.Reason != "Scoped documentation and test update." {
		t.Fatalf("waitingJob = %+v", waitingJob)
	}

	heldJob := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, readToken, "get_job", map[string]any{"id": held.ID}))
	if heldJob.State != mcpJobNeedsYou || heldJob.MergeGate == nil || heldJob.MergeGate.Decision != "human_review" || heldJob.MergeGate.Risk != "medium" || heldJob.MergeGate.Reason != "Modifies authentication middleware." {
		t.Fatalf("heldJob = %+v", heldJob)
	}

	unshippedRes := f.mcpTool(t, readToken, "get_job", map[string]any{"id": unshipped.ID})
	if strings.Contains(unshippedRes.Content[0].Text, `"merge_gate"`) {
		t.Fatalf("expected merge_gate omitted when absent: %s", unshippedRes.Content[0].Text)
	}
	unshippedJob := decodeMCPToolJSON[mcpJobDetail](t, unshippedRes)
	if unshippedJob.State != mcpJobNeedsYou || unshippedJob.MergeGate != nil {
		t.Fatalf("unshippedJob = %+v", unshippedJob)
	}

	badWatchJob := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, readToken, "get_job", map[string]any{"id": badWatch.ID}))
	if badWatchJob.State != mcpJobNeedsYou {
		t.Fatalf("badWatchJob = %+v", badWatchJob)
	}

	doneJob := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, readToken, "get_job", map[string]any{"id": done.ID}))
	if doneJob.State != JobDone || doneJob.MergeGate != nil {
		t.Fatalf("doneJob = %+v", doneJob)
	}

	// list_jobs filters by waiting_for_ci, needs_you (with limit/total/has_more), and done (excluding pr_ready).
	type listResult struct {
		Jobs    []mcpJobSummary `json:"jobs"`
		Total   int             `json:"total"`
		HasMore bool            `json:"has_more"`
	}

	ciList := decodeMCPToolJSON[listResult](t, f.mcpTool(t, readToken, "list_jobs", map[string]any{"state": "waiting_for_ci"}))
	if ciList.Total != 1 || ciList.HasMore || len(ciList.Jobs) != 1 || ciList.Jobs[0].ID != waiting.ID || ciList.Jobs[0].State != mcpJobWaitingForCI || ciList.Jobs[0].MergeGate == nil || ciList.Jobs[0].MergeGate.Decision != "auto_merge" {
		t.Fatalf("ciList = %+v", ciList)
	}

	needsList := decodeMCPToolJSON[listResult](t, f.mcpTool(t, readToken, "list_jobs", map[string]any{"state": "needs_you"}))
	if needsList.Total != 3 || needsList.HasMore || len(needsList.Jobs) != 3 {
		t.Fatalf("needsList = %+v", needsList)
	}
	seenNeeds := map[string]mcpJobSummary{}
	for _, job := range needsList.Jobs {
		if job.State != mcpJobNeedsYou {
			t.Fatalf("expected needs_you state in list, got %+v", job)
		}
		seenNeeds[job.ID] = job
	}
	if seenNeeds[held.ID].MergeGate == nil || seenNeeds[held.ID].MergeGate.Risk != "medium" || seenNeeds[unshipped.ID].MergeGate != nil || seenNeeds[badWatch.ID].ID != badWatch.ID {
		t.Fatalf("seenNeeds = %+v", seenNeeds)
	}

	needsLimited := decodeMCPToolJSON[listResult](t, f.mcpTool(t, readToken, "list_jobs", map[string]any{"state": "needs_you", "limit": 2}))
	if needsLimited.Total != 3 || !needsLimited.HasMore || len(needsLimited.Jobs) != 2 {
		t.Fatalf("needsLimited = %+v", needsLimited)
	}

	doneListRes := f.mcpTool(t, readToken, "list_jobs", map[string]any{"state": "done"})
	if strings.Contains(doneListRes.Content[0].Text, `"merge_gate"`) {
		t.Fatalf("expected merge_gate omitted on done list item: %s", doneListRes.Content[0].Text)
	}
	doneList := decodeMCPToolJSON[listResult](t, doneListRes)
	if doneList.Total != 1 || len(doneList.Jobs) != 1 || doneList.Jobs[0].ID != done.ID || doneList.Jobs[0].State != JobDone {
		t.Fatalf("doneList = %+v", doneList)
	}

	// Unknown state reports all valid states.
	badState := f.mcpTool(t, readToken, "list_jobs", map[string]any{"state": "bogus"})
	if !badState.IsError || !strings.Contains(badState.Content[0].Text, "waiting_for_ci") || !strings.Contains(badState.Content[0].Text, "needs_you") {
		t.Fatalf("badState = %+v", badState)
	}
}

func TestMCPRerunJobAndGetStats(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	if err := f.store.AddProject(t.Context(), "owner/other", "alice"); err != nil {
		t.Fatal(err)
	}
	runToken := f.newScopedToken(t, "grokbot", tickets.ScopeRun)
	readToken := f.newScopedToken(t, "watcher", tickets.ScopeRead)

	// Start a job on owner/repo and transition it to held pr_ready with a merge-gate verdict.
	started := decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, runToken, "run_job", map[string]any{
		"repo":   "owner/repo",
		"prompt": "Fix retry loop",
	}))
	if _, _, err := f.store.Apply(t.Context(), started.ID, tickets.StateWorking, tickets.Transition{
		State:    tickets.StatePRReady,
		PRNumber: 55,
		Shipping: `{"hold":"human review"}`,
		MergeGate: &tickets.MergeGate{
			Decision: "human_review",
			Risk:     "high",
			Reason:   "Needs careful check.",
		},
	}); err != nil {
		t.Fatal(err)
	}

	// rerun_job starts a new queued attempt and clears stale merge_gate.
	rerun := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, runToken, "rerun_job", map[string]any{"job_id": started.ID}))
	if rerun.ID != started.ID || rerun.State != JobQueued || rerun.MergeGate != nil {
		t.Fatalf("rerun = %+v", rerun)
	}

	// Calling rerun_job while the job is active returns a tool error.
	stillWorking := f.mcpTool(t, runToken, "rerun_job", map[string]any{"job_id": started.ID})
	if !stillWorking.IsError || !strings.Contains(stillWorking.Content[0].Text, "still working") {
		t.Fatalf("stillWorking = %+v", stillWorking)
	}

	// Validation and not-found errors for rerun_job.
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"job_id": ""}, "job_id is required"},
		{map[string]any{"job_id": "t_missing"}, "not found"},
	} {
		res := f.mcpTool(t, runToken, "rerun_job", tc.args)
		if !res.IsError || !strings.Contains(res.Content[0].Text, tc.want) {
			t.Fatalf("rerun_job %+v = %+v, want %q", tc.args, res, tc.want)
		}
	}

	// Create a second job in owner/other to test get_stats repo filtering.
	_ = decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, runToken, "run_job", map[string]any{
		"repo":   "owner/other",
		"prompt": "Other repo task",
	}))

	// get_stats works with read scope, defaults to 14 days, and supports days & repo filters.
	defaultStats := decodeMCPToolJSON[tickets.Stats](t, f.mcpTool(t, readToken, "get_stats", nil))
	if defaultStats.Days != 14 || defaultStats.Repo != "" || defaultStats.Jobs.Total != 2 {
		t.Fatalf("defaultStats = %+v", defaultStats)
	}

	repoStats := decodeMCPToolJSON[tickets.Stats](t, f.mcpTool(t, readToken, "get_stats", map[string]any{"days": 7, "repo": "owner/repo"}))
	if repoStats.Days != 7 || repoStats.Repo != "owner/repo" || repoStats.Jobs.Total != 1 {
		t.Fatalf("repoStats = %+v", repoStats)
	}

	thirtyStats := decodeMCPToolJSON[tickets.Stats](t, f.mcpTool(t, readToken, "get_stats", map[string]any{"days": 30}))
	if thirtyStats.Days != 30 || thirtyStats.Jobs.Total != 2 {
		t.Fatalf("thirtyStats = %+v", thirtyStats)
	}

	for _, badDays := range []int{0, -5, 91} {
		res := f.mcpTool(t, readToken, "get_stats", map[string]any{"days": badDays})
		if !res.IsError || !strings.Contains(res.Content[0].Text, "days") {
			t.Fatalf("get_stats days=%d = %+v, want error", badDays, res)
		}
	}
}

func TestMCPRunJobSupportsPresetVars(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	regionDefault := "us-east-1"
	f.server.factory.Presets["deploy"] = factory.Preset{
		Name:        "deploy",
		Description: "Deploy service.",
		Inputs: map[string]factory.Input{
			"environment": {Description: "Target environment"},
			"region":      {Description: "Region", Default: &regionDefault},
		},
		Steps: []runner.Step{{Name: "deploy", Run: "true"}},
	}
	f.server.schedule.Automations = []factory.Automation{
		{Name: "nightly", Repo: "owner/repo", Preset: "default", Prompt: "Check"},
	}
	runToken := f.newScopedToken(t, "grokbot", tickets.ScopeRun)

	// Valid custom job with preset inputs applies defaults and persists vars.
	created := decodeMCPToolJSON[mcpRunJobResult](t, f.mcpTool(t, runToken, "run_job", map[string]any{
		"repo":   "owner/repo",
		"preset": "deploy",
		"prompt": "Deploy staging",
		"vars":   map[string]string{"environment": "staging"},
	}))
	if !created.Created || created.Vars["environment"] != "staging" || created.Vars["region"] != "us-east-1" || len(created.Vars) != 2 {
		t.Fatalf("created = %+v", created)
	}
	got := decodeMCPToolJSON[mcpJobDetail](t, f.mcpTool(t, runToken, "get_job", map[string]any{"id": created.ID}))
	if got.Vars["environment"] != "staging" || got.Vars["region"] != "us-east-1" {
		t.Fatalf("get_job vars = %+v", got.Vars)
	}

	// Missing required var, unknown var, reserved repo in vars, and vars with automation all return tool errors.
	for name, args := range map[string]map[string]any{
		"missing required": {"repo": "owner/repo", "preset": "deploy", "prompt": "Deploy"},
		"unknown var":      {"repo": "owner/repo", "preset": "deploy", "prompt": "Deploy", "vars": map[string]string{"environment": "staging", "bogus": "1"}},
		"reserved repo":    {"repo": "owner/repo", "preset": "deploy", "prompt": "Deploy", "vars": map[string]string{"repo": "owner/other", "environment": "staging"}},
		"with automation":  {"automation": "nightly", "vars": map[string]string{"environment": "staging"}},
	} {
		res := f.mcpTool(t, runToken, "run_job", args)
		if !res.IsError {
			t.Fatalf("%s: expected tool error, got %+v", name, res)
		}
	}
}
