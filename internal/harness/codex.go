package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Codex runs OpenAI's Codex CLI with codex exec and reads its --json events.
// Codex has no separate system prompt, so the instructions come first in the
// message, then the task.
type Codex struct{}

func (Codex) Name() string { return "codex" }

func (Codex) Command(request Request) ([]string, string) {
	args := []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox"}
	if request.Model != "" {
		args = append(args, "--model", request.Model)
	}
	args = append(args, "-")
	return args, request.System + "\n\n# The task\n\n" + request.Prompt
}

type codexEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Item    struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Command  string `json:"command"`
		ExitCode *int   `json:"exit_code"`
		Status   string `json:"status"`
		Query    string `json:"query"`
		Changes  []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
		Server string `json:"server"`
		Tool   string `json:"tool"`
	} `json:"item"`
	Usage struct {
		Input       int64 `json:"input_tokens"`
		CachedInput int64 `json:"cached_input_tokens"`
		Output      int64 `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

var codexEvents = map[string]bool{"thread.started": true, "turn.started": true, "turn.completed": true, "turn.failed": true, "item.started": true, "item.updated": true, "item.completed": true, "error": true}

// Format copies Codex's JSON events from r to w as readable lines. Codex
// reports tokens but not cost, so the usage has no cost.
func (Codex) Format(r io.Reader, w io.Writer, cwd string) (*Usage, error) {
	var usage *Usage
	turns := 0
	err := eachLine(r, w, func(line string) ([]string, bool) {
		var e codexEvent
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &e) != nil || !codexEvents[e.Type] {
			return nil, false
		}
		switch e.Type {
		case "thread.started":
			return []string{"Codex started"}, true
		case "turn.completed":
			turns++
			if usage == nil {
				usage = &Usage{CostUnknown: true}
			}
			usage.Tokens += e.Usage.Input + e.Usage.Output
			return []string{fmt.Sprintf("Finished turn %d", turns)}, true
		case "turn.failed":
			return []string{"Stopped: " + firstLine(e.Error.Message, 200)}, true
		case "error":
			return []string{"Error: " + firstLine(e.Message, 200)}, true
		case "item.started":
			if e.Item.Type == "command_execution" {
				return []string{ActionPrefix + "Run " + firstLine(e.Item.Command, 160)}, true
			}
		case "item.completed":
			switch e.Item.Type {
			case "agent_message":
				if text := strings.TrimSpace(e.Item.Text); text != "" {
					return []string{text}, true
				}
			case "command_execution":
				if e.Item.ExitCode != nil && *e.Item.ExitCode != 0 {
					return []string{fmt.Sprintf("  failed with status %d", *e.Item.ExitCode)}, true
				}
			case "file_change":
				lines := make([]string, 0, len(e.Item.Changes))
				for _, change := range e.Item.Changes {
					lines = append(lines, ActionPrefix+"Edit "+relative(cwd, change.Path))
				}
				return lines, true
			case "web_search":
				return []string{ActionPrefix + "Search the web for " + firstLine(e.Item.Query, 120)}, true
			case "mcp_tool_call":
				return []string{ActionPrefix + "Use " + e.Item.Server + " " + e.Item.Tool}, true
			case "error":
				return []string{"Error: " + firstLine(e.Item.Text, 200)}, true
			}
		}
		return nil, true
	})
	return usage, err
}
