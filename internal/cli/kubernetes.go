package cli

import (
	"path/filepath"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/kuberunner"
	"github.com/spf13/cobra"
)

func newKubernetesCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{Use: "kubernetes", Short: "Manage the operator's Kubernetes runner"}
	root.AddCommand(&cobra.Command{Use: "install", Short: "Install the private gVisor runner namespace and restricted egress proxy", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.LoadRunner(options.configPath)
		if err != nil {
			return err
		}
		if cfg.Kubernetes == nil {
			return cmd.Help()
		}
		return (kuberunner.Connector{Config: *cfg.Kubernetes}).Install(cmd.Context())
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
		return kuberunner.Sandbox(cmd.Context(), "/exchange", filepath.Dir(kuberunner.SourceDirectory), "/home/agent", nil)
	}})
	var allowed []string
	proxy := &cobra.Command{Use: "proxy", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return kuberunner.ServeProxy(cmd.Context(), ":3128", allowed)
	}}
	proxy.Flags().StringSliceVar(&allowed, "allow", nil, "Exact HTTPS destinations allowed by the operator")
	root.AddCommand(proxy)
	return root
}
