package cli

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/spf13/cobra"
)

// openTokenStore opens the database directly. That is how the first token is
// made, because every API route needs one.
func openTokenStore(options *commandOptions) (*tickets.Store, error) {
	machinistConfig, err := config.LoadConfig(options.configPath)
	if err != nil {
		return nil, err
	}
	return tickets.Open(machinistConfig.Server.Database)
}

func newTokenCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{Use: "token", Short: "Manage tokens for developers and runners"}
	root.AddCommand(&cobra.Command{
		Use:   "create <name>",
		Short: "Create a token and print it once",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			token, err := store.CreateToken(command.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(options.stdout, token)
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List tokens",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			tokens, err := store.ListTokens(command.Context())
			if err != nil {
				return err
			}
			table := tabwriter.NewWriter(options.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "NAME\tCREATED\tLAST USED\tREVOKED")
			for _, token := range tokens {
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", token.Name, token.CreatedAt.Format(time.RFC3339), displayTime(token.LastUsedAt), displayTime(token.RevokedAt))
			}
			return table.Flush()
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "revoke <name>",
		Short: "Revoke a token and end its sign-ins",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			err = store.RevokeToken(command.Context(), args[0])
			if errors.Is(err, tickets.ErrNotFound) {
				return fmt.Errorf("no active token named %q", args[0])
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stdout, "revoked %s\n", args[0])
			return nil
		},
	})
	return root
}

func displayTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Format(time.RFC3339)
}
