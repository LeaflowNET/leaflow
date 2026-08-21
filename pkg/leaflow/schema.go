package leaflow

import (
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// maxDepth bounds how deep a contract schema is written out.
//
// A tool's input schema has no $ref to lean on: the client reads it standalone,
// with no components section to resolve against, so every reference has to be
// inlined where it is used. Without a bound, a self-referential schema — a tree
// of nested rules, a resource that links to its own kind — would expand until
// it ran out of memory rather than until it was finished.
const maxDepth = 12

// convertSchema rewrites a contract schema as a JSON Schema document a client
// can read on its own.
//
// It is a translation, not a summary: every constraint the contract states is
// carried across, because those constraints are what let a client get the call
// right on the first attempt instead of learning the rules from a 400.
//
// Keywords are copied selectively rather than by marshalling the contract
// schema straight through. openapi3.SchemaRef marshals as {"$ref": "..."} when
// it came from a reference, and a client resolving that against nothing would
// see an untyped field where the contract was perfectly specific.
func convertSchema(schema *openapi3.Schema) map[string]any {
	return (&converter{
		open: map[*openapi3.Schema]bool{},
	}).convert(schema, 0)
}

// converter tracks the schemas on the current path so a cycle is caught the
// second time one is entered rather than the millionth.
type converter struct {
	open map[*openapi3.Schema]bool
}

func (c *converter) convert(schema *openapi3.Schema, depth int) map[string]any {
	if schema == nil {
		return map[string]any{}
	}

	// A truncated branch is described rather than dropped, and described as
	// accepting anything rather than as a type it might not be. Dropping the
	// field would say the operation does not take it; guessing a type would
	// reject values the server accepts, which is worse than not checking.
	if c.open[schema] {
		return renderElision(schema, "this field repeats the structure above; see the contract for its shape")
	}

	if depth >= maxDepth {
		return renderElision(schema, fmt.Sprintf("nested more than %d levels deep; see the contract for its shape", maxDepth))
	}

	c.open[schema] = true
	defer delete(c.open, schema)

	out := map[string]any{}

	c.writeType(out, schema)
	c.writeDocs(out, schema)
	c.writeConstraints(out, schema)
	c.writeComposition(out, schema, depth)
	c.writeChildren(out, schema, depth)

	return out
}

// writeType carries the type across in whichever form the contract used.
//
// OpenAPI 3.1 spells a nullable field as `type: [string, "null"]`, which is
// also how JSON Schema 2020-12 spells it, so the list passes through. The 3.0
// form is a separate `nullable: true`, and is folded into that same list —
// otherwise a nullable field would arrive at the client as non-nullable and an
// explicit null would be rejected before it was ever sent.
func (c *converter) writeType(out map[string]any, schema *openapi3.Schema) {
	if schema.Type == nil || len(*schema.Type) == 0 {
		return
	}

	types := append([]string{}, *schema.Type...)

	if schema.Nullable && !slices.Contains(types, "null") {
		types = append(types, "null")
	}

	if len(types) == 1 {
		out["type"] = types[0]

		return
	}

	out["type"] = types
}

func (c *converter) writeDocs(out map[string]any, schema *openapi3.Schema) {
	description := strings.TrimSpace(schema.Description)

	// Said in the description because JSON Schema's own `deprecated` is widely
	// ignored by clients, and a model choosing between two fields needs to know
	// which one is on the way out.
	if schema.Deprecated {
		description = strings.TrimSpace("(deprecated) " + description)
	}

	if description != "" {
		out["description"] = description
	}

	if schema.Title != "" {
		out["title"] = schema.Title
	}

	if schema.Default != nil {
		out["default"] = schema.Default
	}

	// Normalised onto JSON Schema's plural keyword: OpenAPI's singular `example`
	// is not part of the vocabulary a client validates against.
	switch {
	case len(schema.Examples) > 0:
		out["examples"] = schema.Examples
	case schema.Example != nil:
		out["examples"] = []any{schema.Example}
	}
}

func (c *converter) writeConstraints(out map[string]any, schema *openapi3.Schema) {
	if len(schema.Enum) > 0 {
		out["enum"] = schema.Enum
	}

	if schema.Const != nil {
		out["const"] = schema.Const
	}

	// Kept even though JSON Schema treats format as an annotation: `format:
	// uuid` is how a caller knows an id is not a name, and that is the single
	// most common mistake this surface can prevent.
	if schema.Format != "" {
		out["format"] = schema.Format
	}

	if schema.Pattern != "" {
		out["pattern"] = schema.Pattern
	}

	if schema.MinLength > 0 {
		out["minLength"] = schema.MinLength
	}

	if schema.MaxLength != nil {
		out["maxLength"] = *schema.MaxLength
	}

	if schema.Min != nil {
		out["minimum"] = *schema.Min
	}

	if schema.Max != nil {
		out["maximum"] = *schema.Max
	}

	if schema.MultipleOf != nil {
		out["multipleOf"] = *schema.MultipleOf
	}

	if schema.MinItems > 0 {
		out["minItems"] = schema.MinItems
	}

	if schema.MaxItems != nil {
		out["maxItems"] = *schema.MaxItems
	}

	if schema.UniqueItems {
		out["uniqueItems"] = true
	}

	if schema.MinProps > 0 {
		out["minProperties"] = schema.MinProps
	}

	if schema.MaxProps != nil {
		out["maxProperties"] = *schema.MaxProps
	}
}

func (c *converter) writeComposition(out map[string]any, schema *openapi3.Schema, depth int) {
	for keyword, refs := range map[string]openapi3.SchemaRefs{
		"oneOf": schema.OneOf,
		"anyOf": schema.AnyOf,
		"allOf": schema.AllOf,
	} {
		if converted := c.convertAll(refs, depth); len(converted) > 0 {
			out[keyword] = converted
		}
	}

	if schema.Not != nil && schema.Not.Value != nil {
		out["not"] = c.convert(schema.Not.Value, depth+1)
	}
}

func (c *converter) convertAll(refs openapi3.SchemaRefs, depth int) []map[string]any {
	converted := make([]map[string]any, 0, len(refs))

	for _, ref := range refs {
		if ref == nil || ref.Value == nil {
			continue
		}

		converted = append(converted, c.convert(ref.Value, depth+1))
	}

	return converted
}

func (c *converter) writeChildren(out map[string]any, schema *openapi3.Schema, depth int) {
	if schema.Items != nil && schema.Items.Value != nil {
		out["items"] = c.convert(schema.Items.Value, depth+1)
	}

	if len(schema.Properties) > 0 {
		properties := make(map[string]any, len(schema.Properties))

		for name, ref := range schema.Properties {
			if ref == nil || ref.Value == nil {
				continue
			}

			properties[name] = c.convert(ref.Value, depth+1)
		}

		out["properties"] = properties
	}

	if len(schema.Required) > 0 {
		out["required"] = schema.Required
	}

	switch {
	case schema.AdditionalProperties.Has != nil:
		out["additionalProperties"] = *schema.AdditionalProperties.Has
	case schema.AdditionalProperties.Schema != nil && schema.AdditionalProperties.Schema.Value != nil:
		out["additionalProperties"] = c.convert(schema.AdditionalProperties.Schema.Value, depth+1)
	}
}

// renderElision stands in for a branch that was not written out. It keeps the
// contract's own description, which is usually enough to fill the field in
// correctly, and states plainly why the rest is missing.
func renderElision(schema *openapi3.Schema, reason string) map[string]any {
	description := strings.TrimSpace(schema.Description)
	if description != "" {
		reason = description + "\n\n" + reason
	}

	return map[string]any{
		"description": reason,
	}
}
