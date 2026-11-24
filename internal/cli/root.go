package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/updater"
	"github.com/spf13/cobra"
)

type commandOptions struct {
	configPath string
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	version    string
}

func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	options := &commandOptions{stdin: stdin, stdout: stdout, stderr: stderr, version: version}
	root := newRootCommand(options)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var outcome *runner.OutcomeError
	if errors.As(err, &outcome) {
		fmt.Fprintf(stderr, "machinist: %s\n", outcome.Error())
		return outcome.ExitCode
	}
	var runtime *runner.RuntimeError
	if errors.As(err, &runtime) {
		fmt.Fprintf(stderr, "machinist: %s\n", runtime.Error())
		return 1
	}
	fmt.Fprintf(stderr, "machinist: %s\n", err)
	return 2
}

func newRootCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{
		Use:           "machinist",
		Short:         "Assign work to coding agents and track every run",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.PersistentFlags().StringVar(&options.configPath, "config", "", "configuration file (default config.yaml or runner.yaml in the current directory)")
	root.AddCommand(newInitCommand(options))
	root.AddCommand(newFactoryCommand(options))
	root.AddCommand(newStartCommand(options))
	root.AddCommand(newRunnerCommand(options))
	root.AddCommand(newTokenCommand(options))
	root.AddCommand(newUpdateCommand(options))
	root.AddCommand(newKubernetesCommand(options))
	root.AddCommand(newSandboxCommand(options))
	root.AddCommand(newAgentLogCommand(options))
	root.AddCommand(newAgentCommand(options))
	root.AddCommand(newPushCommand(options))
	root.AddCommand(newMergeCommand(options))
	root.AddCommand(newVerdictCommand(options))
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the Machinist version",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Fprintln(options.stdout, options.version)
		},
	})
	return root
}

func newUpdateCommand(options *commandOptions) *cobra.Command {
	var requestedVersion string
	var channel string
	var nightly bool
	update := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Update Machinist to a released or nightly version",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			selectedChannel := channel
			if nightly {
				if command.Flags().Changed("channel") && strings.ToLower(strings.TrimSpace(channel)) != updater.ChannelNightly {
					return fmt.Errorf("cannot combine --nightly with --channel=%s", channel)
				}
				selectedChannel = updater.ChannelNightly
			}
			if requestedVersion != "" && (nightly || (command.Flags().Changed("channel") && strings.ToLower(strings.TrimSpace(channel)) != updater.ChannelStable)) {
				return errors.New("cannot combine --version with --nightly or a non-stable --channel")
			}
			result, err := updater.Update(command.Context(), updater.Options{
				Version: requestedVersion,
				Channel: selectedChannel,
				Current: options.version,
			})
			if err != nil {
				return err
			}
			if result.AlreadyCurrent {
				fmt.Fprintf(options.stdout, "machinist %s is already installed\n", result.Version)
				return nil
			}
			fmt.Fprintf(options.stdout, "updated machinist to %s\n", result.Version)
			return nil
		},
	}
	update.Flags().StringVar(&requestedVersion, "version", "", "release version to install, such as v0.5.0 (default latest)")
	update.Flags().StringVar(&channel, "channel", updater.ChannelStable, "release channel to install: stable or nightly")
	update.Flags().BoolVar(&nightly, "nightly", false, "install the latest nightly pre-release (shorthand for --channel nightly)")
	return update
}
