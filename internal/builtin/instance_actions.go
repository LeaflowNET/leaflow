package builtin

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/internal/extension"
	"github.com/LeaflowNET/leaflow/internal/spec"
	"github.com/LeaflowNET/leaflow/internal/transport"
)

var ErrComputeMissing = errors.New("no compute contract is bundled")

// The generated command for this operation is `compute instance act-on-instance
// --action start`, which is faithful to the contract and awkward to type. The
// verb lives in the request body, and no rule derived from a contract can lift
// it into a command name — that is exactly the kind of judgement this package
// exists for.
//
// The generated command is not removed. `compute instance act-on-instance`
// still works, so nothing became unreachable by making this nicer.
func init() {
	for _, action := range []struct {
		verb  string
		short string
		force bool
	}{
		{verb: "start", short: "start an instance"},
		{verb: "stop", short: "stop an instance"},
		{verb: "reboot", short: "reboot an instance", force: true},
	} {
		extension.Register(extension.Command{
			Service: "compute",
			Path:    []string{"instance", action.verb},
			Build:   instanceAction(action.verb, action.short, action.force),
		})
	}
}

func instanceAction(verb, short string, offersForce bool) func(*extension.Context) *cobra.Command {
	return func(ext *extension.Context) *cobra.Command {
		var force bool

		cmd := &cobra.Command{
			Use:           verb + " <instance-id>",
			Short:         short,
			Args:          cobra.ExactArgs(1),
			SilenceUsage:  true,
			SilenceErrors: true,
		}

		if offersForce {
			cmd.Flags().BoolVar(&force, "force", false,
				"do not wait for a clean shutdown; unflushed data is lost. For an unresponsive system")
		}

		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			op, err := computeOperation(ext, "act-on-instance")
			if err != nil {
				return err
			}

			body := map[string]any{"action": verb}
			if offersForce && force {
				body["force"] = true
			}

			path := strings.ReplaceAll(op.Path, "{instanceId}", url.PathEscape(args[0]))

			result, _, err := ext.Client.Do(cmd.Context(), &transport.Request{
				Operation: op,
				Path:      path,
				Query:     url.Values{},
				Body:      body,
			})
			if err != nil {
				return err
			}

			if result == nil {
				return ext.Printer.Print(map[string]any{"instance": args[0], "action": verb})
			}

			return ext.Printer.Print(result)
		}

		return cmd
	}
}

func computeOperation(ext *extension.Context, id string) (*spec.Operation, error) {
	service, ok := ext.Specs.Service("compute")
	if !ok {
		return nil, ErrComputeMissing
	}

	op, ok := service.Operation(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrComputeMissing, id)
	}

	return op, nil
}
