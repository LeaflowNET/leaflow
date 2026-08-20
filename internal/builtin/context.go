package builtin

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/internal/config"
	"github.com/LeaflowNET/leaflow/internal/extension"
)

var (
	ErrNoSuchContext = errors.New("no such context")

	ErrBadEndpoint = errors.New("malformed endpoint")
)

func newContextCommand(ext *extension.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "manage contexts",
		Long: `A context is one "which platform, as whom, in which project" triple.

Working against production and a local stack at the same time is normal, and a
per-command flag is something you eventually forget to pass.`,
	}

	cmd.AddCommand(
		newContextListCommand(ext),
		newContextUseCommand(ext),
		newContextSetCommand(ext),
	)

	return cmd
}

func newContextListCommand(ext *extension.Context) *cobra.Command {
	return &cobra.Command{
		Use:           "list",
		Short:         "list configured contexts",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := ext.Config
			current := cfg.CurrentName()

			names := make([]string, 0, len(cfg.Contexts))
			for name := range cfg.Contexts {
				names = append(names, name)
			}

			sort.Strings(names)

			items := make([]any, 0, len(names))

			for _, name := range names {
				one := cfg.Contexts[name]

				items = append(items, map[string]any{
					"name":    name,
					"current": name == current,
					"domain":  one.Domain,
					"project": one.Project,
					"account": one.Account,
				})
			}

			return ext.Printer.Print(map[string]any{"items": items})
		},
	}
}

func newContextUseCommand(ext *extension.Context) *cobra.Command {
	return &cobra.Command{
		Use:           "use <name>",
		Short:         "switch to a context",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := ext.Config

			if _, ok := cfg.Contexts[args[0]]; !ok {
				return fmt.Errorf("%w: %q; see: leaflow context list", ErrNoSuchContext, args[0])
			}

			cfg.Current = args[0]

			if err := cfg.Save(); err != nil {
				return err
			}

			return ext.Printer.Print(map[string]any{"context": args[0]})
		},
	}
}

// newContextSetCommand creates or edits a context, so that pointing the CLI at
// a local stack does not require hand-writing YAML.
func newContextSetCommand(ext *extension.Context) *cobra.Command {
	var (
		domain    string
		issuer    string
		clientID  string
		project   string
		endpoints []string
	)

	cmd := &cobra.Command{
		Use:           "set <name>",
		Short:         "create or update a context",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().StringVar(&domain, "domain", "", "address suffix shared by the services")
	cmd.Flags().StringVar(&issuer, "issuer", "", "OIDC issuer, including the realm")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OIDC client id; must be a public client")
	cmd.Flags().StringVar(&project, "project", "", "project to select in this context")
	cmd.Flags().StringArrayVar(&endpoints, "endpoint", nil,
		"override one service's address, as service=url; repeatable. Use \"service=\" to drop an override")

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg := ext.Config
		name := args[0]

		one, ok := cfg.Contexts[name]
		if !ok || one == nil {
			one = &config.Context{}
			cfg.Contexts[name] = one
		}

		for _, pair := range []struct {
			flag   string
			value  string
			target *string
		}{
			{"domain", domain, &one.Domain},
			{"issuer", issuer, &one.Issuer},
			{"client-id", clientID, &one.ClientID},
			{"project", project, &one.Project},
		} {
			if cmd.Flags().Changed(pair.flag) {
				*pair.target = pair.value
			}
		}

		if err := applyEndpoints(one, endpoints); err != nil {
			return err
		}

		if err := cfg.Save(); err != nil {
			return err
		}

		return ext.Printer.Print(map[string]any{
			"context":   name,
			"domain":    one.Domain,
			"issuer":    one.Issuer,
			"project":   one.Project,
			"endpoints": one.Endpoints,
			"saved_to":  cfg.Path(),
		})
	}

	return cmd
}

// applyEndpoints records per-service address overrides.
//
// Local stacks need these: the gateway's faces are on ports that are not 443
// and hostnames that do not follow from a domain, so they cannot be derived the
// way the hosted addresses can.
func applyEndpoints(one *config.Context, pairs []string) error {
	for _, pair := range pairs {
		service, address, found := strings.Cut(pair, "=")
		if !found || service == "" {
			return fmt.Errorf("%w %q: expected service=url", ErrBadEndpoint, pair)
		}

		// An empty value removes the override, so returning a context to the
		// derived addresses does not require editing YAML by hand.
		if address == "" {
			delete(one.Endpoints, service)

			continue
		}

		if one.Endpoints == nil {
			one.Endpoints = map[string]string{}
		}

		one.Endpoints[service] = strings.TrimRight(address, "/")
	}

	return nil
}
