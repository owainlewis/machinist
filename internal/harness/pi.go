package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Pi runs the Pi coding agent in print mode and reads its --mode json events.
// The task is the last argument, since Pi reads its message from the command
// line.
type Pi struct{}

func (Pi) Name() string { return "pi" }

func (Pi) Command(request Request) ([]string, string) {
	args := []string{"pi", "--print", "--mode", "json", "--no-session", "--append-system-prompt", request.System}
	if request.Model != "" {
		args = append(args, "--model", request.Model)
	}
	return append(args, request.Prompt), ""
}

type piEvent struct {
	Type     string         `json:"type"`
	ToolName string         `json:"toolName"`
	Args     map[string]any `json:"args"`
	IsError  bool           `json:"isError"`
	Message  struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			Input      int64 `json:"input"`
			Output     int64 `json:"output"`
			CacheRead  int64 `json:"cacheRead"`
			CacheWrite int64 `json:"cacheWrite"`
			Cost       struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	} `json:"message"`
}

var piEvents = map[string]bool{"agent_start": true, "agent_end": true, "turn_start": true, "turn_end": true, "message_start": true, "message_update": true, "message_end": true, "tool_execution_start": true, "tool_execution_update": true, "tool_execution_end": true}

// Format copies Pi's JSON events from r to w as readable lines.
func (Pi) Format(r io.Reader, w io.Writer, cwd string) (*Usage, error) {
	var usage *Usage
	turns := 0
	err := eachLine(r, w, cwd, func(line string) ([]string, bool) {
		var e piEvent
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &e) != nil || !piEvents[e.Type] {
			return nil, false
		}
		switch e.Type {
		case "agent_start":
			return []string{"Pi started"}, true
		case "agent_end":
			return []string{fmt.Sprintf("Finished after %d turns", turns)}, true
		case "turn_end":
			turns++
		case "tool_execution_start":
			return []string{ActionPrefix + piAction(e, cwd)}, true
		case "tool_execution_end":
			if e.IsError {
				return []string{"  failed: " + e.ToolName}, true
			}
		case "message_end":
			if e.Message.Role != "assistant" {
				return nil, true
			}
			u := e.Message.Usage
			if usage == nil {
				usage = &Usage{}
			}
			usage.Tokens += u.Input + u.Output + u.CacheRead + u.CacheWrite
			usage.Cost += u.Cost.Total
			var lines []string
			for _, block := range e.Message.Content {
				if text := strings.TrimSpace(block.Text); block.Type == "text" && text != "" {
					lines = append(lines, text)
				}
			}
			if e.Message.StopReason == "error" && e.Message.ErrorMessage != "" {
				lines = append(lines, "Stopped: "+firstLine(e.Message.ErrorMessage, 200))
			}
			return lines, true
		}
		return nil, true
	})
	return usage, err
}

func piAction(e piEvent, cwd string) string {
	text := func(key string) string {
		value, _ := e.Args[key].(string)
		return value
	}
	switch e.ToolName {
	case "bash":
		return "Run " + firstLine(text("command"), 160)
	case "read":
		return "Read " + relative(cwd, text("path"))
	case "edit":
		return "Edit " + relative(cwd, text("path"))
	case "write":
		return "Write " + relative(cwd, text("path"))
	case "grep":
		return "Search for " + firstLine(text("pattern"), 120)
	case "find", "ls":
		return "Find " + firstLine(text("pattern")+text("path"), 120)
	}
	return "Use " + e.ToolName
}
