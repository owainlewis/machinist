// Package agent translates Claude ACP messages into a small provider-neutral stream.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}
type Request struct {
	Directory, SessionID, Prompt, SystemPrompt, Model string
	ReadOnly                                          bool
	MCPServers                                        []MCPServer
}
type Event struct{ Kind, Text, Title, ID, Status string }
type Option struct {
	ID   string `json:"optionId"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}
type Permission struct {
	ID, Title, Tool string
	Options         []Option
}
type Claude struct {
	Command string
	Args    []string
	Remote  bool
}

// Default uses the explicitly installed, pinned runtime. It never installs software.
func Default(root string) *Claude {
	if executable, err := exec.LookPath("claude-agent-acp"); err == nil {
		return &Claude{Command: executable}
	}
	return &Claude{Command: "node", Args: []string{filepath.Join(root, "internal/agent/runtime/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js")}}
}

type frame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}
type wire struct {
	input      io.Writer
	mu         sync.Mutex
	next       int
	frames     <-chan frame
	failures   <-chan error
	emit       func(Event)
	permission func(context.Context, Permission) (bool, error)
	replay     bool
}

func (w *wire) send(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return json.NewEncoder(w.input).Encode(v)
}
func (w *wire) notify(method string, params any) {
	_ = w.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (w *wire) call(ctx context.Context, method string, params any, result any) error {
	w.next++
	id := w.next
	if err := w.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-w.failures:
			return err
		case f := <-w.frames:
			if f.Method != "" {
				if err := w.handle(ctx, f); err != nil {
					return err
				}
				continue
			}
			var got int
			_ = json.Unmarshal(f.ID, &got)
			if got != id {
				continue
			}
			if f.Error != nil {
				return fmt.Errorf("Claude %s: %s", method, f.Error.Message)
			}
			if result != nil {
				return json.Unmarshal(f.Result, result)
			}
			return nil
		}
	}
}
func (w *wire) handle(ctx context.Context, f frame) error {
	switch f.Method {
	case "session/update":
		if w.replay {
			return nil
		}
		var p struct {
			Update struct {
				Kind                      string          `json:"sessionUpdate"`
				Content                   json.RawMessage `json:"content"`
				ToolCallID, Title, Status string
			} `json:"update"`
		}
		if err := json.Unmarshal(f.Params, &p); err != nil {
			return err
		}
		u := p.Update
		switch u.Kind {
		case "agent_message_chunk":
			var content struct{ Type, Text string }
			if err := json.Unmarshal(u.Content, &content); err != nil {
				return err
			}
			if content.Type == "text" {
				w.emit(Event{Kind: "text", Text: content.Text})
			}
		case "agent_thought_chunk":
			w.emit(Event{Kind: "activity", Title: "Thinking"})
		case "tool_call", "tool_call_update":
			w.emit(Event{Kind: "activity", ID: u.ToolCallID, Title: u.Title, Status: u.Status})
		}
	case "session/request_permission":
		var p struct {
			ToolCall struct{ ToolCallID, Title, Kind string }
			Options  []Option
		}
		if err := json.Unmarshal(f.Params, &p); err != nil {
			return err
		}
		allowed := false
		if w.permission != nil {
			var err error
			allowed, err = w.permission(ctx, Permission{ID: p.ToolCall.ToolCallID, Title: p.ToolCall.Title, Tool: p.ToolCall.Kind, Options: p.Options})
			if err != nil {
				return err
			}
		}
		selected := ""
		want := "reject_once"
		if allowed {
			want = "allow_once"
		}
		for _, o := range p.Options {
			if o.Kind == want {
				selected = o.ID
				break
			}
		}
		outcome := map[string]any{"outcome": "cancelled"}
		if selected != "" {
			outcome = map[string]any{"outcome": "selected", "optionId": selected}
		}
		return w.send(map[string]any{"jsonrpc": "2.0", "id": f.ID, "result": map[string]any{"outcome": outcome}})
	default:
		if len(f.ID) > 0 {
			return w.send(map[string]any{"jsonrpc": "2.0", "id": f.ID, "error": map[string]any{"code": -32601, "message": "Client method not supported"}})
		}
	}
	return nil
}

// Run performs one turn. Session identity survives process exit and can be loaded
// for the next turn. Cancellation sends ACP cancel then terminates the adapter.
func (c *Claude) Run(ctx context.Context, r Request, emit func(Event), permission func(context.Context, Permission) (bool, error)) (sessionID string, err error) {
	if emit == nil {
		emit = func(Event) {}
	}
	if r.Directory == "" || r.Prompt == "" {
		return r.SessionID, errors.New("directory and prompt are required")
	}
	cmd := exec.Command(c.Command, c.Args...)
	configureProcess(cmd)
	if !c.Remote {
		cmd.Dir = r.Directory
	}
	cmd.Env = os.Environ()
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		return r.SessionID, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return r.SessionID, err
	}
	if err = cmd.Start(); err != nil {
		return r.SessionID, fmt.Errorf("start Claude ACP (install its pinned runtime first): %w", err)
	}
	defer func() { _ = input.Close(); stopProcess(cmd); _ = cmd.Wait() }()
	frames := make(chan frame, 64)
	failures := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		s := bufio.NewScanner(output)
		s.Buffer(make([]byte, 4096), 4<<20)
		for s.Scan() {
			var f frame
			if e := json.Unmarshal(s.Bytes(), &f); e != nil {
				failures <- fmt.Errorf("invalid ACP frame: %w", e)
				return
			}
			select {
			case frames <- f:
			case <-done:
				return
			}
		}
		e := s.Err()
		if e == nil {
			e = io.EOF
		}
		failures <- e
	}()
	w := &wire{input: input, frames: frames, failures: failures, emit: emit, permission: permission}
	var init struct{ AgentCapabilities struct{ LoadSession bool } }
	if err = w.call(ctx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]any{"name": "machinist", "version": "1"}}, &init); err != nil {
		return r.SessionID, err
	}
	servers := make([]any, 0, len(r.MCPServers))
	for _, s := range r.MCPServers {
		env := []any{}
		for k, v := range s.Env {
			env = append(env, map[string]string{"name": k, "value": v})
		}
		servers = append(servers, map[string]any{"name": s.Name, "command": s.Command, "args": s.Args, "env": env})
	}
	params := map[string]any{"cwd": r.Directory, "mcpServers": servers}
	if r.SystemPrompt != "" {
		params["_meta"] = map[string]any{"systemPrompt": map[string]string{"append": r.SystemPrompt}}
	}
	sessionID = r.SessionID
	if sessionID != "" {
		if !init.AgentCapabilities.LoadSession {
			return sessionID, errors.New("Claude adapter does not support session reload")
		}
		params["sessionId"] = sessionID
		w.replay = true
		err = w.call(ctx, "session/load", params, nil)
		w.replay = false
	} else {
		var created struct{ SessionID string }
		err = w.call(ctx, "session/new", params, &created)
		sessionID = created.SessionID
	}
	if err != nil {
		return sessionID, err
	}
	if sessionID == "" {
		return "", errors.New("Claude returned no session identity")
	}
	emit(Event{Kind: "session", ID: sessionID})
	mode := "default"
	if r.ReadOnly {
		mode = "plan"
	}
	if err = w.call(ctx, "session/set_mode", map[string]any{"sessionId": sessionID, "modeId": mode}, nil); err != nil {
		return sessionID, err
	}
	if r.Model != "" {
		if err = w.call(ctx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": r.Model}, nil); err != nil {
			return sessionID, err
		}
	}
	cancelled := make(chan struct{})
	defer close(cancelled)
	go func() {
		select {
		case <-ctx.Done():
			w.notify("session/cancel", map[string]any{"sessionId": sessionID})
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-timer.C:
				stopProcess(cmd)
			case <-cancelled:
			}
		case <-cancelled:
		}
	}()
	var response struct{ StopReason string }
	err = w.call(ctx, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []any{map[string]string{"type": "text", "text": r.Prompt}}}, &response)
	if err == nil {
		emit(Event{Kind: "complete", Status: response.StopReason})
	}
	return sessionID, err
}
