package spec

import (
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Inputs is what an operation accepts, split the way the contract states it.
//
// This is a fact about the contract, not a decision about any one caller: which
// parameters live in the path, in what order, and which are query. Both the
// command tree and the tool surface need exactly this split, and deriving it
// twice is how two surfaces start disagreeing about one operation.
//
// What each caller does with it differs and stays with the caller. A shell has
// no way to type an object, so the command tree flattens a body into one flag
// per field; a tool call is already JSON and hands the schema over as it is.
type Inputs struct {
	// Path is ordered by the path itself. Reversed, `detach-disk <a> <b>` swaps
	// two UUIDs and the server can only answer 404.
	Path []*openapi3.Parameter

	Query []*openapi3.Parameter

	// Body is the application/json schema, or nil when the operation takes none.
	Body *openapi3.Schema
}

var placeholder = regexp.MustCompile(`\{([^}]+)\}`)

// Inputs splits the operation's parameters. Computed once when the contract is
// read, because every caller wants the same answer and the walk is not free.
func (o *Operation) Inputs() *Inputs {
	return o.inputs
}

func newInputs(op *Operation) *Inputs {
	inputs := &Inputs{}

	byName := map[string]*openapi3.Parameter{}

	for _, ref := range op.Parameters {
		if ref == nil || ref.Value == nil {
			continue
		}

		parameter := ref.Value

		switch parameter.In {
		case openapi3.ParameterInPath:
			byName[parameter.Name] = parameter
		case openapi3.ParameterInQuery:
			inputs.Query = append(inputs.Query, parameter)
		case openapi3.ParameterInHeader:
			// Some contracts declare Authorization as an explicit parameter on every
			// operation. It is supplied by the transport and must never become
			// something a caller is asked to fill in.
			if strings.EqualFold(parameter.Name, "authorization") {
				continue
			}

			inputs.Query = append(inputs.Query, parameter)
		}
	}

	for _, match := range placeholder.FindAllStringSubmatch(op.Path, -1) {
		if parameter, ok := byName[match[1]]; ok {
			inputs.Path = append(inputs.Path, parameter)
		}
	}

	sort.SliceStable(inputs.Query, func(i, j int) bool {
		return inputs.Query[i].Name < inputs.Query[j].Name
	})

	inputs.Body = bodySchema(op)

	return inputs
}

func bodySchema(op *Operation) *openapi3.Schema {
	if op.RequestBody == nil {
		return nil
	}

	media := op.RequestBody.Content.Get("application/json")
	if media == nil || media.Schema == nil {
		return nil
	}

	return media.Schema.Value
}

// RequiresBody reports whether the contract insists on one.
func (o *Operation) RequiresBody() bool {
	return o.inputs.Body != nil && o.RequestBody != nil && o.RequestBody.Required
}
