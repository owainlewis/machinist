package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

// Activity is an observation owned by a primary run, never a schedulable job.
type Activity struct {
	NativeID        string `json:"native_id"`
	Role            string `json:"role,omitempty"`
	Name            string `json:"name,omitempty"`
	Status          string `json:"status"`
	Detail          string `json:"detail,omitempty"`
	Summary         string `json:"summary,omitempty"`
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
}

type nativeStream struct {
	mu               sync.Mutex
	adapter          string
	log              *eventLog
	stdin            io.WriteCloser
	buffer           []byte
	dropping         bool
	root             string
	turn             string
	model            string
	cwd              string
	prompt           string
	final            string
	completed        bool
	usage            *int64
	cost             *float64
	credentialSource string
	active           map[string]Activity
	toolRoles        map[string]string
	roleRequests     map[int]string
	nextRoleRequest  int
}

func nativeCommand(command config.ResolvedCommand) ([]string, error) {
	argv := append([]string(nil), command.Command...)
	if len(argv) == 0 {
		return nil, errors.New("native executor command is empty")
	}
	switch command.Adapter {
	case "claude-stream":
		info, ok := claudeCommandInfo(command.Executor, argv)
		if !ok {
			return nil, errors.New("claude-stream requires a Claude print command")
		}
		if info.hasOutputFormat && info.outputFormat != "stream-json" {
			return nil, errors.New("claude-stream requires stream-json output")
		}
		argv = append(structuredClaudeCommand(command.Executor, argv), "--json-schema", nativeResultSchema)
		if command.Model != "" {
			argv = append(argv, "--model", command.Model)
		}
	case "codex-app-server":
		// app-server flags remain configurable; model travels in thread/start.
		found := false
		for _, arg := range argv {
			if arg == "app-server" {
				found = true
			}
		}
		if !found {
			return nil, errors.New("codex-app-server requires an app-server command")
		}
	}
	return argv, nil
}

func (n *nativeStream) send(id int, method string, params any) error {
	message := map[string]any{"method": method, "params": params}
	if id != 0 {
		message["id"] = id
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = n.stdin.Write(append(body, '\n'))
	return err
}
func (n *nativeStream) start() error {
	return n.send(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "machinist", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}})
}
func (n *nativeStream) Write(data []byte) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, b := range data {
		if b == '\n' {
			if !n.dropping {
				if err := n.line(n.buffer); err != nil {
					return 0, err
				}
			}
			n.buffer = n.buffer[:0]
			n.dropping = false
		} else if !n.dropping {
			if len(n.buffer) >= maxStructuredEventBytes {
				n.buffer = nil
				n.dropping = true
				_ = n.log.append("native.diagnostic", "", "native event exceeded 1 MiB; raw output retained")
			} else {
				n.buffer = append(n.buffer, b)
			}
		}
	}
	return len(data), nil
}
func (n *nativeStream) observe(a Activity) error {
	if a.NativeID == "" {
		return nil
	}
	if n.active == nil {
		n.active = map[string]Activity{}
	}
	a.NativeID = truncateNative(a.NativeID, 512)
	a.Name = truncateNative(a.Name, 2048)
	a.Summary = truncateNative(a.Summary, 8<<10)
	a.Detail = truncateNative(a.Detail, 2048)
	a.ParentToolUseID = truncateNative(a.ParentToolUseID, 512)
	a.Status = truncateNative(a.Status, 64)
	previous := n.active[a.NativeID]
	if a.Role == "" {
		a.Role = previous.Role
	}
	a.Role = truncateNative(a.Role, 256)
	if previous.Name != "" {
		a.Name = previous.Name
	} else if a.Name == "" {
		a.Name = previous.Name
	}
	if a.ParentToolUseID == "" {
		a.ParentToolUseID = previous.ParentToolUseID
	}
	n.active[a.NativeID] = a
	body, _ := json.Marshal(a)
	return n.log.append("agent.activity", "", string(body))
}
func (n *nativeStream) line(line []byte) error {
	var e map[string]json.RawMessage
	if json.Unmarshal(line, &e) != nil {
		return nil
	}
	// Raw lines are already spooled by the process pump, including unknown events.
	str := func(key string) string { var s string; _ = json.Unmarshal(e[key], &s); return s }
	if n.adapter == "claude-stream" {
		typ, sub := str("type"), str("subtype")
		if typ == "system" && sub == "init" {
			n.credentialSource = str("apiKeySource")
		}
		if typ == "system" && (sub == "task_started" || sub == "task_progress" || sub == "task_notification") {
			id := str("task_id")
			if id == "" {
				id = str("agent_id")
			}
			if sub == "task_started" && str("task_type") != "local_agent" {
				return nil
			}
			if sub != "task_started" {
				if _, known := n.active[id]; !known {
					return nil
				}
			}
			status := "running"
			if sub == "task_notification" {
				status = str("status")
				if status == "" {
					status = "unknown"
				}
			}
			name, detail := str("description"), ""
			if sub != "task_started" {
				detail = name
				name = ""
			}
			return n.observe(Activity{NativeID: id, Role: n.toolRoles[str("tool_use_id")], Name: name, Detail: detail, Status: status, Summary: str("summary"), ParentToolUseID: str("tool_use_id")})
		}
		if typ == "assistant" {
			var message struct {
				Content []struct {
					Type  string `json:"type"`
					Text  string `json:"text"`
					Name  string `json:"name"`
					ID    string `json:"id"`
					Input struct {
						SubagentType string `json:"subagent_type"`
					} `json:"input"`
				} `json:"content"`
			}
			_ = json.Unmarshal(e["message"], &message)
			for _, part := range message.Content {
				if part.Type == "tool_use" && (part.Name == "Agent" || part.Name == "Task") && part.Input.SubagentType != "" {
					if n.toolRoles == nil {
						n.toolRoles = map[string]string{}
					}
					n.toolRoles[part.ID] = part.Input.SubagentType
				}
				if part.Type == "text" {
					parent := str("parent_tool_use_id")
					id := str("agent_id")
					if id == "" && parent != "" {
						for key, a := range n.active {
							if a.ParentToolUseID == parent {
								id = key
								break
							}
						}
					}
					if err := n.message(part.Text, id, parent == "" && str("agent_id") == ""); err != nil {
						return err
					}
				}
			}
		}
		if typ == "result" && str("parent_tool_use_id") == "" {
			n.final = str("result")
			var failed bool
			_ = json.Unmarshal(e["is_error"], &failed)
			if failed {
				return fmt.Errorf("Claude primary failed: %s", n.final)
			}
			raw := e["structured_output"]
			if len(raw) == 0 || string(raw) == "null" {
				return errors.New("Claude primary completed without a structured report")
			}
			n.final = string(raw)
			var cost float64
			if json.Unmarshal(e["total_cost_usd"], &cost) == nil {
				n.cost = &cost
			}
			n.completed = true
		}
		return nil
	}
	if len(e["method"]) > 0 && len(e["id"]) > 0 {
		body, _ := json.Marshal(map[string]any{"id": e["id"], "error": map[string]any{"code": -32601, "message": "Unattended Machinist runs cannot answer client approval or input requests"}})
		_, err := n.stdin.Write(append(body, '\n'))
		return err
	}
	var response struct {
		ID     int             `json:"id"`
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
		Method string          `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
			Item     struct {
				Type          string `json:"type"`
				Kind          string `json:"kind"`
				AgentThreadID string `json:"agentThreadId"`
				AgentPath     string `json:"agentPath"`
				Text          string `json:"text"`
			} `json:"item"`
			Turn struct {
				Status string `json:"status"`
			} `json:"turn"`
			TokenUsage struct {
				Total struct {
					TotalTokens int64 `json:"totalTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return nil
	}
	if id, ok := n.roleRequests[response.ID]; ok {
		delete(n.roleRequests, response.ID)
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return n.log.append("native.diagnostic", "", "Child role metadata unavailable")
		}
		var metadata struct {
			Thread struct {
				AgentRole string `json:"agentRole"`
			} `json:"thread"`
		}
		if json.Unmarshal(response.Result, &metadata) == nil && metadata.Thread.AgentRole != "" {
			if a, known := n.active[id]; known {
				a.Role = metadata.Thread.AgentRole
				return n.observe(a)
			}
		}
		return nil
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return fmt.Errorf("Codex RPC %d: %s", response.ID, response.Error)
	}
	switch response.ID {
	case 1:
		if err := n.send(0, "initialized", map[string]any{}); err != nil {
			return err
		}
		return n.send(2, "account/read", map[string]bool{"refreshToken": false})
	case 2:
		var account struct {
			Account *struct {
				Type string `json:"type"`
			} `json:"account"`
		}
		_ = json.Unmarshal(response.Result, &account)
		n.credentialSource = "none"
		if account.Account != nil {
			n.credentialSource = account.Account.Type
		}
		return n.send(3, "model/list", map[string]any{"limit": 100})
	case 3:
		var models struct {
			Data []struct {
				ID        string `json:"id"`
				Model     string `json:"model"`
				IsDefault bool   `json:"isDefault"`
			} `json:"data"`
		}
		_ = json.Unmarshal(response.Result, &models)
		if n.model == "" {
			for _, m := range models.Data {
				if m.IsDefault {
					n.model = m.Model
					if n.model == "" {
						n.model = m.ID
					}
					break
				}
			}
			if n.model == "" {
				return errors.New("Codex returned no default model; configure an account-supported model for the primary command")
			}
			if err := n.log.append("native.model", "", n.model); err != nil {
				return err
			}
		}
		if n.model != "" {
			found := false
			for _, m := range models.Data {
				if m.ID == n.model || m.Model == n.model {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("Codex model %q unavailable for signed-in account", n.model)
			}
		}
		params := map[string]any{"cwd": n.cwd, "approvalPolicy": "never"}
		if n.model != "" {
			params["model"] = n.model
		}
		return n.send(4, "thread/start", params)
	case 4:
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		_ = json.Unmarshal(response.Result, &result)
		n.root = result.Thread.ID
		if n.root == "" {
			return errors.New("Codex thread/start returned no thread ID")
		}
		return n.send(5, "turn/start", map[string]any{"threadId": n.root, "input": []map[string]string{{"type": "text", "text": n.prompt}}})
	}
	if response.Method == "configWarning" {
		var warning struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(e["params"], &warning)
		if strings.Contains(warning.Summary, "until the project is trusted") {
			return fmt.Errorf("Codex repository configuration was not loaded: %s", warning.Summary)
		}
	}
	p := response.Params
	if p.Item.Type == "subAgentActivity" && p.Item.AgentThreadID != n.root {
		status := "unknown"
		switch p.Item.Kind {
		case "started":
			status = "running"
		case "completed":
			status = "completed"
		case "failed":
			status = "failed"
		case "interrupted":
			status = "interrupted"
		}
		if status == "unknown" {
			return nil
		}
		// item/completed is the end of a notification, not the child lifecycle.
		if response.Method == "item/started" {
			_, known := n.active[p.Item.AgentThreadID]
			if err := n.observe(Activity{NativeID: p.Item.AgentThreadID, Name: p.Item.AgentPath, Status: status}); err != nil {
				return err
			}
			if !known && status == "running" {
				if n.roleRequests == nil {
					n.roleRequests = map[int]string{}
				}
				if n.nextRoleRequest < 100 {
					n.nextRoleRequest = 100
				}
				n.nextRoleRequest++
				n.roleRequests[n.nextRoleRequest] = p.Item.AgentThreadID
				return n.send(n.nextRoleRequest, "thread/read", map[string]any{"threadId": p.Item.AgentThreadID, "includeTurns": false})
			}
			return nil
		}
	}
	if response.Method == "item/completed" && p.Item.Type == "agentMessage" {
		if err := n.message(p.Item.Text, p.ThreadID, p.ThreadID == n.root && n.root != ""); err != nil {
			return err
		}
	}
	if p.ThreadID != n.root || n.root == "" {
		return nil
	}
	if response.Method == "item/completed" && p.Item.Type == "agentMessage" {
		n.final = p.Item.Text
	}
	if response.Method == "thread/tokenUsage/updated" {
		v := p.TokenUsage.Total.TotalTokens
		if v >= 0 {
			n.usage = &v
		}
	}
	if response.Method == "turn/started" {
		var params struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(e["params"], &params)
		n.turn = params.Turn.ID
	}
	if response.Method == "turn/completed" {
		if p.Turn.Status != "completed" {
			return fmt.Errorf("Codex primary turn %s", p.Turn.Status)
		}
		n.completed = true
		return n.stdin.Close()
	}
	return nil
}
func (n *nativeStream) finish(interrupted bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	var parseErr error
	if len(bytes.TrimSpace(n.buffer)) > 0 && !n.dropping {
		parseErr = n.line(n.buffer)
		if parseErr != nil {
			interrupted = true
		}
		n.buffer = nil
	}
	for _, a := range n.active {
		if a.Status == "running" {
			if interrupted {
				a.Status = "interrupted"
			} else {
				a.Status = "unknown"
			}
			if err := n.observe(a); err != nil {
				return errors.Join(parseErr, err)
			}
		}
	}
	return parseErr
}
func (n *nativeStream) lastMessage() string { return strings.TrimSpace(n.final) }

func (n *nativeStream) message(text, id string, primary bool) error {
	if text == "" {
		return nil
	}
	text = truncateNative(text, 8<<10)
	id = truncateNative(id, 512)
	body, _ := json.Marshal(map[string]any{"text": text, "native_id": id, "primary": primary})
	return n.log.append("agent.message", "", string(body))
}

func truncateNative(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "[truncated]"
}

// Best effort protocol interruption precedes the shared runner's process-group
// termination. A blocked stream must never delay cancellation indefinitely.
func (n *nativeStream) BeforeProcessStop() {
	if n == nil || n.adapter != "codex-app-server" || !n.mu.TryLock() {
		return
	}
	defer n.mu.Unlock()
	if n.root == "" || n.turn == "" {
		return
	}
	pipe, ok := n.stdin.(*os.File)
	if !ok {
		return
	}
	_ = pipe.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	_ = n.send(6, "turn/interrupt", map[string]string{"threadId": n.root, "turnId": n.turn})
	_ = n.log.append("native.interrupt", "", "primary interruption requested")
}
