package cli

import (
	"errors"
	"fmt"
	"net"

	"github.com/owainlewis/machinist/internal/agentrunner"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/spf13/cobra"
)

func newStartCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the Machinist control plane",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			machinistConfig, err := config.LoadConfig(options.configPath)
			if err != nil {
				return err
			}
			loaded, err := factory.Load(machinistConfig.Server.Factory)
			if err != nil {
				return fmt.Errorf("load factory: %w", err)
			}
			store, err := tickets.Open(machinistConfig.Server.Database)
			if err != nil {
				return err
			}
			defer store.Close()
			server, err := controlplane.New(machinistConfig, store, loaded, options.version, options.stderr)
			if err != nil {
				return err
			}
			return server.Serve(command.Context(), func(address net.Addr) {
				fmt.Fprintf(options.stderr, "machinist: control plane listening on %s\n", address)
			})
		},
	}
}

func newRunnerCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{Use: "runner", Short: "Run agents for a control plane"}
	root.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start taking work from the control plane",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			runnerConfig, executor, err := loadLocalRunner(options)
			if err != nil {
				return err
			}
			daemon, err := agentrunner.NewDaemon(runnerConfig, executor, options.stderr)
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stderr, "machinist: runner connecting to %s\n", runnerConfig.ControlPlane)
			return daemon.Run(command.Context())
		},
	})
	var repository string
	check := &cobra.Command{
		Use:   "check",
		Short: "Make one real run per harness to prove this runner works",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			runnerConfig, executor, err := loadLocalRunner(options)
			if err != nil {
				return err
			}
			return agentrunner.Check(command.Context(), executor, runnerConfig, repository, options.stdout)
		},
	}
	check.Flags().StringVar(&repository, "repo", "", "repository to clone, as owner/name")
	_ = check.MarkFlagRequired("repo")
	root.AddCommand(check)
	return root
}

func loadLocalRunner(options *commandOptions) (config.Runner, *agentrunner.Local, error) {
	runnerConfig, err := config.LoadRunner(options.configPath)
	if err != nil {
		return config.Runner{}, nil, err
	}
	if runnerConfig.Kubernetes != nil {
		return config.Runner{}, nil, errors.New("Kubernetes runners are not available yet")
	}
	executor, err := agentrunner.NewLocal(runnerConfig)
	return runnerConfig, executor, err
}
