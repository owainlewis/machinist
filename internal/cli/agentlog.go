package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/owainlewis/machinist/internal/agentlog"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/spf13/cobra"
)

// newAgentLogCommand runs an agent inside a step so its progress shows while
// it works: machinist agent-log -- claude --print --output-format stream-json.
func newAgentLogCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "agent-log -- <command> [args...]",
		Short: "Run an agent and print its progress as readable lines",
		Long: "Run an agent and print its progress as it works. Claude Code's stream-json output becomes one line per message or action; other output passes through. " +
			"Inside a step, the tokens and cost the agent reports are written to the control directory for the runner to record. It exits with the agent's exit code.",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		RunE: func(command *cobra.Command, args []string) error {
			usagePath := ""
			if directory := os.Getenv("MACHINIST_DIR"); directory != "" {
				usagePath = filepath.Join(directory, agentlog.UsageFile)
			}
			code, err := agentlog.Run(command.Context(), args, options.stdin, options.stdout, options.stderr, usagePath)
			if err != nil {
				return err
			}
			if code != 0 {
				return &runner.OutcomeError{State: runner.State(fmt.Sprintf("agent exited with status %d", code)), ExitCode: code}
			}
			return nil
		},
	}
}
