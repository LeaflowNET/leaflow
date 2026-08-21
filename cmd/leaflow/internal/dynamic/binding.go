package dynamic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/LeaflowNET/leaflow/pkg/naming"
	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/LeaflowNET/leaflow/pkg/validate"
)

var (
	ErrBodyConflict = errors.New("--body cannot be combined with field flags")

	ErrBodyUnreadable = errors.New("cannot read request body")

	ErrBodyNotJSON = errors.New("--body is not valid JSON")

	ErrMissingArguments = errors.New("missing argument")

	ErrTooManyArguments = errors.New("too many arguments")
)

// Binding describes an operation's arguments:
//
//	path parameters  -> positional   leaflow compute disk get <disk-id>
//	query parameters -> flags        --region-code cn-north-1
//	request body     -> one flag per field, or --body
//
// Path parameters are positional because they are always required and already
// ordered by the path itself; spelling them as flags only makes the same
// information longer.
type Binding struct {
	Operation *spec.Operation

	Path []*openapi3.Parameter

	Query []*openapi3.Parameter

	Body *openapi3.Schema

	Fields []*BodyField

	flagNames map[*openapi3.Parameter]string
}

type BodyField struct {
	Name     string
	Flag     string
	Schema   *openapi3.Schema
	Required bool
}

// Bind adds the shell's half to the contract's parameter split: a flag name for
// every query parameter, and one flag per scalar body field.
//
// The split itself comes from the contract and is shared with every other
// caller; what is here is only what a shell needs, because a shell is the one
// caller that cannot type an object.
func Bind(op *spec.Operation) *Binding {
	inputs := op.Inputs()

	binding := &Binding{
		Operation: op,
		Path:      inputs.Path,
		Query:     inputs.Query,
		Body:      inputs.Body,
		flagNames: map[*openapi3.Parameter]string{},
	}

	binding.bindFields()
	binding.assignFlagNames()

	return binding
}

func (b *Binding) bindFields() {
	schema := b.Body
	if schema == nil {
		return
	}

	if readPrimaryType(schema) != "object" {
		return
	}

	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}

	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		ref := schema.Properties[name]
		if ref == nil || ref.Value == nil {
			continue
		}

		// Nested objects and object arrays do not fit one flag. They go through
		// --body; inventing a `--rules[0].port` syntax would cost more to learn
		// than writing the JSON.
		if !canFlatten(ref.Value) {
			continue
		}

		b.Fields = append(b.Fields, &BodyField{
			Name:     name,
			Flag:     naming.Kebab(name),
			Schema:   ref.Value,
			Required: required[name],
		})
	}
}

// assignFlagNames resolves collisions in favour of body fields, which are the point
// of the command (`disk create --name`), while query parameters are usually
// filters. Nothing collides in today's contracts, but two flags with one name
// make cobra panic during registration — on a user's machine, not in CI.
func (b *Binding) assignFlagNames() {
	taken := map[string]bool{
		"body": true,
	}

	for _, field := range b.Fields {
		taken[field.Flag] = true
	}

	for _, parameter := range b.Query {
		name := naming.Kebab(parameter.Name)
		if taken[name] {
			name = "query-" + name
		}

		taken[name] = true
		b.flagNames[parameter] = name
	}
}

func (b *Binding) FlagName(parameter *openapi3.Parameter) string {
	if name, ok := b.flagNames[parameter]; ok {
		return name
	}

	return naming.Kebab(parameter.Name)
}

func (b *Binding) Register(cmd *cobra.Command) {
	flags := cmd.Flags()

	for _, parameter := range b.Query {
		registerFlag(flags, b.FlagName(parameter), readParameterSchema(parameter), writeParameterUsage(parameter))
	}

	for _, field := range b.Fields {
		usage := writeSchemaUsage(field.Schema)
		if field.Required {
			usage = "(required) " + usage
		}

		registerFlag(flags, field.Flag, field.Schema, usage)
	}

	if b.Body != nil {
		flags.String("body", "", "whole request body as JSON; @file or @- to read from a file or stdin")
	}

	// cobra's own message is "accepts 1 arg(s), received 0", which says how many
	// are missing but not what they are. The contract knows their names, types
	// and limits, and that is the whole of what someone needs to type one.
	if len(b.Path) > 0 {
		cmd.Args = b.exactArgs()
	} else {
		cmd.Args = cobra.NoArgs
	}
}

func (b *Binding) exactArgs() cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == len(b.Path) {
			return nil
		}

		help := fmt.Sprintf("\n\n%s\nusage: %s %s",
			strings.TrimRight(describeArguments(b), "\n"),
			cmd.CommandPath(),
			commandUsageArgs(b))

		if len(args) < len(b.Path) {
			missing := make([]string, 0, len(b.Path)-len(args))
			for _, parameter := range b.Path[len(args):] {
				missing = append(missing, "<"+naming.Kebab(parameter.Name)+">")
			}

			return fmt.Errorf("%w: %s%s", ErrMissingArguments, strings.Join(missing, " "), help)
		}

		return fmt.Errorf("%w: expected %d, got %d%s",
			ErrTooManyArguments, len(b.Path), len(args), help)
	}
}

func commandUsageArgs(b *Binding) string {
	parts := make([]string, 0, len(b.Path))
	for _, parameter := range b.Path {
		parts = append(parts, "<"+naming.Kebab(parameter.Name)+">")
	}

	return strings.Join(parts, " ")
}

func registerFlag(flags *pflag.FlagSet, name string, schema *openapi3.Schema, usage string) {
	switch readPrimaryType(schema) {
	case "boolean":
		flags.Bool(name, false, usage)
	case "array":
		flags.StringSlice(name, nil, usage)
	default:
		// Everything else is collected as a string and converted in Values().
		// Letting pflag convert produces `invalid argument "abc" for "--size-gb"
		// flag: strconv.ParseInt: parsing "abc": invalid syntax`.
		flags.String(name, "", usage)
	}
}

// Values collects what was typed into the three pieces a request needs.
func (b *Binding) Values(cmd *cobra.Command, args []string) (path []string, query map[string][]string, body any, err error) {
	flags := cmd.Flags()

	path = make([]string, 0, len(b.Path))

	var problems []string

	for i, parameter := range b.Path {
		raw := args[i]
		schema := readParameterSchema(parameter)

		converted, convErr := convert(raw, schema)
		if convErr != nil {
			problems = append(problems, fmt.Sprintf("<%s> %s", naming.Kebab(parameter.Name), convErr))

			continue
		}

		if verr := validate.Value(schema, converted, pointAtPositional(parameter)); verr != nil {
			problems = appendProblems(problems, verr)

			continue
		}

		path = append(path, raw)
	}

	// Only send what was actually given: `--name ""` filters on an empty name,
	// while omitting it does not filter on name at all.
	query = map[string][]string{}

	for _, parameter := range b.Query {
		name := b.FlagName(parameter)

		if !flags.Changed(name) {
			continue
		}

		values, valueErr := queryValues(flags, name, parameter)
		if valueErr != nil {
			problems = appendProblems(problems, valueErr)

			continue
		}

		query[parameter.Name] = values
	}

	body, bodyErr := b.bodyValue(cmd)
	if bodyErr != nil {
		problems = appendProblems(problems, bodyErr)
	}

	if len(problems) > 0 {
		return nil, nil, nil, &validate.Error{
			Problems: problems,
		}
	}

	return path, query, body, nil
}

// appendProblems flattens a nested validation error instead of folding its
// entries into one string. Under -o json, problems has to be a real array for a
// wrapping program to read it.
func appendProblems(dst []string, err error) []string {
	var nested *validate.Error
	if errors.As(err, &nested) {
		return append(dst, nested.Problems...)
	}

	return append(dst, err.Error())
}

func queryValues(flags *pflag.FlagSet, name string, parameter *openapi3.Parameter) ([]string, error) {
	schema := readParameterSchema(parameter)

	switch readPrimaryType(schema) {
	case "array":
		return flags.GetStringSlice(name)
	case "boolean":
		value, err := flags.GetBool(name)
		if err != nil {
			return nil, err
		}

		return []string{
			strconv.FormatBool(value),
		}, nil
	}

	raw, err := flags.GetString(name)
	if err != nil {
		return nil, err
	}

	converted, err := convert(raw, schema)
	if err != nil {
		return nil, fmt.Errorf("--%s %s", name, err)
	}

	if verr := validate.Value(schema, converted, flagLabel(name)); verr != nil {
		return nil, verr
	}

	return []string{raw}, nil
}

func (b *Binding) bodyValue(cmd *cobra.Command) (any, error) {
	if b.Body == nil {
		return nil, nil
	}

	flags := cmd.Flags()
	given := listGivenFields(flags, b.Fields)

	// Mixing the two would need a rule for which wins, and nobody remembers such
	// a rule; exclusivity is the only version that needs no explanation.
	if flags.Changed("body") && len(given) > 0 {
		return nil, fmt.Errorf("%w: --%s", ErrBodyConflict, strings.Join(given, ", --"))
	}

	if flags.Changed("body") {
		raw, err := flags.GetString("body")
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBodyUnreadable, err)
		}

		return b.parseBody(cmd, raw)
	}

	object := map[string]any{}

	for _, field := range b.Fields {
		if !flags.Changed(field.Flag) {
			continue
		}

		value, err := fieldValue(flags, field)
		if err != nil {
			return nil, err
		}

		object[field.Name] = value
	}

	// An optional body with nothing supplied is omitted rather than sent as {},
	// which some operations would read as an explicit clear.
	if len(object) == 0 && !b.requiresBody() {
		return nil, nil
	}

	if err := validate.Value(b.Body, object, bodyLabel(b)); err != nil {
		return nil, err
	}

	return object, nil
}

func (b *Binding) requiresBody() bool {
	return b.Operation.RequestBody != nil && b.Operation.RequestBody.Required
}

func listGivenFields(flags *pflag.FlagSet, fields []*BodyField) []string {
	var given []string

	for _, field := range fields {
		if flags.Changed(field.Flag) {
			given = append(given, field.Flag)
		}
	}

	return given
}

func (b *Binding) parseBody(cmd *cobra.Command, raw string) (any, error) {
	text := strings.TrimSpace(raw)

	if after, ok := strings.CutPrefix(text, "@"); ok {
		source := after

		var (
			data []byte
			err  error
		)

		if source == "-" {
			data, err = io.ReadAll(cmd.InOrStdin())
		} else {
			data, err = os.ReadFile(source)
		}

		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBodyUnreadable, err)
		}

		text = string(data)
	}

	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBodyNotJSON, err)
	}

	if err := validate.Value(b.Body, value, pointIntoJSON()); err != nil {
		return nil, err
	}

	return value, nil
}

func fieldValue(flags *pflag.FlagSet, field *BodyField) (any, error) {
	switch readPrimaryType(field.Schema) {
	case "boolean":
		return flags.GetBool(field.Flag)
	case "array":
		list, err := flags.GetStringSlice(field.Flag)
		if err != nil {
			return nil, err
		}

		items := itemSchema(field.Schema)

		converted := make([]any, 0, len(list))

		for _, item := range list {
			value, err := convert(item, items)
			if err != nil {
				return nil, fmt.Errorf("--%s %s", field.Flag, err)
			}

			converted = append(converted, value)
		}

		return converted, nil
	default:
		raw, err := flags.GetString(field.Flag)
		if err != nil {
			return nil, err
		}

		value, err := convert(raw, field.Schema)
		if err != nil {
			return nil, fmt.Errorf("--%s %s", field.Flag, err)
		}

		return value, nil
	}
}

func convert(raw string, schema *openapi3.Schema) (any, error) {
	switch readPrimaryType(schema) {
	case "integer":
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("must be an integer (got %q)", raw)
		}

		// float64 because that is what JSON numbers decode to, and what the
		// validator compares against.
		return float64(value), nil
	case "number":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("must be a number (got %q)", raw)
		}

		return value, nil
	case "boolean":
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("must be true or false (got %q)", raw)
		}

		return value, nil
	default:
		return raw, nil
	}
}

// readPrimaryType cannot use kin-openapi's Type.Is(): these contracts are OpenAPI
// 3.1, where a nullable field is `type: [string, "null"]`, and Is() only
// matches a single-element type — so every nullable field would look untyped
// and silently degrade to a string.
func readPrimaryType(schema *openapi3.Schema) string {
	if schema == nil || schema.Type == nil {
		return ""
	}

	for _, one := range *schema.Type {
		if one != "null" {
			return one
		}
	}

	return ""
}

func itemSchema(schema *openapi3.Schema) *openapi3.Schema {
	if schema == nil || schema.Items == nil {
		return nil
	}

	return schema.Items.Value
}

func readParameterSchema(parameter *openapi3.Parameter) *openapi3.Schema {
	if parameter == nil || parameter.Schema == nil {
		return nil
	}

	return parameter.Schema.Value
}

func canFlatten(schema *openapi3.Schema) bool {
	switch readPrimaryType(schema) {
	case "string", "integer", "number", "boolean":
		return true
	case "array":
		switch readPrimaryType(itemSchema(schema)) {
		case "string", "integer", "number", "boolean":
			return true
		}
	}

	return false
}

func writeSchemaUsage(schema *openapi3.Schema) string {
	if schema == nil {
		return ""
	}

	text := cutAtSentence(schema.Description)

	if len(schema.Enum) > 0 {
		options := make([]string, 0, len(schema.Enum))
		for _, option := range schema.Enum {
			options = append(options, fmt.Sprint(option))
		}

		if text != "" {
			text += "; "
		}

		text += "one of: " + strings.Join(options, ", ")
	}

	return text
}

func writeParameterUsage(parameter *openapi3.Parameter) string {
	text := cutAtSentence(parameter.Description)
	if text == "" {
		text = writeSchemaUsage(readParameterSchema(parameter))
	}

	if parameter.Required {
		text = "(required) " + text
	}

	return text
}

// cutAtSentence keeps help to one line; the full text is in --help's long form.
func cutAtSentence(text string) string {
	text = strings.TrimSpace(text)

	if index := strings.IndexAny(text, "\n。"); index > 0 {
		text = text[:index]
	}

	return text
}

func flagLabel(name string) validate.Labeler {
	return func([]string) string {
		return "--" + name
	}
}

func pointAtPositional(parameter *openapi3.Parameter) validate.Labeler {
	return func([]string) string {
		return "<" + naming.Kebab(parameter.Name) + ">"
	}
}

// bodyLabel maps a JSON field back to the flag that set it.
func bodyLabel(b *Binding) validate.Labeler {
	return func(path []string) string {
		if len(path) == 0 {
			return "request body"
		}

		name := path[len(path)-1]

		for _, field := range b.Fields {
			if field.Name == name {
				return "--" + field.Flag
			}
		}

		return name
	}
}

// pointIntoJSON is used with --body, where the user wrote JSON and the error
// should point at a place in that JSON.
func pointIntoJSON() validate.Labeler {
	return func(path []string) string {
		if len(path) == 0 {
			return "request body"
		}

		return "request body ." + strings.Join(path, ".")
	}
}

// describeSchema states a value's type and limits in the terms the contract
// uses. Nothing is inferred: a missing constraint simply is not printed.
func describeSchema(schema *openapi3.Schema) string {
	if schema == nil {
		return "string"
	}

	kind := readPrimaryType(schema)
	if kind == "" {
		kind = "string"
	}

	// The format is more useful than the type when there is one: knowing an
	// argument is a UUID is what stops someone passing a name.
	if schema.Format != "" {
		kind = schema.Format
	}

	var limits []string

	if schema.MaxLength != nil {
		limits = append(limits, fmt.Sprintf("at most %d characters", *schema.MaxLength))
	}

	if schema.MinLength > 0 {
		limits = append(limits, fmt.Sprintf("at least %d characters", schema.MinLength))
	}

	if schema.Min != nil {
		limits = append(limits, fmt.Sprintf("minimum %v", *schema.Min))
	}

	if schema.Max != nil {
		limits = append(limits, fmt.Sprintf("maximum %v", *schema.Max))
	}

	if len(schema.Enum) > 0 {
		options := make([]string, 0, len(schema.Enum))
		for _, option := range schema.Enum {
			options = append(options, fmt.Sprint(option))
		}

		limits = append(limits, "one of "+strings.Join(options, ", "))
	}

	if len(limits) == 0 {
		return kind
	}

	return kind + ", " + strings.Join(limits, ", ")
}

func readSchemaDescription(schema *openapi3.Schema) string {
	if schema == nil {
		return ""
	}

	return schema.Description
}
