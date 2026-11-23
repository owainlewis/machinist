package cli

import (
	"fmt"
	"io"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/spf13/cobra"
)

func newFactoryCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{
		Use:   "factory",
		Short: "Inspect and upgrade factory files",
	}
	var force bool
	upgrade := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade shipped factory files without overwriting your edits",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			machinistConfig, err := config.LoadConfig(options.configPath)
			if err != nil {
				return err
			}
			return upgradeFactory(machinistConfig.Server.Factory, force, options.stdout)
		},
	}
	upgrade.Flags().BoolVar(&force, "force", false, "overwrite edited files with the shipped versions")
	root.AddCommand(upgrade)
	return root
}

func upgradeFactory(directory string, force bool, output io.Writer) error {
	result, err := factory.Upgrade(directory, force)
	if err != nil {
		return err
	}
	if len(result.Entries) == 0 {
		fmt.Fprintln(output, "factory is up to date")
		return nil
	}
	changed := 0
	skipped := 0
	for _, entry := range result.Entries {
		switch entry.Action {
		case factory.ActionUpdated:
			changed++
			fmt.Fprintf(output, "updated %s\n", entry.Path)
		case factory.ActionCreated:
			changed++
			fmt.Fprintf(output, "created %s\n", entry.Path)
		case factory.ActionSkipped:
			skipped++
			fmt.Fprintf(output, "skipped %s (edited locally; pass --force to overwrite)\n", entry.Path)
			if entry.Diff != "" {
				fmt.Fprint(output, entry.Diff)
			}
		}
	}
	if changed > 0 {
		fmt.Fprintln(output, "Restart the control plane to load the updated factory.")
	}
	if skipped > 0 {
		noun := "edited files were"
		if skipped == 1 {
			noun = "edited file was"
		}
		fmt.Fprintf(output, "%d %s kept; review the diff above or run machinist factory upgrade --force to overwrite.\n", skipped, noun)
	}
	return nil
}
