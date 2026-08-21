// Package builtin holds the commands that belong to no service: signing in,
// choosing a project, inspecting contracts.
//
// They print through the same printer as generated commands, so -o json works
// on `auth status` exactly as it does on `disk list`. A wrapper program should
// not have to special-case them.
package builtin

import (
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/cmd/leaflow/internal/extension"
)

var (
	ErrCompletionFailed = errors.New("cannot install completion")

	ErrUnsupportedShell = errors.New("unsupported shell")
)

// Commands returns every top-level command.
//
// root is needed because completion is generated from the finished tree, which
// does not exist yet when these are built.
func Commands(ext *extension.Context, root *cobra.Command, version string) []*cobra.Command {
	return []*cobra.Command{
		newLoginCommand(ext),
		newLogoutCommand(ext),
		newAuthCommand(ext),
		newProjectCommand(ext),
		newContextCommand(ext),
		newMCPCommand(ext, version),
		newCompletionInstallCommand(root),
		newUpdateCommand(ext, version),
	}
}

func newLogoutCommand(ext *extension.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "sign out and revoke the refresh token",
		Long: `Remove local credentials and ask the realm to revoke the refresh token.

Access tokens already issued stay valid at each service until they expire;
that is bounded by their lifetime and is not something a client can shorten.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := ext.Tokens.Logout(cmd.Context()); err != nil {
				return err
			}

			return ext.Printer.Print(map[string]any{
				"logged_out": true,
			})
		},
	}
}

func newAuthCommand(ext *extension.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "inspect credentials",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "show the current credential state",
		Long: `Show credential state.

This reads only: it does not renew or exchange anything, because a command for
inspecting state should not change it.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := ext.Tokens.Status()
			if err != nil {
				return err
			}

			result := map[string]any{
				"logged_in": status.LoggedIn,
				"context":   ext.Config.CurrentName(),
			}

			if status.FromEnv {
				result["source"] = "LEAFLOW_TOKEN"
			} else {
				result["source"] = status.Storage
			}

			if status.Account != "" {
				result["account"] = status.Account
			}

			if status.LoggedIn && !status.FromEnv {
				result["offline"] = status.Offline
			}

			if status.Project != "" {
				result["project"] = status.Project
			}

			if !status.AccountExpires.IsZero() {
				result["account_expires"] = status.AccountExpires.Format(time.RFC3339)
			}

			if !status.AccessTokenExpires.IsZero() {
				result["access_token_expires"] = status.AccessTokenExpires.Format(time.RFC3339)
			}

			return ext.Printer.Print(result)
		},
	})

	return cmd
}
