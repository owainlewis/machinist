package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/ship"
	"github.com/spf13/cobra"
)

// newMergeCommand hands the job's pull request to the control plane, which
// merges it when the rules allow. The rules are code, so no agent decides
// whether a change merges.
func newMergeCommand(options *commandOptions) *cobra.Command {
	var withoutReview bool
	var requireGate bool
	command := &cobra.Command{
		Use:   "merge",
		Short: "Have the control plane merge the job's pull request once its checks pass, inside a step",
		Long: "Hand the open pull request on $MACHINIST_BRANCH to the control plane, with the verdict in review.json and optional merge-gate.json, and end " +
			"at once, so the runner does not wait on CI. The control plane merges it when github.auto_merge is on, the " +
			"review accepted the change, the merge gate approved it (when present), and every check passed. result.md says " +
			"whether it will merge or stays open, and why.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			control, err := controlDirectory("merge")
			if err != nil {
				return err
			}
			return ship.Merge(command.Context(), ship.MergeOptions{
				Control:       control,
				Branch:        os.Getenv("MACHINIST_BRANCH"),
				AutoMerge:     os.Getenv("MACHINIST_AUTO_MERGE") == "true",
				WithoutReview: withoutReview,
				RequireGate:   requireGate,
				GH:            runGH,
				Log:           options.stdout,
			})
		},
	}
	command.Flags().BoolVar(&withoutReview, "without-review", false, "merge when there is no review.json, for presets with no review step")
	command.Flags().BoolVar(&requireGate, "require-gate", false, "require a valid merge-gate.json before auto-merging")
	return command
}

// newPushCommand pushes the job's branch. A push GitHub rejects fails the step
// with GitHub's answer, so a job whose work never reached GitHub fails.
func newPushCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Push the job's branch to origin, inside a step",
		Long: "Push HEAD to origin and track it. When the push fails, the step fails and says what GitHub answered, " +
			"and the commits beyond origin/HEAD are saved as " + protocol.PatchFile + " in the control directory, " +
			"which the job page links to, for git am. A branch with merge commits is saved as one commit of " +
			"everything it changed, so a merge's conflict resolution is kept.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			control, err := controlDirectory("push")
			if err != nil {
				return err
			}
			return ship.Push(command.Context(), ship.PushOptions{
				Git:   runGit,
				Log:   options.stdout,
				Patch: filepath.Join(control, protocol.PatchFile),
				Base:  "origin/HEAD",
			})
		},
	}
}

// newVerdictCommand prints the review's verdict, so a script step can act on
// it. It fails when review.json is missing or not valid.
func newVerdictCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "verdict",
		Short: "Print the verdict in review.json, inside a step",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			control, err := controlDirectory("verdict")
			if err != nil {
				return err
			}
			review, err := ship.ReadReview(control)
			if err != nil {
				return err
			}
			fmt.Fprintln(options.stdout, review.Verdict)
			return nil
		},
	}
}

// newGateCommand validates merge-gate.json and prints its decision, so a
// script step fails immediately when the merge-gate agent wrote no valid verdict.
// When --tripwire is set, deterministic code checks on the git diff can force
// human_review (high risk) regardless of what the LLM judge returned.
func newGateCommand(options *commandOptions) *cobra.Command {
	var tripwire bool
	var base string
	command := &cobra.Command{
		Use:   "gate",
		Short: "Validate and print the decision in merge-gate.json, inside a step",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			control, err := controlDirectory("gate")
			if err != nil {
				return err
			}
			gate, err := ship.ReadGate(control)
			if err != nil {
				return err
			}
			if tripwire {
				findings, err := ship.CheckTripwire(command.Context(), runGit, base)
				if err != nil {
					return err
				}
				gate, err = ship.ApplyTripwire(control, gate, findings)
				if err != nil {
					return err
				}
			}
			fmt.Fprintf(options.stdout, "%s (%s): %s\n", gate.Decision, gate.Risk, gate.Reason)
			return nil
		},
	}
	command.Flags().BoolVar(&tripwire, "tripwire", false, "enforce deterministic tripwire checks on the git diff")
	command.Flags().StringVar(&base, "base", "origin/HEAD", "base ref for tripwire diff checks")
	return command
}

func controlDirectory(command string) (string, error) {
	control := os.Getenv("MACHINIST_DIR")
	if control == "" {
		return "", errors.New("machinist " + command + " runs inside a step, where MACHINIST_DIR is set")
	}
	return control, nil
}

func runGit(ctx context.Context, stdout io.Writer, args ...string) (string, error) {
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, "git", args...)
	command.Stdout = stdout
	command.Stderr = &stderr
	err := command.Run()
	return stderr.String(), err
}

func runGH(ctx context.Context, args ...string) ([]byte, string, error) {
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, "gh", args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.String(), err
}
