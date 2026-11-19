// Package agentlog turns an agent's machine output into lines a person can
// follow while a run is going. Claude Code's stream-json output becomes one
// line per message or action. Any other line passes through unchanged.
package agentlog

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
	"strings"
	"time"
)

// ActionPrefix starts every line that names an action the agent took. The web
// app shows the latest one as what a running job is doing now.
const ActionPrefix = "▸ "

// maxLine bounds a line of agent output. A tool result can hold a whole file.
const maxLine = 16 << 20

// Usage is what a run reported spending. Tokens counts input, output and
// cache tokens together.
type Usage struct {
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

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

// Format copies r to w, line by line, as it arrives. cwd shortens file paths
// in actions. It returns the usage the agent reported, if it reported any.
func Format(r io.Reader, w io.Writer, cwd string) (*Usage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxLine)
	var usage *Usage
	for scanner.Scan() {
		line := scanner.Text()
		var parsed event
		if !strings.HasPrefix(line, `{"type":`) || json.Unmarshal([]byte(line), &parsed) != nil || !claudeEvents[parsed.Type] {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return usage, err
			}
			continue
		}
		if parsed.Type == "result" {
			u := parsed.Usage
			usage = &Usage{Tokens: u.Input + u.Output + u.CacheCreation + u.CacheRead, Cost: parsed.TotalCost}
		}
		for _, out := range describe(parsed, cwd) {
			if _, err := fmt.Fprintln(w, out); err != nil {
				return usage, err
			}
		}
	}
	return usage, scanner.Err()
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
		if relative, err := filepath.Rel(cwd, file); err == nil && cwd != "" && !strings.HasPrefix(relative, "..") {
			return relative
		}
		return file
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

func firstLine(text string, limit int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	runes := []rune(line)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return line
}

// UsageFile is where Run leaves the usage, in the run's control directory.
// The runner reads it after the step and adds it to the step's result.
const UsageFile = "usage.json"

// Run starts command with stdin, formats its stdout onto stdout, and passes
// its stderr through. It writes the reported usage to usagePath, when the
// agent reported any and usagePath is set, and returns the command's exit
// code.
func Run(ctx context.Context, command []string, stdin io.Reader, stdout, stderr io.Writer, usagePath string) (int, error) {
	if len(command) == 0 {
		return 2, errors.New("name the agent command after --")
	}
	process := exec.CommandContext(ctx, command[0], command[1:]...)
	process.Stdin = stdin
	process.Stderr = stderr
	output, err := process.StdoutPipe()
	if err != nil {
		return 1, err
	}
	if err := process.Start(); err != nil {
		return 127, err
	}
	cwd, _ := os.Getwd()
	usage, formatErr := Format(output, stdout, cwd)
	if formatErr != nil {
		_, _ = io.Copy(io.Discard, output)
	}
	waitErr := process.Wait()
	if usage != nil && usagePath != "" {
		if data, err := json.Marshal(usage); err == nil {
			_ = os.WriteFile(usagePath, data, 0o600)
		}
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		return exit.ExitCode(), nil
	}
	if waitErr != nil {
		return 1, waitErr
	}
	if formatErr != nil {
		return 1, formatErr
	}
	return 0, nil
}
