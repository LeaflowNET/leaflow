package leaflow

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/LeaflowNET/leaflow/pkg/naming"
	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/LeaflowNET/leaflow/pkg/transport"
	"github.com/LeaflowNET/leaflow/pkg/validate"
)

var ErrUnknownArgument = errors.New("unknown argument")

// Operation is one contract operation: what it accepts, and how a call against
// it becomes a request.
//
// The parameter split is the contract's, taken from spec.Inputs — the same
// split the command tree works from. Which parameters live in the path, in what
// order, and which are query are facts about the contract, and deriving them
// twice is how two surfaces start disagreeing about one operation.
type Operation struct {
	spec   *spec.Operation
	inputs *spec.Inputs

	// bodyKey is the property a request body arrives under.
	//
	// Parameter names are the contract's and cannot move, so when one of them is
	// already called "body" it is this invented name that gives way. The
	// alternative — renaming the contract's parameter — would mean a field was
	// spelled one way at the top level and another inside the body.
	bodyKey string
}

func newOperation(op *spec.Operation) *Operation {
	inputs := op.Inputs()

	taken := map[string]bool{}

	for _, parameter := range inputs.Path {
		taken[parameter.Name] = true
	}

	for _, parameter := range inputs.Query {
		taken[parameter.Name] = true
	}

	return &Operation{
		spec:    op,
		inputs:  inputs,
		bodyKey: pickBodyKey(taken),
	}
}

func pickBodyKey(taken map[string]bool) string {
	for _, candidate := range []string{"body", "requestBody", "request_body"} {
		if !taken[candidate] {
			return candidate
		}
	}

	name := "body_"
	for taken[name] {
		name += "_"
	}

	return name
}

// toolName is the operation's name on this surface: `compute-create-disk` for
// what the command line spells `leaflow compute create-disk`.
//
// Both are the contract's operationId under the CLI's one naming rule, so a
// tool name cannot drift from a command name — and neither can change without
// changing the identifier the SDKs generate their method names from.
func (o *Operation) toolName() string {
	return naming.Kebab(o.spec.Service) + "-" + naming.Kebab(o.spec.ID)
}

// inputSchema states everything the operation accepts, in one object.
//
// Path and query parameters sit at the top level under the contract's own
// names; the request body arrives whole, with its nesting intact. The command
// line has to flatten a body into one flag per field because a shell has no
// way to type an object — here the argument is already JSON, so the contract's
// schema is handed over as it stands and nothing has to be reassembled.
func (o *Operation) Schema() map[string]any {
	properties := map[string]any{}

	var required []string

	for _, parameter := range o.inputs.Path {
		properties[parameter.Name] = renderParameter(parameter, "path")
		required = append(required, parameter.Name)
	}

	for _, parameter := range o.inputs.Query {
		properties[parameter.Name] = renderParameter(parameter, "query")

		if parameter.Required {
			required = append(required, parameter.Name)
		}
	}

	if o.inputs.Body != nil {
		properties[o.bodyKey] = convertSchema(o.inputs.Body)

		if o.spec.RequestBody != nil && o.spec.RequestBody.Required {
			required = append(required, o.bodyKey)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
		// Closed, so that a misspelled field is refused here with the name of the
		// field, rather than being dropped silently and leaving a caller to
		// wonder why the filter it passed had no effect.
		"additionalProperties": false,
	}

	if len(required) > 0 {
		sort.Strings(required)

		schema["required"] = required
	}

	return schema
}

// describeParameter renders one parameter, saying where it goes.
//
// A caller cannot tell a path parameter from a query one by name, and the
// difference matters: the first identifies the thing being acted on, the second
// usually narrows a list.
func renderParameter(parameter *openapi3.Parameter, in string) map[string]any {
	schema := map[string]any{}

	if parameter.Schema != nil && parameter.Schema.Value != nil {
		schema = convertSchema(parameter.Schema.Value)
	}

	description := strings.TrimSpace(parameter.Description)

	if existing, ok := schema["description"].(string); ok && description == "" {
		description = existing
	}

	if parameter.Deprecated {
		description = strings.TrimSpace("(deprecated) " + description)
	}

	if description != "" {
		description += "\n\n"
	}

	schema["description"] = description + in + " parameter"

	return schema
}

// request turns a call's arguments into the request to send.
//
// Every problem is reported at once rather than one per attempt: a caller that
// has to fix three fields in three round trips is three round trips slower, and
// on this surface each of those is a model turn.
func (o *Operation) Request(arguments map[string]any) (*transport.Request, error) {
	var problems []string

	problems = append(problems, o.findUnknownArguments(arguments)...)

	path := o.spec.Path

	for _, parameter := range o.inputs.Path {
		value, ok := arguments[parameter.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s is required", parameter.Name))

			continue
		}

		schema := readParameterSchema(parameter)

		if err := validate.Value(schema, value, pointAtArgument(parameter.Name)); err != nil {
			problems = appendProblems(problems, err)

			continue
		}

		literal, ok := formatScalar(value)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s must be a single value", parameter.Name))

			continue
		}

		// Escaped, not concatenated: a value containing / or a space would
		// otherwise point the request somewhere else.
		path = strings.ReplaceAll(path, "{"+parameter.Name+"}", url.PathEscape(literal))
	}

	query := url.Values{}

	for _, parameter := range o.inputs.Query {
		value, ok := arguments[parameter.Name]
		if !ok {
			// Only what was actually given is sent: `name: ""` filters on an empty
			// name, while omitting it does not filter on name at all.
			if parameter.Required {
				problems = append(problems, fmt.Sprintf("%s is required", parameter.Name))
			}

			continue
		}

		schema := readParameterSchema(parameter)

		if err := validate.Value(schema, value, pointAtArgument(parameter.Name)); err != nil {
			problems = appendProblems(problems, err)

			continue
		}

		values, err := queryValues(parameter.Name, value)
		if err != nil {
			problems = appendProblems(problems, err)

			continue
		}

		query[parameter.Name] = values
	}

	body, given := arguments[o.bodyKey]

	switch {
	case given && o.inputs.Body != nil:
		if err := validate.Value(o.inputs.Body, body, pointIntoBody(o.bodyKey)); err != nil {
			problems = appendProblems(problems, err)
		}

	case !given && o.spec.RequiresBody():
		problems = append(problems, fmt.Sprintf("%s is required", o.bodyKey))

	case !given:
		// An optional body that was not supplied is omitted rather than sent as
		// {}, which some operations would read as an explicit clear.
		body = nil
	}

	if len(problems) > 0 {
		sort.Strings(problems)

		return nil, &validate.Error{
			Problems: problems,
		}
	}

	return &transport.Request{
		Operation: o.spec,
		Path:      path,
		Query:     query,
		Body:      body,
	}, nil
}

// unknownArguments names what the operation does not accept. The tool's schema
// already says additionalProperties is false, but nothing in the protocol makes
// a client enforce it, and a silently ignored argument is a call that appears
// to have worked.
//
// Reported as prose rather than through ErrUnknownArgument, because these join
// every other problem in one validate.Error and a sentinel cannot survive being
// merged with three others. Wrapping one would only make errors.Is look like it
// should work here.
func (o *Operation) findUnknownArguments(arguments map[string]any) []string {
	known := map[string]bool{}

	for _, parameter := range o.inputs.Path {
		known[parameter.Name] = true
	}

	for _, parameter := range o.inputs.Query {
		known[parameter.Name] = true
	}

	if o.inputs.Body != nil {
		known[o.bodyKey] = true
	}

	var problems []string

	for name := range arguments {
		if !known[name] {
			problems = append(problems, fmt.Sprintf("unknown argument: %s; accepts %s",
				name, strings.Join(sortKeys(known), ", ")))
		}
	}

	return problems
}

func sortKeys(values map[string]bool) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// queryValues flattens one argument into what goes on the wire. A list becomes
// one entry per item, which is how OpenAPI's default `explode: true` says an
// array parameter is serialised.
func queryValues(name string, value any) ([]string, error) {
	if list, ok := value.([]any); ok {
		values := make([]string, 0, len(list))

		for _, item := range list {
			literal, ok := formatScalar(item)
			if !ok {
				return nil, fmt.Errorf("%s must be a list of single values", name)
			}

			values = append(values, literal)
		}

		return values, nil
	}

	literal, ok := formatScalar(value)
	if !ok {
		return nil, fmt.Errorf("%s must be a single value", name)
	}

	return []string{literal}, nil
}

// scalar renders a JSON value as the text a URL carries. Whole numbers are
// written without a fractional part: every JSON number decodes to float64, and
// a page number sent as "2.0" is not a page number the server recognises.
func formatScalar(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10), true
		}

		return strconv.FormatFloat(typed, 'f', -1, 64), true
	default:
		return "", false
	}
}

func readParameterSchema(parameter *openapi3.Parameter) *openapi3.Schema {
	if parameter == nil || parameter.Schema == nil {
		return nil
	}

	return parameter.Schema.Value
}

// nameLabel points a validation failure at the argument that caused it, under
// the name the caller used.
func pointAtArgument(name string) validate.Labeler {
	return func(path []string) string {
		if len(path) == 0 {
			return name
		}

		return name + "." + strings.Join(path, ".")
	}
}

func pointIntoBody(key string) validate.Labeler {
	return func(path []string) string {
		if len(path) == 0 {
			return key
		}

		return key + "." + strings.Join(path, ".")
	}
}

func appendProblems(dst []string, err error) []string {
	var nested *validate.Error
	if errors.As(err, &nested) {
		return append(dst, nested.Problems...)
	}

	return append(dst, err.Error())
}
