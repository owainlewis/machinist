package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

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
	root.PersistentFlags().StringVar(&options.configPath, "config", "", "configuration file")
	root.AddCommand(newInitCommand(options))
	root.AddCommand(newStartCommand(options))
	root.AddCommand(newRunnerCommand(options))
	root.AddCommand(newTokenCommand(options))
	root.AddCommand(newUpdateCommand(options))
	root.AddCommand(newKubernetesCommand(options))
	root.AddCommand(newSandboxCommand(options))
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
	update := &cobra.Command{
		Use:   "update",
		Short: "Update Machinist to a released version",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := updater.Update(command.Context(), updater.Options{
				Version: requestedVersion,
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
	update.Flags().StringVar(&requestedVersion, "version", "", "release version to install, such as v0.2.0 (default latest)")
	return update
}
