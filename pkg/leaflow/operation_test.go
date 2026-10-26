package leaflow

import (
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/LeaflowNET/leaflow/pkg/spec"
)

func TestRequestRoutesHeaderAndQueryParameters(t *testing.T) {
	operation := &Operation{
		spec: &spec.Operation{
			ID:      "create-resource",
			Service: "example",
			Method:  http.MethodPost,
			Path:    "/resources",
		},
		bodyKey: "body",
		inputs: &spec.Inputs{Body: openapi3.NewObjectSchema(), Query: []*openapi3.Parameter{
			{
				Name:   "Idempotency-Key",
				In:     openapi3.ParameterInHeader,
				Schema: openapi3.NewSchemaRef("", openapi3.NewStringSchema()),
			},
			{
				Name:   "region",
				In:     openapi3.ParameterInQuery,
				Schema: openapi3.NewSchemaRef("", openapi3.NewStringSchema()),
			},
		}},
	}

	request, err := operation.Request(map[string]any{
		"Idempotency-Key": "synthetic-key",
		"region":          "test-region",
		"body":            map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := request.Header.Get("Idempotency-Key"); got != "synthetic-key" {
		t.Fatalf("header = %q, want %q", got, "synthetic-key")
	}
	if request.Query.Has("Idempotency-Key") {
		t.Fatal("header parameter was added to the query")
	}
	if got := request.Query.Get("region"); got != "test-region" {
		t.Fatalf("query region = %q, want %q", got, "test-region")
	}
	if request.Header.Get("region") != "" {
		t.Fatal("query parameter was added to the headers")
	}
}

func TestSchemaDescribesParameterLocation(t *testing.T) {
	operation := &Operation{
		spec: &spec.Operation{ID: "create-resource", Service: "example"},
		inputs: &spec.Inputs{Query: []*openapi3.Parameter{
			{
				Name:   "Idempotency-Key",
				In:     openapi3.ParameterInHeader,
				Schema: openapi3.NewSchemaRef("", openapi3.NewStringSchema()),
			},
		}},
	}

	schema := operation.Schema()
	properties := schema["properties"].(map[string]any)
	header := properties["Idempotency-Key"].(map[string]any)
	if got := header["description"]; got != "header parameter" {
		t.Fatalf("header description = %q, want %q", got, "header parameter")
	}
}
