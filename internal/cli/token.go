package cli

import (
	"errors"
	"fmt"
	"strings"
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
	var runner bool
	var scope string
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a token and print it once",
		Long: "Create a token and print it once. By default a token has full operator access, to sign in and use the API " +
			"and MCP. Use --scope read or --scope run to limit it to reading or running jobs, or --runner for a runner's " +
			"token, which can only take runs and report on them.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			scope = strings.TrimSpace(scope)
			if runner && scope != "" {
				return errors.New("--runner and --scope cannot be used together")
			}
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			role := tickets.RoleOperator
			if runner {
				role = tickets.RoleRunner
			}
			token, err := store.CreateScopedToken(command.Context(), args[0], role, scope)
			if err != nil {
				return err
			}
			fmt.Fprintln(options.stdout, token)
			return nil
		},
	}
	create.Flags().BoolVar(&runner, "runner", false, "make a runner's token, which can only take runs and report on them")
	create.Flags().StringVar(&scope, "scope", "", "limit an operator token's access: read or run")
	root.AddCommand(create)
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
			fmt.Fprintln(table, "NAME\tROLE\tPREFIX\tSCOPE\tCREATED\tLAST USED\tREVOKED")
			for _, token := range tokens {
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", token.Name, displayRole(token.Role), displayPrefix(token.Prefix), displayScope(token.Role, token.Scope), token.CreatedAt.Format(time.RFC3339), displayTime(token.LastUsedAt), displayTime(token.RevokedAt))
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

// displayRole names a token's role. A token from before roles can do both.
func displayRole(role string) string {
	if role == tickets.RoleAll {
		return "any"
	}
	return role
}

// displayScope names a token's scope. Runner tokens have no scope; operator
// tokens without a scope have full access.
func displayScope(role, scope string) string {
	if role == tickets.RoleRunner {
		return "-"
	}
	if scope == tickets.ScopeAll {
		return "full"
	}
	return scope
}

// displayPrefix shows a token's recognisable prefix, or "No prefix" for
// tokens made before prefixes were added.
func displayPrefix(prefix string) string {
	if prefix == "" {
		return "No prefix"
	}
	return prefix + "…"
}

func displayTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Format(time.RFC3339)
}
