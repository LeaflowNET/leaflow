// Package validate checks arguments against the contract before a request goes
// out.
//
// This is the most direct payoff of reading OpenAPI rather than linking a
// generated SDK. In generated Go a field is just `Name string`; it does not
// know it is capped at 128 characters, and `DiskTypeId string` does not know it
// must be a UUID. Those constraints exist only in the contract.
//
//	$ leaflow compute disk create --name <129 chars> --size-gb 0
//	error: --name is longer than 128 characters (got 129)
//	       --size-gb must be at least 1 (got 0)
//
// The alternative is a 400 written for API callers, which does not contain the
// word "--size-gb" and has to be translated back to the flag by hand.
package validate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/LeaflowNET/leaflow/pkg/transport"
)

// anyUUID accepts every UUID version, unlike kin-openapi's built-in pattern,
// which only allows versions 1 to 5 as RFC 4122 defined them.
//
// The platform issues UUIDv7. Validating against the built-in pattern rejects
// every id the platform hands out — the local check would be stricter than the
// server and refuse valid input, which is worse than not checking at all.
//
// What is still worth catching is the actual mistake: a name, a truncated id, a
// shell variable that did not expand.
const anyUUID = `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`

func init() {
	// kin-openapi registers only date, date-time, byte, int32 and int64 by
	// default. `format: uuid` is by far the most common one in these contracts —
	// every resource id — and without this a malformed id travels to the server
	// to come back as a 400 that was visible locally.
	openapi3.DefineStringFormatValidator("uuid",
		openapi3.NewRegexpFormatValidator(anyUUID))
}

// Labeler translates a JSON field path into the name the user typed. The
// validator knows `/size_gb`; the user typed `--size-gb`.
type Labeler func(path []string) string

// Error carries every problem at once. Reporting one at a time would make
// someone fix three mistakes in three round trips.
type Error struct {
	Problems []string
}

// Unwrap places a validation failure among the transport's kinds, so that one
// question — "were the arguments wrong?" — has one answer whether the contract
// caught it here or the service caught it there.
func (e *Error) Unwrap() error {
	return transport.ErrInvalidArgument
}

func (e *Error) Error() string {
	if len(e.Problems) == 1 {
		return e.Problems[0]
	}

	return strings.Join(e.Problems, "\n       ")
}

func Value(schema *openapi3.Schema, value any, label Labeler) error {
	if schema == nil {
		return nil
	}

	err := schema.VisitJSON(value, openapi3.MultiErrors())
	if err == nil {
		return nil
	}

	problems := collect(err, label)
	if len(problems) == 0 {
		return nil
	}

	sort.Strings(problems)

	return &Error{
		Problems: problems,
	}
}

func collect(err error, label Labeler) []string {
	var problems []string

	switch typed := err.(type) {
	case openapi3.MultiError:
		for _, one := range typed {
			problems = append(problems, collect(one, label)...)
		}
	case *openapi3.SchemaError:
		problems = append(problems, describe(typed, label))
	default:
		problems = append(problems, err.Error())
	}

	return problems
}

// describe rewrites one SchemaError for someone typing a command. Raw, it
// arrives with the entire schema attached, which is written for whoever is
// reading the code.
func describe(err *openapi3.SchemaError, label Labeler) string {
	name := label(err.JSONPointer())
	schema := err.Schema

	switch err.SchemaField {
	case "required":
		if missing := readMissingField(err.Reason); missing != "" {
			return fmt.Sprintf("%s is required", label([]string{missing}))
		}

		return fmt.Sprintf("%s is missing a required field", name)

	case "maxLength":
		if schema != nil && schema.MaxLength != nil {
			return fmt.Sprintf("%s is longer than %d characters (got %d)",
				name, *schema.MaxLength, countRunes(err.Value))
		}

	case "minLength":
		if schema != nil {
			return fmt.Sprintf("%s is shorter than %d characters (got %d)",
				name, schema.MinLength, countRunes(err.Value))
		}

	case "minimum":
		if schema != nil && schema.Min != nil {
			return fmt.Sprintf("%s must be at least %v (got %v)", name, plain(*schema.Min), err.Value)
		}

	case "maximum":
		if schema != nil && schema.Max != nil {
			return fmt.Sprintf("%s must be at most %v (got %v)", name, plain(*schema.Max), err.Value)
		}

	case "enum":
		if schema != nil {
			options := make([]string, 0, len(schema.Enum))
			for _, option := range schema.Enum {
				options = append(options, fmt.Sprint(option))
			}

			return fmt.Sprintf("%s must be one of %s (got %v)", name, strings.Join(options, ", "), err.Value)
		}

	case "format":
		if schema != nil {
			return fmt.Sprintf("%s is not a valid %s: %v", name, schema.Format, err.Value)
		}

	case "type":
		if schema != nil {
			return fmt.Sprintf("%s must be %s (got %v)", name, typeName(schema), err.Value)
		}

	case "pattern":
		return fmt.Sprintf("%s has the wrong format: %v", name, err.Value)

	case "additionalProperties":
		return fmt.Sprintf("%s is not a field this operation accepts", name)
	}

	// Whatever the constraint, the message has to name the offending argument;
	// a bare reason leaves the user guessing which one to change.
	if err.Reason != "" {
		return fmt.Sprintf("%s %s", name, err.Reason)
	}

	return fmt.Sprintf("%s is invalid", name)
}

// readMissingField extracts the name from `property "disk_type_id" is missing`.
// Parsing another library's prose is fragile by nature, so the caller above has
// a fallback rather than emitting an empty name.
func readMissingField(reason string) string {
	const prefix = `property "`

	_, after, ok := strings.Cut(reason, prefix)
	if !ok {
		return ""
	}

	rest := after

	before, _, ok := strings.Cut(rest, `"`)
	if !ok {
		return ""
	}

	return before
}

// countRunes counts characters, not bytes: maxLength in the contract is in
// characters, and reporting bytes would make people think they miscounted.
func countRunes(value any) int {
	if text, ok := value.(string); ok {
		return len([]rune(text))
	}

	return 0
}

func plain(value float64) any {
	if value == float64(int64(value)) {
		return int64(value)
	}

	return value
}

func typeName(schema *openapi3.Schema) string {
	if schema.Type == nil {
		return "another type"
	}

	for _, one := range *schema.Type {
		switch one {
		case "string":
			return "a string"
		case "integer":
			return "an integer"
		case "number":
			return "a number"
		case "boolean":
			return "true or false"
		case "array":
			return "a list"
		case "object":
			return "an object"
		}
	}

	return "another type"
}
