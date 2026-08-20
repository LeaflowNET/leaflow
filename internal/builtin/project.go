package builtin

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/internal/extension"
	"github.com/LeaflowNET/leaflow/internal/spec"
	"github.com/LeaflowNET/leaflow/internal/transport"
)

var (
	ErrNoSuchProject = errors.New("no such project")

	ErrAccountMissing = errors.New("no account contract is bundled, so projects cannot be listed")
)

// newProjectCommand covers choosing a project, which is not an operation: the
// exchange happens against IAM, but "remember this one" is local state.
func newProjectCommand(ext *extension.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "list and select projects",
		Long: `A session belongs to one project: the token names it, so no request path
carries a project id. Selecting one here is what every later command runs
against.`,
	}

	cmd.AddCommand(newProjectListCommand(ext), newProjectUseCommand(ext), newProjectCurrentCommand(ext))

	return cmd
}

func newProjectListCommand(ext *extension.Context) *cobra.Command {
	return &cobra.Command{
		Use:           "list",
		Short:         "list the projects you belong to",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := listProjects(cmd, ext)
			if err != nil {
				return err
			}

			return ext.Printer.Print(result)
		},
	}
}

func listProjects(cmd *cobra.Command, ext *extension.Context) (any, error) {
	// This runs before a project exists, so it goes to the account face with an
	// account token. spec.Operation carries that decision already.
	op, err := accountOperation(ext, "list-projects")
	if err != nil {
		return nil, err
	}

	result, _, err := ext.Client.Do(cmd.Context(), &transport.Request{
		Operation: op,
		Path:      op.Path,
		Query:     url.Values{},
	})

	return result, err
}

func newProjectUseCommand(ext *extension.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "use <project>",
		Short: "select the project for later commands",
		Long: `Select a project by id or by name.

The cached project token is dropped at the same time. Keeping it would not
fail — the next command would succeed against the previous project, which is
worse than any error.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, name, err := resolveProject(cmd, ext, args[0])
			if err != nil {
				return err
			}

			cfg := ext.Config
			active := cfg.EditContext("")
			active.Project = id

			if err := ext.Tokens.InvalidateProject(); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			return ext.Printer.Print(map[string]any{
				"project": id,
				"name":    name,
				"context": cfg.CurrentName(),
			})
		},
	}
}

// resolveProject accepts an id or a name. Names are matched by listing, which
// costs one request but means people can use the word they actually know.
func resolveProject(cmd *cobra.Command, ext *extension.Context, wanted string) (id, name string, err error) {
	listed, err := listProjects(cmd, ext)
	if err != nil {
		return "", "", err
	}

	for _, item := range projectItems(listed) {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}

		// A listing entry wraps the project alongside the caller's grant, so the
		// identifying fields are one level down.
		if nested, wrapped := object["project"].(map[string]any); wrapped {
			object = nested
		}

		gotID, _ := object["id"].(string)
		gotName, _ := object["name"].(string)

		if gotID == wanted || gotName == wanted {
			return gotID, gotName, nil
		}
	}

	return "", "", fmt.Errorf("%w: %q; see: leaflow project list", ErrNoSuchProject, wanted)
}

func projectItems(listed any) []any {
	object, ok := listed.(map[string]any)
	if !ok {
		if list, isList := listed.([]any); isList {
			return list
		}

		return nil
	}

	for _, value := range object {
		if list, isList := value.([]any); isList {
			return list
		}
	}

	return nil
}

func newProjectCurrentCommand(ext *extension.Context) *cobra.Command {
	return &cobra.Command{
		Use:           "current",
		Short:         "show the selected project",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			active := ext.Config.Context()

			return ext.Printer.Print(map[string]any{
				"project": active.Project,
				"context": ext.Config.CurrentName(),
			})
		},
	}
}

// accountOperation looks up an operation on the account face, which is where
// listing projects lives: it runs before a project is chosen, so it takes an
// account token rather than a project one.
func accountOperation(ext *extension.Context, id string) (*spec.Operation, error) {
	service, ok := ext.Specs.Service("account")
	if !ok {
		return nil, ErrAccountMissing
	}

	op, ok := service.Operation(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrAccountMissing, id)
	}

	return op, nil
}
