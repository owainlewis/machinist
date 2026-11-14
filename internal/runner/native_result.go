package runner

import (
	"strings"

	"github.com/owainlewis/machinist/internal/protocol"
)

// Claude's structured-output channel is separate from its human-readable result.
const nativeResultSchema = `{"type":"object","additionalProperties":false,"required":["outcome","summary"],"properties":{"outcome":{"type":"string","enum":["pr_ready","needs_input","no_change","failed"]},"summary":{"type":"string","minLength":1},"question":{"type":"string"}}}`

func parseNativeResult(message string) (*protocol.Report, error) {
	text := strings.TrimSpace(message)
	if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "\n```") {
		text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "\n```")
	}
	return protocol.ParseReport([]byte(text))
}
