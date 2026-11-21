package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Claude runs Claude Code headless and reads its stream-json output.
type Claude struct{}

func (Claude) Name() string { return "claude" }

func (Claude) Command(request Request) ([]string, string) {
	args := []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions", "--append-system-prompt", request.System}
	if request.Model != "" {
		args = append(args, "--model", request.Model)
	}
	return args, request.Prompt
}

func (Claude) Format(r io.Reader, w io.Writer, cwd string) (*Usage, error) { return Format(r, w, cwd) }

// claudeEvents are the event types Claude Code's stream-json writes. A JSON
// line of any other type is not Claude Code's, so it passes through.
var claudeEvents = map[string]bool{"system": true, "assistant": true, "user": true, "result": true}

type event struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Message struct {
		Content []block `json:"content"`
	} `json:"message"`
	IsError    bool    `json:"is_error"`
	NumTurns   int     `json:"num_turns"`
	DurationMS int64   `json:"duration_ms"`
	TotalCost  float64 `json:"total_cost_usd"`
	Usage      struct {
		Input         int64 `json:"input_tokens"`
		Output        int64 `json:"output_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

type block struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   map[string]any  `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

// Format copies Claude Code's stream-json output from r to w as readable
// lines. cwd shortens file paths in actions.
func Format(r io.Reader, w io.Writer, cwd string) (*Usage, error) {
	var usage *Usage
	err := eachLine(r, w, func(line string) ([]string, bool) {
		var parsed event
		if !strings.HasPrefix(line, `{"type":`) || json.Unmarshal([]byte(line), &parsed) != nil || !claudeEvents[parsed.Type] {
			return nil, false
		}
		if parsed.Type == "result" {
			u := parsed.Usage
			usage = &Usage{Tokens: u.Input + u.Output + u.CacheCreation + u.CacheRead, Cost: parsed.TotalCost}
		}
		return describe(parsed, cwd), true
	})
	return usage, err
}

func describe(e event, cwd string) []string {
	switch e.Type {
	case "system":
		if e.Subtype == "init" && e.Model != "" {
			return []string{"Claude Code started with " + e.Model}
		}
	case "assistant":
		var lines []string
		for _, b := range e.Message.Content {
			switch b.Type {
			case "text":
				if text := strings.TrimSpace(b.Text); text != "" {
					lines = append(lines, text)
				}
			case "tool_use":
				lines = append(lines, ActionPrefix+action(b, cwd))
			}
		}
		return lines
	case "user":
		var lines []string
		for _, b := range e.Message.Content {
			if b.Type == "tool_result" && b.IsError {
				lines = append(lines, "  failed: "+firstLine(resultText(b.Content), 200))
			}
		}
		return lines
	case "result":
		summary := fmt.Sprintf("Finished after %d turns in %s", e.NumTurns, (time.Duration(e.DurationMS) * time.Millisecond).Round(time.Second))
		if e.TotalCost > 0 {
			summary += fmt.Sprintf(", $%.2f", e.TotalCost)
		}
		if e.IsError {
			summary = "Stopped (" + e.Subtype + ") after " + strings.TrimPrefix(summary, "Finished after ")
		}
		return []string{summary}
	}
	return nil
}

// action names one tool call in a few words: the command it ran, the file it
// touched or what it searched for.
func action(b block, cwd string) string {
	text := func(key string) string {
		value, _ := b.Input[key].(string)
		return value
	}
	path := func() string {
		file := text("file_path")
		if file == "" {
			file = text("notebook_path")
		}
		return relative(cwd, file)
	}
	switch b.Name {
	case "Bash":
		return "Run " + firstLine(text("command"), 160)
	case "Read":
		return "Read " + path()
	case "Edit", "MultiEdit":
		return "Edit " + path()
	case "Write":
		return "Write " + path()
	case "NotebookEdit":
		return "Edit " + path()
	case "Grep":
		return "Search for " + firstLine(text("pattern"), 120)
	case "Glob":
		return "Find " + firstLine(text("pattern"), 120)
	case "WebFetch":
		return "Fetch " + text("url")
	case "WebSearch":
		return "Search the web for " + firstLine(text("query"), 120)
	case "Task", "Agent":
		return "Start a subagent: " + firstLine(text("description"), 120)
	case "TodoWrite":
		return "Update the plan"
	}
	return "Use " + b.Name
}

// resultText reads a tool result, which is a string or a list of text blocks.
func resultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			parts = append(parts, b.Text)
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
