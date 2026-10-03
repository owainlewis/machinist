package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newFactoryCommand(options *commandOptions) *cobra.Command {
	command := &cobra.Command{Use: "factory", Short: "Connect agent tools to the local factory"}
	command.AddCommand(&cobra.Command{Use: "tools", Short: "Serve conversation-scoped factory tools over stdio", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return serveFactoryTools(cmd.Context(), options.stdin, options.stdout, os.Getenv("MACHINIST_FACTORY_URL"), os.Getenv("MACHINIST_FACTORY_TOKEN"))
	}})
	return command
}

type factoryTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func factoryTools() []factoryTool {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	tool := func(name, description string, properties map[string]any, required ...string) factoryTool {
		if required == nil {
			required = []string{}
		}
		return factoryTool{name, description, map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
	}
	return []factoryTool{
		tool("create_task", "Create a task in this project. Inspect existing tasks first to avoid duplicate work.", map[string]any{"title": str(), "brief": str(), "pipeline": str()}, "title", "brief"),
		tool("inspect_tasks", "Read this project's tasks, current steps, and saved results.", map[string]any{}),
		tool("start_step", "Start the next eligible agent or script step. Human approval cannot be skipped.", map[string]any{"task_id": str()}, "task_id"),
		tool("send_message", "Send a user answer or correction to this task's existing worker conversation.", map[string]any{"task_id": str(), "message": str(), "request_id": str()}, "task_id", "message", "request_id"),
		tool("cancel_task", "Request cancellation of this project's task. Preserve its workspace and history.", map[string]any{"task_id": str()}, "task_id"),
		tool("link_pr", "Link the pull request published from a human-approved revision. Only the assigned delivery worker may call this; it cannot approve or merge.", map[string]any{"task_id": str(), "pr_url": str()}, "task_id", "pr_url"),
		tool("report", "Save the current step result. Use a stable unique report_id. Outcome is complete, blocked, or changes. Planning reports include design; build reports include revision and pr_url when available.", map[string]any{"task_id": str(), "report_id": str(), "summary": str(), "outcome": map[string]any{"type": "string", "enum": []string{"complete", "blocked", "changes"}}, "design": str(), "revision": str(), "pr_url": str()}, "task_id", "report_id", "summary", "outcome"),
	}
}

func serveFactoryTools(ctx context.Context, input io.Reader, output io.Writer, address, token string) error {
	parsed, err := url.Parse(address)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || token == "" {
		return errors.New("factory tools require MACHINIST_FACTORY_URL and a scoped MACHINIST_FACTORY_TOKEN")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	client := &http.Client{Timeout: 45 * time.Second}
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return fmt.Errorf("decode tool request: %w", err)
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		var rpcError any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "machinist", "version": "1"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": factoryTools()}
		case "tools/call":
			var call struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(request.Params, &call); err != nil {
				rpcError = map[string]any{"code": -32602, "message": "invalid tool arguments"}
				break
			}
			known := false
			for _, tool := range factoryTools() {
				if tool.Name == call.Name {
					known = true
				}
			}
			if !known {
				rpcError = map[string]any{"code": -32602, "message": "unknown factory tool"}
				break
			}
			if len(call.Arguments) == 0 {
				call.Arguments = json.RawMessage(`{}`)
			}
			body, failed := callFactoryTool(ctx, client, strings.TrimRight(address, "/"), token, call.Name, call.Arguments)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": body}}, "isError": failed}
		default:
			rpcError = map[string]any{"code": -32601, "message": "unsupported method"}
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		if rpcError != nil {
			reply["error"] = rpcError
		} else {
			reply["result"] = result
		}
		if err := json.NewEncoder(output).Encode(reply); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func callFactoryTool(ctx context.Context, client *http.Client, address, token, name string, args json.RawMessage) (string, bool) {
	request, err := http.NewRequestWithContext(ctx, "POST", address+"/api/factory/tools/"+name, bytes.NewReader(args))
	if err != nil {
		return err.Error(), true
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err.Error(), true
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return err.Error(), true
	}
	if len(body) > 1<<20 {
		return "factory response exceeded size limit", true
	}
	return string(body), response.StatusCode < 200 || response.StatusCode >= 300
}
