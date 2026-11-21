package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/spf13/cobra"
)

// newAgentCommand runs one of the factory's agents inside a step. A preset
// step with agent: <name> runs it.
func newAgentCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "agent <name>",
		Short: "Run one of the factory's agents inside a step",
		Long: "Run the agent in $MACHINIST_DIR/agents/<name>.md on the job's prompt. Its system prompt is where it is working, " +
			"then prompts/rules.md if there is one, then the agent file's prompt. Progress prints as readable lines, " +
			"and the tokens and cost the agent reports are recorded. It exits with the agent's exit code.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			control := os.Getenv("MACHINIST_DIR")
			if control == "" {
				return errors.New("machinist agent runs inside a step, where MACHINIST_DIR is set")
			}
			system, task, agent, err := agentTurn(control, args[0])
			if err != nil {
				return err
			}
			argv, stdin := agent.Command(system, task)
			code, err := harness.Run(command.Context(), agent.Format(), argv, strings.NewReader(stdin), options.stdout, options.stderr, filepath.Join(control, harness.UsageFile))
			if err != nil {
				return fmt.Errorf("agent %s: %w", agent.Name, err)
			}
			if code != 0 {
				return &runner.OutcomeError{State: runner.State(fmt.Sprintf("agent %s exited with status %d", agent.Name, code)), ExitCode: code}
			}
			return nil
		},
	}
}

// agentTurn reads the agent file and builds its system prompt and task.
func agentTurn(control, name string) (system, task string, agent harness.Agent, err error) {
	body, err := os.ReadFile(filepath.Join(control, "agents", name+".md"))
	if err != nil {
		return "", "", harness.Agent{}, fmt.Errorf("agent %s is not in the factory's agents/: %w", name, err)
	}
	agent, err = harness.ParseAgent(name, string(body))
	if err != nil {
		return "", "", harness.Agent{}, fmt.Errorf("agents/%s.md: %w", name, err)
	}
	parts := []string{fmt.Sprintf("You are working in a clone of %s on branch %s.\nThe control directory is %s. Files named below without a path are there.",
		os.Getenv("MACHINIST_REPO"), os.Getenv("MACHINIST_BRANCH"), control)}
	if rules, err := os.ReadFile(filepath.Join(control, "prompts", "rules.md")); err == nil && strings.TrimSpace(string(rules)) != "" {
		parts = append(parts, strings.TrimSpace(string(rules)))
	}
	parts = append(parts, agent.Prompt)
	prompt, err := os.ReadFile(filepath.Join(control, "prompt.md"))
	if err != nil {
		return "", "", harness.Agent{}, fmt.Errorf("read the job's prompt: %w", err)
	}
	return strings.Join(parts, "\n\n"), string(prompt), agent, nil
}
