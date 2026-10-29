package cli

import (
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/kuberunner"
	"github.com/spf13/cobra"
)

func newKubernetesCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{Use: "kubernetes", Short: "Manage the operator's Kubernetes runner"}
	root.AddCommand(&cobra.Command{Use: "install", Short: "Install the private gVisor runner namespace and restricted egress proxy", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.LoadWorker(options.configPath)
		if err != nil {
			return err
		}
		if cfg.Kubernetes == nil {
			return cmd.Help()
		}
		return (kuberunner.Connector{Config: *cfg.Kubernetes}).Install(cmd.Context())
	}})
	root.AddCommand(&cobra.Command{Use: "login", Short: "Store a dedicated Codex login or Claude setup-token for the cluster", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.LoadWorker(options.configPath)
		if err != nil {
			return err
		}
		if cfg.Kubernetes == nil {
			return cmd.Help()
		}
		return (kuberunner.Connector{Config: *cfg.Kubernetes}).Login(cmd.Context(), options.stdin, options.stdout, options.stderr)
	}})
	return root
}

func newSandboxCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{Use: "sandbox", Hidden: true}
	root.AddCommand(&cobra.Command{Use: "receive", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return kuberunner.Receive(options.stdin, "/exchange") }})
	root.AddCommand(&cobra.Command{Use: "prepare", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return kuberunner.Prepare(cmd.Context(), "/exchange", kuberunner.SourceDirectory)
	}})
	root.AddCommand(&cobra.Command{Use: "run", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return kuberunner.Sandbox(cmd.Context(), "/exchange", kuberunner.SourceDirectory, "/home/agent", "/credentials")
	}})
	var allowed []string
	proxy := &cobra.Command{Use: "proxy", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return kuberunner.ServeProxy(cmd.Context(), ":3128", allowed)
	}}
	proxy.Flags().StringSliceVar(&allowed, "allow", nil, "Exact HTTPS destinations allowed by the operator")
	root.AddCommand(proxy)
	return root
}
