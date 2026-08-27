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
	execIndex := codexExecIndex(command)
	if execIndex < 1 || !slices.Contains(command[execIndex+1:], "--json") {
		return nil
	}
	if !recognizedCodexCommand(executor, command[:execIndex]) {
		return nil
	}
	return &codexUsageCollector{}
}

func structuredCodexCommand(executor string, command []string) []string {
	execIndex := codexExecIndex(command)
	if execIndex < 1 || !recognizedCodexCommand(executor, command[:execIndex]) || slices.Contains(command[execIndex+1:], "--json") {
		return command
	}
	structured := make([]string, 0, len(command)+1)
	structured = append(structured, command[:execIndex+1]...)
	structured = append(structured, "--json")
	return append(structured, command[execIndex+1:]...)
}

func codexExecIndex(command []string) int {
	for index := 1; index < len(command); index++ {
		argument := command[index]
		if codexRootOptionTakesValue(argument) {
			index++
			continue
		}
		if argument == "exec" {
			return index
		}
	}
	return -1
}

func codexRootOptionTakesValue(argument string) bool {
	for _, option := range []string{"-c", "--config", "--enable", "--disable", "--remote", "--remote-auth-token-env", "-i", "--image", "-m", "--model", "--local-provider", "-p", "--profile", "-s", "--sandbox", "-C", "--cd", "--add-dir", "-a", "--ask-for-approval"} {
		if argument == option {
			return true
		}
	}
	return false
}

func recognizedCodexCommand(executor string, commandPrefix []string) bool {
	return codexExecutorName(executor) || slices.ContainsFunc(commandPrefix, func(argument string) bool {
		return codexExecutableName(argument) == "codex"
	})
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
	if len(fragment) > maxCodexEventBytes-len(collector.buffer) {
		remainingCapacity := maxCodexEventBytes - len(collector.buffer)
		collector.buffer = append(collector.buffer, fragment[:remainingCapacity]...)
		if isCompletedTurnCandidate(collector.buffer) {
			collector.usage = nil
		}
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
