package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestFactoryToolsScopedBridge(t *testing.T) {
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.URL.Path != "/api/factory/tools/create_task" || r.Header.Get("Authorization") != "Bearer scoped" {
			t.Errorf("unexpected scoped call: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"title":"Feature","brief":"Implement feature"}` {
			t.Errorf("unexpected arguments %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"task_1"}`)
	}))
	defer server.Close()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_task","arguments":{"title":"Feature","brief":"Implement feature"}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"approve","arguments":{}}}
`)
	var output bytes.Buffer
	if err := serveFactoryTools(context.Background(), input, &output, server.URL, "scoped"); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var replies []map[string]any
	for decoder.More() {
		var reply map[string]any
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		replies = append(replies, reply)
	}
	if len(replies) != 4 || called != 1 {
		t.Fatalf("replies=%d calls=%d", len(replies), called)
	}
	if replies[3]["error"] == nil {
		t.Fatal("human approval tool must be unavailable")
	}
}

func TestFactoryToolSchemasHaveValidRequiredArrays(t *testing.T) {
	for _, tool := range factoryTools() {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]json.RawMessage
		if err = json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if string(schema["required"]) == "null" {
			t.Fatalf("invalid schema for %s: required must be an array", tool.Name)
		}
	}
}

func TestFactoryToolsRequireCredentials(t *testing.T) {
	for _, address := range []string{"", "file:///tmp", "http://"} {
		if serveFactoryTools(context.Background(), strings.NewReader(""), io.Discard, address, "scoped") == nil {
			t.Errorf("accepted %q", address)
		}
	}
	if serveFactoryTools(context.Background(), strings.NewReader(""), io.Discard, "http://127.0.0.1:7331", "") == nil {
		t.Fatal("accepted missing scoped token")
	}
}

func TestFactoryToolFailureIsVisible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "approval required", http.StatusConflict) }))
	defer server.Close()
	body, failed := callFactoryTool(context.Background(), server.Client(), server.URL, "scoped", "start_step", json.RawMessage(`{"task_id":"task_1"}`))
	if !failed || !strings.Contains(body, "approval required") {
		t.Fatalf("failure hidden: %q %v", body, failed)
	}
}

type factoryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f factoryRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFactoryBridgeWaitsForRemoteOperations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := http.DefaultTransport
		defer func() { http.DefaultTransport = original }()
		http.DefaultTransport = factoryRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			select {
			case <-time.After(135 * time.Second):
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"task":{"id":"remote-task"}}`))}, nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		})
		var output bytes.Buffer
		input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"link_pr","arguments":{"task_id":"remote-task","pr_url":"https://github.com/example/project/pull/1"}}}`)
		if err := serveFactoryTools(context.Background(), input, &output, "http://localhost:7331", "scoped"); err != nil {
			t.Fatal(err)
		}
		var reply struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(output.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Result.IsError || len(reply.Result.Content) != 1 || !strings.Contains(reply.Result.Content[0].Text, "remote-task") {
			t.Fatalf("legitimate remote result lost: %s", output.String())
		}
	})
}
