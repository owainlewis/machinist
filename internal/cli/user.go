package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/spf13/cobra"
)

func newUserCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{Use: "user", Short: "Manage team members"}

	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List team members",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			users, err := store.ListUsers(command.Context())
			if err != nil {
				return err
			}
			table := tabwriter.NewWriter(options.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "ID\tGITHUB\tNAME\tEMAIL\tROLE\tSTATUS\tLAST SEEN\tCREATED")
			for _, u := range users {
				status := "active"
				if u.DisabledAt != nil {
					status = "disabled"
				}
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					u.ID, u.GitHubLogin, dashIfEmpty(u.Name), dashIfEmpty(u.Email), u.Role, status, displayTime(u.LastSeenAt), u.CreatedAt.Format(time.RFC3339))
			}
			return table.Flush()
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "set-role <id-or-github-login> <role>",
		Short: "Change a team member's role (admin, member, or viewer)",
		Args:  cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			role := strings.TrimSpace(args[1])
			if !tickets.ValidUserRole(role) {
				return fmt.Errorf("invalid role %q; must be admin, member, or viewer", role)
			}
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			user, err := store.FindUserByIDOrLogin(command.Context(), args[0])
			if errors.Is(err, tickets.ErrNotFound) {
				return fmt.Errorf("no user matching %q", args[0])
			}
			if err != nil {
				return err
			}
			updated, err := store.UpdateUser(command.Context(), user.ID, &role, nil)
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stdout, "updated %s (%s) role to %s\n", updated.GitHubLogin, updated.ID, updated.Role)
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "disable <id-or-github-login>",
		Short: "Disable a team member and revoke all their sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			user, err := store.FindUserByIDOrLogin(command.Context(), args[0])
			if errors.Is(err, tickets.ErrNotFound) {
				return fmt.Errorf("no user matching %q", args[0])
			}
			if err != nil {
				return err
			}
			dis := true
			updated, err := store.UpdateUser(command.Context(), user.ID, nil, &dis)
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stdout, "disabled %s (%s)\n", updated.GitHubLogin, updated.ID)
			return nil
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "enable <id-or-github-login>",
		Short: "Re-enable a disabled team member",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			user, err := store.FindUserByIDOrLogin(command.Context(), args[0])
			if errors.Is(err, tickets.ErrNotFound) {
				return fmt.Errorf("no user matching %q", args[0])
			}
			if err != nil {
				return err
			}
			dis := false
			updated, err := store.UpdateUser(command.Context(), user.ID, nil, &dis)
			if err != nil {
				return err
			}
			fmt.Fprintf(options.stdout, "enabled %s (%s)\n", updated.GitHubLogin, updated.ID)
			return nil
		},
	})

	return root
}

func dashIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
