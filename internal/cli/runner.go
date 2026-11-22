package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/owainlewis/machinist/internal/agentrunner"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/ghclient"
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
			runnerConfig, executor, err := loadRunner(command.Context(), options)
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
		Short: "Run one step that reaches a repository, to prove this runner works",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			runnerConfig, executor, err := loadRunner(command.Context(), options)
			if err != nil {
				return err
			}
			return agentrunner.Check(command.Context(), executor, runnerConfig, repository, options.stdout)
		},
	}
	check.Flags().StringVar(&repository, "repo", "", "repository to reach, as owner/name")
	_ = check.MarkFlagRequired("repo")
	root.AddCommand(check)
	return root
}

func loadRunner(ctx context.Context, options *commandOptions) (config.Runner, agentrunner.Executor, error) {
	runnerConfig, err := config.LoadRunner(options.configPath)
	if err != nil {
		return config.Runner{}, nil, err
	}
	if runnerConfig.Kubernetes != nil {
		executor, err := agentrunner.NewKubernetes(runnerConfig)
		return runnerConfig, executor, err
	}
	executor, err := agentrunner.NewLocal(runnerConfig)
	if err == nil {
		// A failed scope lookup must not prevent the runner from taking work.
		if scopes, scopeErr := ghclient.New().TokenScopes(ctx); scopeErr == nil {
			warnAboutTokenScopes(options.stderr, scopes)
		}
	}
	return runnerConfig, executor, err
}

// warnAboutTokenScopes warns about a classic token's scopes. GitHub App and
// fine-grained tokens report no scopes, so they get no warning.
func warnAboutTokenScopes(stderr io.Writer, scopes []string) {
	if len(scopes) == 0 {
		return
	}
	var extra []string
	hasWorkflow := false
	for _, scope := range scopes {
		switch scope {
		// Classic tokens need repo for private repositories, or public_repo for
		// public ones, and workflow to push changes to .github/workflows/.
		case "repo", "public_repo":
		case "workflow":
			hasWorkflow = true
		default:
			extra = append(extra, scope)
		}
	}
	if len(extra) > 0 {
		fmt.Fprintf(stderr, "machinist: warning: the GitHub token has extra scopes: %s; use a token limited to the repositories the bot works on\n", strings.Join(extra, ", "))
	}
	if !hasWorkflow {
		fmt.Fprintln(stderr, "machinist: warning: the GitHub token has no workflow scope, so jobs cannot push changes to .github/workflows/; run gh auth refresh -s workflow")
	}
}
