package runner

import (
	"fmt"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

const (
	HarnessClaude = "claude"
	HarnessCodex  = "codex"
)

// HarnessCommand returns the fixed command for a harness. These flags turn off
// the harness's own permission checks, so the OS user or Job is the boundary.
func HarnessCommand(harness, model string, timeout time.Duration) (config.ResolvedCommand, error) {
	command := config.ResolvedCommand{Name: harness, Executor: harness, Model: model, Timeout: timeout}
	switch harness {
	case HarnessClaude:
		command.Adapter = "claude-stream"
		command.Command = []string{"claude", "--print", "--verbose", "--output-format", "stream-json", "--include-partial-messages", "--dangerously-skip-permissions"}
	case HarnessCodex:
		command.Adapter = "codex-app-server"
		command.Command = []string{"codex", "app-server", "--stdio", "-c", "features.multi_agent=true", "-c", `sandbox_mode="danger-full-access"`}
	default:
		return config.ResolvedCommand{}, fmt.Errorf("unknown harness %q", harness)
	}
	return command, nil
}
