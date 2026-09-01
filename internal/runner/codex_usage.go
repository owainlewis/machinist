package runner

import (
	"bytes"
	"encoding/json"
	"math"
	"path/filepath"
	"slices"
	"strings"
)

const maxStructuredEventBytes = 1 << 20

type structuredUsageCollector struct {
	buffer     []byte
	discarding bool
	usage      *int64
	resultType string
	cache      bool
}

// newUsageCollector returns a collector for the executor's structured output,
// or nil when the command does not emit a parseable usage event.
func newUsageCollector(executor string, command []string) *structuredUsageCollector {
	if execIndex := codexExecIndex(executor, command); execIndex >= 1 && slices.Contains(command[execIndex+1:], "--json") {
		return &structuredUsageCollector{resultType: "turn.completed"}
	}
	info, ok := claudeCommandInfo(executor, command)
	if !ok || (info.hasOutputFormat && info.outputFormat != "json" && info.outputFormat != "stream-json") {
		return nil
	}
	return &structuredUsageCollector{resultType: "result", cache: true}
}

func structuredCommand(executor string, command []string) []string {
	if codexCommand := structuredCodexCommand(executor, command); !slices.Equal(codexCommand, command) {
		return codexCommand
	}
	return structuredClaudeCommand(executor, command)
}

func structuredClaudeCommand(executor string, command []string) []string {
	info, ok := claudeCommandInfo(executor, command)
	if !ok || info.hasOutputFormat {
		return command
	}
	structured := make([]string, 0, len(command)+3)
	structured = append(structured, command[:info.printIndex+1]...)
	if !info.hasVerbose {
		structured = append(structured, "--verbose")
	}
	structured = append(structured, "--output-format", "stream-json")
	return append(structured, command[info.printIndex+1:]...)
}

type claudeCommand struct {
	printIndex      int
	hasOutputFormat bool
	outputFormat    string
	hasVerbose      bool
}

func claudeCommandInfo(executor string, command []string) (claudeCommand, bool) {
	programIndex := claudeProgramIndex(command)
	if programIndex < 0 || (executableName(command[programIndex]) != "claude" && !executorNamed(executor, "claude")) {
		return claudeCommand{}, false
	}
	return claudeCommandInfoAfter(command, programIndex)
}

func claudeProgramIndex(command []string) int {
	programIndex := wrappedProgramIndex(command)
	if programIndex >= 0 && executableName(command[programIndex]) == "nice" {
		programIndex++
		if programIndex >= len(command) {
			return -1
		}
		if command[programIndex] == "-n" || command[programIndex] == "--adjustment" {
			programIndex += 2
		} else if strings.HasPrefix(command[programIndex], "-n") || strings.HasPrefix(command[programIndex], "--adjustment=") {
			programIndex++
		}
		if programIndex >= len(command) {
			return -1
		}
	}
	return programIndex
}

func claudeCommandInfoAfter(command []string, programIndex int) (claudeCommand, bool) {
	info := claudeCommand{printIndex: -1}
	for index := programIndex + 1; index < len(command); index++ {
		argument := command[index]
		if argument == "--print" || argument == "-p" {
			if info.printIndex >= 0 {
				return claudeCommand{}, false
			}
			info.printIndex = index
			continue
		}
		if argument == "--" || !strings.HasPrefix(argument, "-") {
			return claudeCommand{}, false
		}
		recognized, takesNextValue := claudeRootOption(argument)
		if !recognized {
			return claudeCommand{}, false
		}
		if argument == "--output-format" {
			if index+1 >= len(command) {
				return claudeCommand{}, false
			}
			index++
			info.hasOutputFormat = true
			info.outputFormat = command[index]
		} else if strings.HasPrefix(argument, "--output-format=") {
			info.hasOutputFormat = true
			info.outputFormat = strings.TrimPrefix(argument, "--output-format=")
		} else if argument == "--verbose" {
			info.hasVerbose = true
		} else if claudeOptionalValueOption(argument) {
			if index+1 < len(command) && !strings.HasPrefix(command[index+1], "-") {
				index++
			}
		} else if claudeVariadicOption(argument) {
			valueIndex := index + 1
			for valueIndex < len(command) && !strings.HasPrefix(command[valueIndex], "-") {
				valueIndex++
			}
			if valueIndex == index+1 {
				return claudeCommand{}, false
			}
			index = valueIndex - 1
		} else if takesNextValue {
			if index+1 >= len(command) {
				return claudeCommand{}, false
			}
			index++
		}
	}
	if info.printIndex < 0 {
		return claudeCommand{}, false
	}
	return info, true
}

func claudeRootOption(argument string) (bool, bool) {
	if argument == "--debug" || strings.HasPrefix(argument, "--debug=") {
		return true, false
	}
	if claudeOptionalValueOption(argument) || claudeVariadicOption(argument) {
		return true, false
	}
	valueOptions := []string{
		"--advisor", "--agent", "--agents", "--append-subagent-system-prompt",
		"--append-system-prompt", "--append-system-prompt-file", "--betas", "--debug-file", "--disallowedTools",
		"--dangerously-load-development-channels", "--disallowed-tools", "--effort", "--fallback-model", "--from-pr", "--input-format", "--json-schema", "--max-budget-usd", "--max-turns",
		"--mcp-config", "--model", "--name", "-n", "--output-format", "--permission-mode", "--permission-prompt-tool",
		"--plugin-dir", "--plugin-url", "--session-id", "--settings", "--system-prompt",
		"--system-prompt-file", "--setting-sources", "--teammate-mode", "--tools", "--worktree", "-w",
	}
	for _, option := range valueOptions {
		if argument == option {
			return true, true
		}
		if strings.HasPrefix(argument, option+"=") {
			return true, false
		}
	}
	booleanOptions := []string{
		"--allow-dangerously-skip-permissions", "--ax-screen-reader", "--bare", "--chrome", "--continue", "-c",
		"--dangerously-skip-permissions", "--disable-slash-commands", "--enable-auto-mode", "--exclude-dynamic-system-prompt-sections",
		"--debug", "--fork-session", "--forward-subagent-text", "--ide", "--include-hook-events", "--include-partial-messages", "--init",
		"--init-only", "--maintenance", "--no-chrome", "--no-session-persistence", "--print", "-p",
		"--replay-user-messages", "--restricted", "--safe-mode", "--strict-mcp-config", "--teleport", "--verbose",
	}
	if slices.Contains(booleanOptions, argument) {
		return true, false
	}
	if argument == "--prompt-suggestions" || strings.HasPrefix(argument, "--prompt-suggestions=") {
		return true, false
	}
	return false, false
}

func claudeOptionalValueOption(argument string) bool {
	return argument == "--resume" || argument == "-r"
}

func claudeVariadicOption(argument string) bool {
	variadicOptions := []string{
		"--add-dir", "--allowedTools", "--allowed-tools", "--betas", "--disallowedTools", "--disallowed-tools", "--mcp-config", "--tools",
	}
	return slices.Contains(variadicOptions, argument)
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
	if executorNamed(executor, "codex") {
		for programIndex, argument := range command {
			if executableName(argument) == "codex" {
				if execIndex := codexExecIndexAfter(command, programIndex); execIndex >= 0 {
					return execIndex
				}
			}
		}
	}
	programIndex := wrappedProgramIndex(command)
	if programIndex < 0 || (executableName(command[programIndex]) != "codex" && !executorNamed(executor, "codex")) {
		return -1
	}
	return codexExecIndexAfter(command, programIndex)
}

func wrappedProgramIndex(command []string) int {
	if len(command) == 0 {
		return -1
	}
	programIndex := 0
	for programIndex < len(command) {
		var nestedProgramIndex int
		switch executableName(command[programIndex]) {
		case "env":
			nestedProgramIndex = envProgramIndex(command[programIndex:])
		case "mise":
			nestedProgramIndex = miseProgramIndex(command[programIndex:])
		case "direnv":
			nestedProgramIndex = direnvProgramIndex(command[programIndex:])
		default:
			return programIndex
		}
		if nestedProgramIndex < 1 {
			return -1
		}
		programIndex += nestedProgramIndex
	}
	return -1
}

func direnvProgramIndex(command []string) int {
	if len(command) < 4 || command[1] != "exec" || command[2] == "" || strings.HasPrefix(command[2], "-") {
		return -1
	}
	return 3
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
		if argument == "-" || slices.Contains([]string{"--ignore-environment", "--null", "--debug", "--block-signal", "--default-signal", "--ignore-signal", "--list-signal-handling"}, argument) {
			continue
		}
		if envSplitStringOption(argument) {
			return -1
		}
		if recognized, takesNextValue := envShortOptions(argument); recognized {
			if takesNextValue {
				index++
			}
			continue
		}
		if argument == "--split-string" || strings.HasPrefix(argument, "--split-string=") {
			return -1
		}
		if slices.Contains([]string{"--unset", "--chdir", "--argv0"}, argument) {
			index++
			continue
		}
		if strings.HasPrefix(argument, "--unset=") || strings.HasPrefix(argument, "--chdir=") || strings.HasPrefix(argument, "--argv0=") ||
			strings.HasPrefix(argument, "--block-signal=") || strings.HasPrefix(argument, "--default-signal=") || strings.HasPrefix(argument, "--ignore-signal=") {
			continue
		}
		if strings.HasPrefix(argument, "-") {
			return -1
		}
		return index
	}
	return -1
}

func envSplitStringOption(argument string) bool {
	if len(argument) < 2 || argument[0] != '-' || argument[1] == '-' {
		return false
	}
	for _, option := range argument[1:] {
		if strings.ContainsRune("iv0", option) {
			continue
		}
		return option == 'S'
	}
	return false
}

func envShortOptions(argument string) (bool, bool) {
	if len(argument) < 2 || argument[0] != '-' || argument[1] == '-' {
		return false, false
	}
	for index, option := range argument[1:] {
		if strings.ContainsRune("iv0", option) {
			continue
		}
		if strings.ContainsRune("uCPSa", option) {
			return true, index+2 == len(argument)
		}
		return false, false
	}
	return true, false
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
	for _, option := range []string{"--strict-config", "--oss", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", "--approve-for-me", "--not-so-yolo", "--search", "--no-alt-screen", "-h", "--help", "-V", "--version"} {
		if argument == option {
			return true, false
		}
	}
	return false, false
}

// executorNamed reports whether a configured executor name contains the tool
// name as one dash, underscore, or dot separated word, such as "claude-fast".
func executorNamed(executor, tool string) bool {
	parts := strings.FieldsFunc(strings.ToLower(executor), func(character rune) bool {
		return character == '-' || character == '_' || character == '.'
	})
	return slices.Contains(parts, tool)
}

func executableName(command string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(command)), ".exe")
}

func (collector *structuredUsageCollector) Write(data []byte) (int, error) {
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

func (collector *structuredUsageCollector) tokenUsage() *int64 {
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

func (collector *structuredUsageCollector) appendFragment(fragment []byte) {
	if collector.discarding {
		return
	}
	if len(fragment) > maxStructuredEventBytes-len(collector.buffer) {
		remainingCapacity := maxStructuredEventBytes - len(collector.buffer)
		collector.buffer = append(collector.buffer, fragment[:remainingCapacity]...)
		if isUsageResultCandidate(collector.buffer, collector.resultType) {
			collector.usage = nil
		}
		collector.buffer = nil
		collector.discarding = true
		return
	}
	collector.buffer = append(collector.buffer, fragment...)
}

func (collector *structuredUsageCollector) completeLine() {
	if !collector.discarding {
		collector.parseLine(collector.buffer)
	}
	collector.buffer = collector.buffer[:0]
	collector.discarding = false
}

func (collector *structuredUsageCollector) parseLine(line []byte) {
	var event struct {
		Type  string          `json:"type"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		if isUsageResultCandidate(line, collector.resultType) {
			collector.usage = nil
		}
		return
	}
	if event.Type != collector.resultType {
		return
	}
	collector.usage = nil
	if collector.cache {
		var usage struct {
			Input              *int64 `json:"input_tokens"`
			CacheCreationInput *int64 `json:"cache_creation_input_tokens"`
			CacheReadInput     *int64 `json:"cache_read_input_tokens"`
			Output             *int64 `json:"output_tokens"`
		}
		if err := json.Unmarshal(event.Usage, &usage); err != nil {
			return
		}
		if usage.Input == nil || usage.CacheCreationInput == nil || usage.CacheReadInput == nil || usage.Output == nil ||
			*usage.Input < 0 || *usage.CacheCreationInput < 0 || *usage.CacheReadInput < 0 || *usage.Output < 0 {
			return
		}
		total := *usage.Input
		if total > math.MaxInt64-*usage.CacheCreationInput {
			return
		}
		total += *usage.CacheCreationInput
		if total > math.MaxInt64-*usage.CacheReadInput {
			return
		}
		total += *usage.CacheReadInput
		if total > math.MaxInt64-*usage.Output {
			return
		}
		total += *usage.Output
		collector.usage = &total
		return
	}
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

func isUsageResultCandidate(line []byte, resultType string) bool {
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
			return err == nil && ok && value == resultType
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return hasJSONFieldValue(line, "type", resultType)
		}
	}
	return hasJSONFieldValue(line, "type", resultType)
}

func hasJSONFieldValue(line []byte, field, want string) bool {
	depth := 0
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case '"':
			end, ok := scanJSONString(line, index)
			if !ok {
				return false
			}
			if depth == 1 {
				var key string
				if err := json.Unmarshal(line[index:end+1], &key); err != nil || key != field {
					index = end
					continue
				}
				valueStart := end + 1
				for valueStart < len(line) && isJSONWhitespace(line[valueStart]) {
					valueStart++
				}
				if valueStart >= len(line) || line[valueStart] != ':' {
					index = end
					continue
				}
				valueStart++
				for valueStart < len(line) && isJSONWhitespace(line[valueStart]) {
					valueStart++
				}
				if valueStart >= len(line) || line[valueStart] != '"' {
					index = end
					continue
				}
				valueEnd, ok := scanJSONString(line, valueStart)
				if !ok {
					return false
				}
				var value string
				if err := json.Unmarshal(line[valueStart:valueEnd+1], &value); err == nil && value == want {
					return true
				}
			}
			index = end
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return false
}

func scanJSONString(line []byte, start int) (int, bool) {
	for index := start + 1; index < len(line); index++ {
		if line[index] == '\\' {
			index++
			continue
		}
		if line[index] == '"' {
			return index, true
		}
	}
	return 0, false
}

func isJSONWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}
