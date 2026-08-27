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
	execIndex := codexExecIndex(executor, command)
	if execIndex < 1 || !slices.Contains(command[execIndex+1:], "--json") {
		return nil
	}
	return &codexUsageCollector{}
}

func structuredCodexCommand(executor string, command []string) []string {
	execIndex := codexExecIndex(executor, command)
	if execIndex < 1 || slices.Contains(command[execIndex+1:], "--json") {
		return command
	}
	structured := make([]string, 0, len(command)+1)
	structured = append(structured, command[:execIndex+1]...)
	structured = append(structured, "--json")
	return append(structured, command[execIndex+1:]...)
}

func codexExecIndex(executor string, command []string) int {
	programIndex := wrappedProgramIndex(command)
	if programIndex < 0 || (codexExecutableName(command[programIndex]) != "codex" && !codexExecutorName(executor)) {
		return -1
	}
	return codexExecIndexAfter(command, programIndex)
}

func wrappedProgramIndex(command []string) int {
	if len(command) == 0 {
		return -1
	}
	switch codexExecutableName(command[0]) {
	case "env":
		return envProgramIndex(command)
	case "mise":
		return miseProgramIndex(command)
	}
	return 0
}

func miseProgramIndex(command []string) int {
	for index := 1; index < len(command); index++ {
		argument := command[index]
		recognized, takesNextValue := miseGlobalOption(argument)
		if recognized {
			if takesNextValue {
				index++
			}
			continue
		}
		if argument != "exec" && argument != "x" {
			return -1
		}
		for commandIndex := index + 1; commandIndex < len(command); commandIndex++ {
			if command[commandIndex] == "--" && commandIndex+1 < len(command) {
				return commandIndex + 1
			}
		}
		return -1
	}
	return -1
}

func miseGlobalOption(argument string) (bool, bool) {
	for _, option := range []string{"--cd", "--env", "--jobs", "--output"} {
		if argument == option {
			return true, true
		}
		if strings.HasPrefix(argument, option+"=") {
			return true, false
		}
	}
	if slices.Contains([]string{"--quiet", "--verbose", "--yes", "--raw", "--locked", "--silent", "--no-config", "--no-env", "--no-hooks", "--help"}, argument) {
		return true, false
	}
	if len(argument) >= 2 && argument[0] == '-' && argument[1] != '-' {
		for index, option := range argument[1:] {
			if strings.ContainsRune("qvyh", option) {
				continue
			}
			if strings.ContainsRune("CEj", option) {
				if index+2 < len(argument) {
					return true, false
				}
				return true, true
			}
			return false, false
		}
		return true, false
	}
	return false, false
}

func codexExecIndexAfter(command []string, programIndex int) int {
	for index := programIndex + 1; index < len(command); index++ {
		argument := command[index]
		recognized, takesNextValue := codexRootOption(argument)
		if recognized {
			if takesNextValue {
				index++
			}
			continue
		}
		if strings.HasPrefix(argument, "-") {
			return -1
		}
		if argument == "exec" {
			return index
		}
		return -1
	}
	return -1
}

func envProgramIndex(command []string) int {
	for index := 1; index < len(command); index++ {
		argument := command[index]
		if argument == "--" {
			if index+1 < len(command) {
				return index + 1
			}
			return -1
		}
		if strings.Contains(argument, "=") && !strings.HasPrefix(argument, "-") {
			continue
		}
		if argument == "-" || envBooleanShortOptions(argument) || slices.Contains([]string{"--ignore-environment", "--null", "--debug", "--block-signal", "--default-signal", "--ignore-signal", "--list-signal-handling"}, argument) {
			continue
		}
		if slices.Contains([]string{"-u", "--unset", "-C", "--chdir", "-P", "-S", "--split-string", "-a", "--argv0"}, argument) {
			index++
			continue
		}
		if strings.HasPrefix(argument, "--unset=") || strings.HasPrefix(argument, "--chdir=") || strings.HasPrefix(argument, "--split-string=") || strings.HasPrefix(argument, "--argv0=") ||
			strings.HasPrefix(argument, "--block-signal=") || strings.HasPrefix(argument, "--default-signal=") || strings.HasPrefix(argument, "--ignore-signal=") ||
			(len(argument) > 2 && slices.Contains([]string{"-u", "-C", "-P", "-S", "-a"}, argument[:2])) {
			continue
		}
		if strings.HasPrefix(argument, "-") {
			return -1
		}
		return index
	}
	return -1
}

func envBooleanShortOptions(argument string) bool {
	if len(argument) < 2 || argument[0] != '-' || argument[1] == '-' {
		return false
	}
	for _, option := range argument[1:] {
		if !strings.ContainsRune("iv0", option) {
			return false
		}
	}
	return true
}

func codexRootOption(argument string) (bool, bool) {
	for _, option := range []string{"-c", "--config", "--enable", "--disable", "--remote", "--remote-auth-token-env", "-i", "--image", "-m", "--model", "--local-provider", "-p", "--profile", "-s", "--sandbox", "-C", "--cd", "--add-dir", "-a", "--ask-for-approval"} {
		if argument == option {
			return true, true
		}
		if strings.HasPrefix(argument, option+"=") || (len(option) == 2 && strings.HasPrefix(argument, option) && len(argument) > len(option)) {
			return true, false
		}
	}
	for _, option := range []string{"--strict-config", "--oss", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", "--search", "--no-alt-screen", "-h", "--help", "-V", "--version"} {
		if argument == option {
			return true, false
		}
	}
	return false, false
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
