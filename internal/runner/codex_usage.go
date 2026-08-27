package runner

import (
	"bytes"
	"encoding/json"
	"math"
	"path/filepath"
	"slices"
	"strings"
)

const maxCodexEventBytes = 1 << 20

type codexUsageCollector struct {
	buffer     []byte
	discarding bool
	usage      *int64
}

func newCodexUsageCollector(executor string, command []string) *codexUsageCollector {
	execIndex := slices.Index(command, "exec")
	if execIndex < 1 || !slices.Contains(command[execIndex+1:], "--json") {
		return nil
	}
	if !codexExecutorName(executor) && codexExecutableName(command[execIndex-1]) != "codex" {
		return nil
	}
	return &codexUsageCollector{}
}

func codexExecutorName(executor string) bool {
	parts := strings.FieldsFunc(strings.ToLower(executor), func(character rune) bool {
		return character == '-' || character == '_' || character == '.'
	})
	return slices.Contains(parts, "codex")
}

func codexExecutableName(command string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(command)), ".exe")
}

func (collector *codexUsageCollector) Write(data []byte) (int, error) {
	remaining := data
	for len(remaining) > 0 {
		newline := bytes.IndexByte(remaining, '\n')
		if newline < 0 {
			collector.appendFragment(remaining)
			break
		}
		collector.appendFragment(remaining[:newline])
		collector.completeLine()
		remaining = remaining[newline+1:]
	}
	return len(data), nil
}

func (collector *codexUsageCollector) tokenUsage() *int64 {
	if !collector.discarding && len(collector.buffer) > 0 {
		collector.parseLine(collector.buffer)
		collector.buffer = nil
	}
	if collector.usage == nil {
		return nil
	}
	value := *collector.usage
	return &value
}

func (collector *codexUsageCollector) appendFragment(fragment []byte) {
	if collector.discarding {
		return
	}
	if len(collector.buffer)+len(fragment) > maxCodexEventBytes {
		collector.buffer = nil
		collector.discarding = true
		return
	}
	collector.buffer = append(collector.buffer, fragment...)
}

func (collector *codexUsageCollector) completeLine() {
	if !collector.discarding {
		collector.parseLine(collector.buffer)
	}
	collector.buffer = collector.buffer[:0]
	collector.discarding = false
}

func (collector *codexUsageCollector) parseLine(line []byte) {
	var event struct {
		Type  string          `json:"type"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		if isCompletedTurnCandidate(line) {
			collector.usage = nil
		}
		return
	}
	if event.Type != "turn.completed" {
		return
	}
	collector.usage = nil
	var usage struct {
		Input  *int64 `json:"input_tokens"`
		Output *int64 `json:"output_tokens"`
	}
	if err := json.Unmarshal(event.Usage, &usage); err != nil {
		return
	}
	if usage.Input == nil || usage.Output == nil || *usage.Input < 0 || *usage.Output < 0 || *usage.Input > math.MaxInt64-*usage.Output {
		return
	}
	total := *usage.Input + *usage.Output
	collector.usage = &total
}

func isCompletedTurnCandidate(line []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(line))
	token, err := decoder.Token()
	delimiter, ok := token.(json.Delim)
	if err != nil || !ok || delimiter != '{' {
		return false
	}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok {
			return false
		}
		if key == "type" {
			token, err = decoder.Token()
			value, ok := token.(string)
			return err == nil && ok && value == "turn.completed"
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	return false
}
