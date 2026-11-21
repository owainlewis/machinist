package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Agy runs Google's Antigravity CLI in print mode and reads its stream-json
// events. agy has no system prompt flag, so the instructions come first in the
// prompt, then the task, as with Codex. The prompt is the value of --print.
type Agy struct{}

func (Agy) Name() string { return "agy" }

func (Agy) Command(request Request) ([]string, string) {
	args := []string{"agy", "--print", request.System + "\n\n# The task\n\n" + request.Prompt, "--output-format", "stream-json", "--dangerously-skip-permissions"}
	if request.Model != "" {
		args = append(args, "--model", request.Model)
	}
	return args, ""
}

type agyEvent struct {
	Type string `json:"type"`
	// init
	Model string `json:"model"`
	// message
	Role    string `json:"role"`
	Content string `json:"content"`
	Delta   bool   `json:"delta"`
	// tool_use and tool_result
	ToolName   string         `json:"tool_name"`
	ToolID     string         `json:"tool_id"`
	Parameters map[string]any `json:"parameters"`
	Status     string         `json:"status"`
	// error, and a failed result
	Message string `json:"message"`
	Error   any    `json:"error"`
	// result
	Stats *agyStats `json:"stats"`
	Usage *agyStats `json:"usage"`
}

type agyStats struct {
	TotalTokens  int64   `json:"total_tokens"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CachedTokens int64   `json:"cached_tokens"`
	Cost         float64 `json:"cost"`
}

func (s *agyStats) tokens() int64 {
	if s.TotalTokens > 0 {
		return s.TotalTokens
	}
	return s.InputTokens + s.OutputTokens + s.CachedTokens
}

var agyEvents = map[string]bool{"init": true, "message": true, "tool_use": true, "tool_result": true, "error": true, "result": true}

// Format copies agy's stream-json events from r to w as readable lines. The
// assistant's text arrives in pieces, so it is joined and written when the
// next event or the end arrives.
func (Agy) Format(r io.Reader, w io.Writer, cwd string) (*Usage, error) {
	var usage *Usage
	var text strings.Builder
	tools := map[string]string{}
	flush := func(lines []string) []string {
		if said := strings.TrimSpace(text.String()); said != "" {
			lines = append([]string{said}, lines...)
		}
		text.Reset()
		return lines
	}
	err := eachLine(r, w, func(line string) ([]string, bool) {
		var e agyEvent
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &e) != nil || !agyEvents[e.Type] {
			return flush(nil), false
		}
		switch e.Type {
		case "init":
			if e.Model != "" {
				return flush([]string{"Antigravity started with " + e.Model}), true
			}
			return flush([]string{"Antigravity started"}), true
		case "message":
			if e.Role != "assistant" {
				return nil, true
			}
			if !e.Delta && text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(e.Content)
			return nil, true
		case "tool_use":
			tools[e.ToolID] = e.ToolName
			return flush([]string{ActionPrefix + agyAction(e, cwd)}), true
		case "tool_result":
			if e.Status == "error" || e.Status == "failed" {
				name := tools[e.ToolID]
				if name == "" {
					name = e.ToolName
				}
				return flush([]string{"  failed: " + name}), true
			}
			return flush(nil), true
		case "error":
			return flush([]string{"Error: " + firstLine(agyMessage(e), 200)}), true
		case "result":
			lines := []string{}
			if stats := e.Stats; stats != nil || e.Usage != nil {
				if stats == nil {
					stats = e.Usage
				}
				usage = &Usage{Tokens: stats.tokens(), Cost: stats.Cost}
			}
			if e.Status != "" && e.Status != "success" {
				lines = append(lines, "Stopped: "+firstLine(agyMessage(e), 200))
			} else {
				lines = append(lines, "Finished")
			}
			return flush(lines), true
		}
		return nil, true
	})
	if err == nil {
		if said := strings.TrimSpace(text.String()); said != "" {
			_, err = fmt.Fprintln(w, said)
		}
	}
	return usage, err
}

// agyMessage finds the words of an error, which agy gives as a string or as an
// object with a message.
func agyMessage(e agyEvent) string {
	switch value := e.Error.(type) {
	case string:
		return value
	case map[string]any:
		if message, ok := value["message"].(string); ok {
			return message
		}
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Status
}

func agyAction(e agyEvent, cwd string) string {
	text := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := e.Parameters[key].(string); ok && value != "" {
				return value
			}
		}
		return ""
	}
	file := text("file_path", "absolute_path", "path", "TargetFile", "AbsolutePath")
	name := strings.ToLower(e.ToolName)
	switch {
	case text("command", "CommandLine") != "":
		return "Run " + firstLine(text("command", "CommandLine"), 160)
	case strings.Contains(name, "read") || strings.Contains(name, "view"):
		return "Read " + relative(cwd, file)
	case strings.Contains(name, "write") || strings.Contains(name, "create"):
		return "Write " + relative(cwd, file)
	case strings.Contains(name, "edit") || strings.Contains(name, "replace"):
		return "Edit " + relative(cwd, file)
	case text("pattern", "query", "Query") != "":
		return "Search for " + firstLine(text("pattern", "query", "Query"), 120)
	case file != "":
		return "Use " + e.ToolName + " on " + relative(cwd, file)
	}
	return "Use " + e.ToolName
}
