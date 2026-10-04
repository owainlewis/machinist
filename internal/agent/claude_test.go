package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestACPProcess(t *testing.T) {
	if os.Getenv("MACHINIST_FAKE_ACP") != "1" {
		return
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var f frame
		if dec.Decode(&f) != nil {
			os.Exit(0)
		}
		result := any(map[string]any{})
		switch f.Method {
		case "initialize":
			result = map[string]any{"agentCapabilities": map[string]any{"loadSession": true}}
		case "session/new":
			checkExpectedProfile(f.Params)
			result = map[string]string{"sessionId": "saved-session"}
		case "session/load":
			checkExpectedProfile(f.Params)
			enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "old"}}}})
		case "session/set_model":
			os.Exit(9)
		case "session/set_config_option":
			if expected := os.Getenv("MACHINIST_EXPECTED_MODEL"); expected != "" {
				var p struct{ ConfigID, Value string }
				json.Unmarshal(f.Params, &p)
				if p.ConfigID != "model" || p.Value != expected {
					os.Exit(8)
				}
			}
		case "session/prompt":
			enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "tool_call_update", "content": []any{}, "toolCallId": "write", "status": "in_progress"}}})
			enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "hello"}}}})
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 90, "method": "session/request_permission", "params": map[string]any{"toolCall": map[string]string{"toolCallId": "write", "title": "Write file", "kind": "edit", "name": "Write"}, "options": []Option{{ID: "yes", Kind: "allow_once"}, {ID: "no", Kind: "reject_once"}}}})
			var response struct {
				Result struct{ Outcome struct{ OptionID string } }
			}
			dec.Decode(&response)
			if response.Result.Outcome.OptionID != "no" {
				os.Exit(3)
			}
			result = map[string]string{"stopReason": "end_turn"}
		}
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": result})
	}
}
func TestRunStreamsDeniesAndReloads(t *testing.T) {
	t.Setenv("MACHINIST_FAKE_ACP", "1")
	c := Claude{Command: os.Args[0], Args: []string{"-test.run=^TestACPProcess$"}}
	for _, previous := range []string{"", "saved-session"} {
		var text string
		permissions := 0
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		id, err := c.Run(ctx, Request{Directory: t.TempDir(), SessionID: previous, Prompt: "test"}, func(e Event) {
			if e.Kind == "text" {
				text += e.Text
			}
		}, func(_ context.Context, p Permission) (bool, error) {
			permissions++
			if p.Name != "Write" || p.Title != "Write file" || p.Tool != "edit" {
				t.Errorf("permission identity lost: %+v", p)
			}
			return false, nil
		})
		cancel()
		if err != nil || id != "saved-session" || text != "hello" || permissions != 1 {
			t.Fatalf("id=%q text=%q permissions=%d err=%v", id, text, permissions, err)
		}
	}
}

// Opt-in live proof uses installed CLI credentials and an isolated temporary directory.
func TestLiveClaude(t *testing.T) {
	if os.Getenv("MACHINIST_LIVE_CLAUDE") != "1" {
		t.Skip("set MACHINIST_LIVE_CLAUDE=1 for live proof")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	c := Default(root)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	text := ""
	emit := func(e Event) {
		if e.Kind == "text" {
			text += e.Text
		}
		if e.Kind == "activity" {
			t.Log(e.Title)
		}
	}
	id, err := c.Run(ctx, Request{Directory: dir, Prompt: "Reply with the word MACHINIST_OK only. Do not use tools."}, emit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || text == "" {
		t.Fatalf("missing output: %q", text)
	}
	t.Logf("new session %s: %s", id, text)
	text = ""
	_, err = c.Run(ctx, Request{Directory: dir, SessionID: id, Prompt: "What exact word did you just reply with? Do not use tools."}, emit, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reloaded: %s", text)
	count := 0
	_, err = c.Run(ctx, Request{Directory: dir, SessionID: id, Prompt: fmt.Sprintf("Use the Write tool to create %s containing test. Do not use bash or any other tool. If denied, stop.", filepath.Join(dir, "denied.txt"))}, emit, func(_ context.Context, p Permission) (bool, error) {
		count++
		t.Logf("Rejected: %s", p.Title)
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("no permission requested")
	}
	if _, err = os.Stat(filepath.Join(dir, "denied.txt")); !os.IsNotExist(err) {
		t.Fatal("rejected write created file")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { time.Sleep(time.Second); cancel2() }()
	_, err = c.Run(ctx2, Request{Directory: dir, SessionID: id, Prompt: "Think carefully and write a long explanation of Go concurrency without tools."}, emit, nil)
	if err == nil {
		t.Fatal("cancel returned success")
	}
	t.Logf("cancel: %v", err)
}

func TestRemoteDirectoryIsProviderPath(t *testing.T) {
	t.Setenv("MACHINIST_FAKE_ACP", "1")
	c := Claude{Command: os.Args[0], Args: []string{"-test.run=^TestACPProcess$"}, Remote: true}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	id, err := c.Run(ctx, Request{Directory: "/does-not-exist-on-local-host", Prompt: "test"}, nil, nil)
	if err != nil || id != "saved-session" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestPermissionDecodesACPToolNameSeparatelyFromTitle(t *testing.T) {
	var output bytes.Buffer
	var received Permission
	adapter := wire{input: &output, permission: func(_ context.Context, p Permission) (bool, error) { received = p; return true, nil }}
	request := frame{ID: json.RawMessage(`90`), Method: "session/request_permission", Params: json.RawMessage(`{"toolCall":{"toolCallId":"inspect","name":"mcp__machinist__inspect_tasks","title":"Read project tasks","kind":"read"},"options":[{"optionId":"allow","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}]}`)}
	if err := adapter.handle(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if received.Name != "mcp__machinist__inspect_tasks" || received.Title != "Read project tasks" || received.Tool != "read" || received.ID != "inspect" {
		t.Fatal("permission identity conflated with display fields", received)
	}
	var response struct {
		Result struct {
			Outcome struct {
				OptionID string `json:"optionId"`
			}
		}
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.Result.Outcome.OptionID != "allow" {
		t.Fatal("permission response incorrect", output.String(), err)
	}
}

func checkExpectedProfile(raw json.RawMessage) {
	if expected := os.Getenv("MACHINIST_EXPECTED_SYSTEM"); expected != "" {
		var params struct {
			Meta struct{ SystemPrompt struct{ Append string } } `json:"_meta"`
		}
		json.Unmarshal(raw, &params)
		if params.Meta.SystemPrompt.Append != expected {
			os.Exit(7)
		}
	}
}
func TestAcceptedProfileAppliedToNewAndLoadedACP(t *testing.T) {
	t.Setenv("MACHINIST_FAKE_ACP", "1")
	t.Setenv("MACHINIST_EXPECTED_SYSTEM", "Saved coordinator profile")
	c := Claude{Command: os.Args[0], Args: []string{"-test.run=^TestACPProcess$"}}
	for _, model := range []string{"explicit-model", ""} {
		for _, previous := range []string{"", "saved-session"} {
			expected := model
			if expected == "" {
				expected = "default"
			}
			t.Setenv("MACHINIST_EXPECTED_MODEL", expected)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, err := c.Run(ctx, Request{Directory: t.TempDir(), SessionID: previous, Prompt: "Continue", SystemPrompt: "Saved coordinator profile", Model: model}, func(Event) {}, func(context.Context, Permission) (bool, error) { return false, nil })
			cancel()
			if err != nil {
				t.Fatal(model, previous, err)
			}
		}
	}
}
