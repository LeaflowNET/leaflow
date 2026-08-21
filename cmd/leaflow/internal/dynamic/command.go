package dynamic

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/pkg/naming"
	"github.com/LeaflowNET/leaflow/pkg/output"
	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/LeaflowNET/leaflow/pkg/transport"
)

// Runtime holds what generated commands need at run time.
//
// It is passed as a pointer because the tree is built before flags are parsed,
// and --output is not known until after; commands read these fields when they
// run, not when they are created.
type Runtime struct {
	Client  *transport.Client
	Printer *output.Printer
}

func NewCommand(use string, op *spec.Operation, rt *Runtime) *cobra.Command {
	binding := Bind(op)

	cmd := &cobra.Command{
		Use:           commandUsage(use, binding),
		Short:         writeSummary(op),
		Long:          writeDetails(op, binding),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	if op.Deprecated {
		cmd.Deprecated = "this operation is marked deprecated"
	}

	binding.Register(cmd)

	// Filenames are never a valid id, and offering them is worse than offering
	// nothing: it answers a question nobody asked. No candidates are suggested
	// because the contract does not say where they would come from — deriving
	// that from the path would be right often enough to be relied on and wrong
	// often enough to mislead.
	cmd.ValidArgsFunction = func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return run(cmd, args, binding, rt)
	}

	return cmd
}

func run(cmd *cobra.Command, args []string, binding *Binding, rt *Runtime) error {
	path, query, body, err := binding.Values(cmd, args)
	if err != nil {
		return err
	}

	result, _, err := rt.Client.Do(cmd.Context(), &transport.Request{
		Operation: binding.Operation,
		Path:      fillPath(binding, path),
		Query:     url.Values(query),
		Body:      body,
	})
	if err != nil {
		return err
	}

	// A 204 prints something rather than nothing: an empty screen after a
	// command looks like it did not run.
	if result == nil {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "ok")

		return err
	}

	return rt.Printer.Print(result)
}

func fillPath(binding *Binding, values []string) string {
	path := binding.Operation.Path

	for i, parameter := range binding.Path {
		if i >= len(values) {
			break
		}

		// Escaped, not concatenated: a value containing / or a space would
		// otherwise point the request somewhere else.
		path = strings.ReplaceAll(path, "{"+parameter.Name+"}", url.PathEscape(values[i]))
	}

	return path
}

func commandUsage(name string, binding *Binding) string {
	parts := []string{name}

	for _, parameter := range binding.Path {
		parts = append(parts, "<"+naming.Kebab(parameter.Name)+">")
	}

	return strings.Join(parts, " ")
}

// describeArguments spells out each positional argument.
//
// The usage line says <modelId> and nothing else, which does not say whether
// that is a name, a UUID, or something with a length limit. Everything printed
// here is already in the contract; none of it is inferred.
func describeArguments(binding *Binding) string {
	if len(binding.Path) == 0 {
		return ""
	}

	var builder strings.Builder

	builder.WriteString("Arguments:\n")

	for _, parameter := range binding.Path {
		constraint := describeSchema(readParameterSchema(parameter))

		description := cutAtSentence(parameter.Description)
		if description == "" {
			description = cutAtSentence(readSchemaDescription(readParameterSchema(parameter)))
		}

		fmt.Fprintf(&builder, "  %-22s %s", "<"+naming.Kebab(parameter.Name)+">", constraint)

		if description != "" {
			fmt.Fprintf(&builder, " — %s", description)
		}

		builder.WriteString("\n")
	}

	return builder.String()
}

func writeSummary(op *spec.Operation) string {
	if op.Summary != "" {
		return op.Summary
	}

	return op.Method + " " + op.Path
}

// writeDetails ends with the operationId, method and path: those are what you match
// against backend logs or API docs, and there is no other way back to the
// operationId from a command name.
func writeDetails(op *spec.Operation, binding *Binding) string {
	var builder strings.Builder

	if op.Summary != "" {
		builder.WriteString(op.Summary)
		builder.WriteString("\n\n")
	}

	if op.Description != "" {
		builder.WriteString(strings.TrimSpace(op.Description))
		builder.WriteString("\n\n")
	}

	if arguments := describeArguments(binding); arguments != "" {
		builder.WriteString(arguments)
		builder.WriteString("\n")
	}

	fmt.Fprintf(&builder, "operation: %s (%s %s)", op.ID, op.Method, op.Path)

	if op.Credential == spec.AccountToken {
		builder.WriteString("\ncredential: account token (no project needed)")
	}

	return builder.String()
}
