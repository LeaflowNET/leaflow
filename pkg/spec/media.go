package spec

import (
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// FileFields identifies multipart file properties; the bool distinguishes repeated files.
func (o *Operation) FileFields() map[string]bool {
	fields := map[string]bool{}
	if o.inputs.ContentType != "multipart/form-data" {
		return fields
	}
	seen := map[*openapi3.Schema]bool{}
	var visit func(*openapi3.Schema)
	visit = func(schema *openapi3.Schema) {
		if schema == nil || seen[schema] {
			return
		}
		seen[schema] = true
		for name, ref := range schema.Properties {
			if ref == nil || ref.Value == nil {
				continue
			}
			value := ref.Value
			if value.Format == "binary" {
				fields[name] = false
			}
			if value.Type.Is("array") && value.Items != nil && value.Items.Value != nil && value.Items.Value.Format == "binary" {
				fields[name] = true
			}
		}
		for _, ref := range schema.AllOf {
			if ref != nil {
				visit(ref.Value)
			}
		}
	}
	visit(o.inputs.Body)
	return fields
}

func (o *Operation) ResponseMediaTypes() []string {
	types := map[string]bool{}
	if o.Responses != nil {
		for code, ref := range o.Responses.Map() {
			if !strings.HasPrefix(code, "2") || ref == nil || ref.Value == nil {
				continue
			}
			for media := range ref.Value.Content {
				types[media] = true
			}
		}
	}
	result := make([]string, 0, len(types))
	for media := range types {
		result = append(result, media)
	}
	sort.Strings(result)
	return result
}
