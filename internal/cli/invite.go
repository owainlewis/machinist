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

func newInviteCommand(options *commandOptions) *cobra.Command {
	root := &cobra.Command{Use: "invite", Short: "Manage team invitations"}

	var (
		role        string
		githubLogin string
		email       string
	)
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a targeted invite and print its one-time URL",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := config.LoadConfig(options.configPath)
			if err != nil {
				return err
			}
			store, err := tickets.Open(cfg.Server.Database)
			if err != nil {
				return err
			}
			defer store.Close()

			inv, secret, err := store.CreateInvite(command.Context(), strings.TrimSpace(role), githubLogin, email, "cli")
			if errors.Is(err, tickets.ErrInviteConflict) {
				return errors.New("a user matching that target already exists; update or re-enable them with machinist user")
			}
			if err != nil {
				return err
			}
			inviteURL := "/#/invite#" + secret
			if cfg.Server.PublicURL != "" {
				inviteURL = strings.TrimRight(cfg.Server.PublicURL, "/") + inviteURL
			}
			fmt.Fprintf(options.stdout, "%s\n", inviteURL)
			fmt.Fprintf(options.stderr, "created invite %s (role=%s, expires=%s)\n", inv.ID, inv.Role, inv.ExpiresAt.Format(time.RFC3339))
			return nil
		},
	}
	create.Flags().StringVar(&role, "role", tickets.UserRoleMember, "role for the invited user: admin, member, or viewer")
	create.Flags().StringVar(&githubLogin, "github", "", "GitHub username to invite")
	create.Flags().StringVar(&email, "email", "", "verified email address to invite")
	root.AddCommand(create)

	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List pending invites",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			invites, err := store.ListPendingInvites(command.Context())
			if err != nil {
				return err
			}
			table := tabwriter.NewWriter(options.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "ID\tTARGET\tROLE\tCREATED BY\tEXPIRES\tCREATED")
			for _, inv := range invites {
				target := "github:" + inv.GitHubLogin
				if inv.GitHubLogin == "" {
					target = inv.Email
				}
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", inv.ID, target, inv.Role, inv.CreatedBy, inv.ExpiresAt.Format(time.RFC3339), inv.CreatedAt.Format(time.RFC3339))
			}
			return table.Flush()
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a pending invite",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := openTokenStore(options)
			if err != nil {
				return err
			}
			defer store.Close()
			err = store.RevokeInvite(command.Context(), args[0])
			if errors.Is(err, tickets.ErrNotFound) {
				return fmt.Errorf("no pending invite %q", args[0])
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
