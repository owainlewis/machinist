package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
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

// agyEvent is one line of agy's stream-json output: an init, a step update or
// the result. Each names its kind in "event", not "type".
type agyEvent struct {
	Event string `json:"event"`
	Init  struct {
		Model string `json:"model"`
	} `json:"init"`
	Step struct {
		Index     int       `json:"step_index"`
		State     string    `json:"state"`
		Type      string    `json:"step_type"`
		TextDelta string    `json:"text_delta"`
		ToolName  string    `json:"tool_name"`
		Error     string    `json:"error"`
		Usage     *agyUsage `json:"usage"`
		ToolInfo  struct {
			Parameters map[string]any `json:"parameters"`
			Output     string         `json:"output"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Result struct {
		Status   string    `json:"status"`
		Error    any       `json:"error"`
		NumTurns int       `json:"num_turns"`
		Usage    *agyUsage `json:"usage"`
	} `json:"result"`
}

// agyUsage is what agy reports spending. total_tokens is input plus output;
// cache reads are counted apart, so they are added here, as for the other
// harnesses. agy reports no cost.
type agyUsage struct {
	TotalTokens     int64 `json:"total_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
}

var agyEvents = map[string]bool{"init": true, "step_update": true, "result": true}

// agyExit finds the exit code in a run_command result, which agy writes as
// "Command exited with code N."
var agyExit = regexp.MustCompile(`exited with code (-?\d+)`)

// Format copies agy's stream-json events from r to w as readable lines. The
// agent's text arrives in pieces across a step's updates, so it is written
// when the step is done.
func (Agy) Format(r io.Reader, w io.Writer, cwd string) (*Usage, error) {
	var usage *Usage
	var model string
	var text strings.Builder
	err := eachLine(r, w, cwd, func(line string) ([]string, bool) {
		var e agyEvent
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &e) != nil || !agyEvents[e.Event] {
			return nil, false
		}
		switch e.Event {
		case "init":
			model = e.Init.Model
			if e.Init.Model != "" {
				return []string{"Antigravity started with " + e.Init.Model}, true
			}
			return []string{"Antigravity started"}, true
		case "step_update":
			step := e.Step
			switch step.Type {
			case "agent_response":
				text.WriteString(step.TextDelta)
				if step.State != "DONE" {
					return nil, true
				}
				said := strings.TrimSpace(text.String())
				text.Reset()
				if said == "" {
					return nil, true
				}
				return []string{said}, true
			case "tool":
				if step.State == "ACTIVE" {
					return []string{ActionPrefix + agyAction(step.ToolName, step.ToolInfo.Parameters, cwd)}, true
				}
				if match := agyExit.FindStringSubmatch(step.ToolInfo.Output); match != nil && match[1] != "0" {
					return []string{"  failed with status " + match[1]}, true
				}
				if step.State != "DONE" && step.State != "" {
					return []string{"  " + strings.ToLower(step.State) + ": " + step.ToolName}, true
				}
			}
			if step.Error != "" {
				return []string{"Error: " + firstLine(step.Error, 200)}, true
			}
			return nil, true
		case "result":
			if e.Result.Usage != nil {
				usage = &Usage{Tokens: e.Result.Usage.TotalTokens + e.Result.Usage.CacheReadTokens, CostUnknown: true}
			}
			if e.Result.Status != "SUCCESS" {
				return []string{"Stopped: " + firstLine(agyError(e), 200)}, true
			}
			return []string{fmt.Sprintf("Finished after %d turns", e.Result.NumTurns)}, true
		}
		return nil, true
	})
	// A run cut off mid-answer still shows what the agent had said.
	if said := strings.TrimSpace(text.String()); err == nil && said != "" {
		_, err = fmt.Fprintln(w, said)
	}
	return withModel(usage, model), err
}

// agyError finds the words of a failed result, which may be a string or an
// object with a message.
func agyError(e agyEvent) string {
	switch value := e.Result.Error.(type) {
	case string:
		if value != "" {
			return value
		}
	case map[string]any:
		if message, ok := value["message"].(string); ok {
			return message
		}
	}
	return strings.ToLower(e.Result.Status)
}

func agyAction(tool string, parameters map[string]any, cwd string) string {
	text := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := parameters[key].(string); ok && value != "" {
				return value
			}
		}
		return ""
	}
	file := text("AbsolutePath", "TargetFile", "File", "file_path", "path")
	switch tool {
	case "run_command":
		return "Run " + firstLine(text("CommandLine", "command"), 160)
	case "view_file":
		return "Read " + relative(cwd, file)
	case "write_to_file":
		return "Write " + relative(cwd, file)
	case "replace_file_content", "multi_replace_file_content", "sed_file":
		return "Edit " + relative(cwd, file)
	case "grep_search", "find_by_name", "search_web":
		return "Search for " + firstLine(text("Query", "Pattern", "SearchPath", "query"), 120)
	case "list_dir":
		return "List " + relative(cwd, text("DirectoryPath", "path"))
	case "read_url_content", "open_browser_url":
		return "Open " + firstLine(text("Url", "url"), 120)
	}
	return "Use " + tool
}
