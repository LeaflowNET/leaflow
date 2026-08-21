package validate_test

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/LeaflowNET/leaflow/pkg/validate"
)

func schemaOf(t *testing.T, definition string) *openapi3.Schema {
	t.Helper()

	loader := openapi3.NewLoader()

	doc, err := loader.LoadFromData([]byte(`
openapi: 3.1.0
info: {title: t, version: "1"}
paths: {}
components:
  schemas:
    Subject:
` + definition))
	if err != nil {
		t.Fatalf("loading schema: %v", err)
	}

	return doc.Components.Schemas["Subject"].Value
}

func flat(path []string) string {
	if len(path) == 0 {
		return "value"
	}

	return "--" + strings.Join(path, ".")
}

// The platform issues UUIDv7. kin-openapi's built-in pattern only allows
// versions 1 to 5, so validating against it rejects every id the platform hands
// out — a local check stricter than the server, which is worse than no check.
func TestUUIDAcceptsEveryVersion(t *testing.T) {
	schema := schemaOf(t, "      type: string\n      format: uuid\n")

	for _, id := range []string{
		"01a00c9b-d25a-7618-b9c7-42174dc667b4", // v7, as issued by the platform
		"019fb8c2-cde8-7a55-b5e5-a4538cf2597a", // v7
		"6f1e4b7a-2c3d-4e5f-8a9b-0c1d2e3f4a5b", // v4
		"00000000-0000-0000-0000-000000000000", // nil
	} {
		if err := validate.Value(schema, id, flat); err != nil {
			t.Errorf("valid uuid %q was rejected: %v", id, err)
		}
	}
}

// What is still worth catching: a name, a truncated id, an unexpanded variable.
func TestUUIDStillRejectsGarbage(t *testing.T) {
	schema := schemaOf(t, "      type: string\n      format: uuid\n")

	for _, bad := range []string{
		"not-an-id",
		"01a00c9b-d25a-7618-b9c7",
		"$INSTANCE_ID",
		"",
	} {
		if err := validate.Value(schema, bad, flat); err == nil {
			t.Errorf("invalid uuid %q was accepted", bad)
		}
	}
}

func TestReportsEveryProblemAtOnce(t *testing.T) {
	schema := schemaOf(t, `      type: object
      required: [name, size_gb]
      properties:
        name:
          type: string
          maxLength: 8
        size_gb:
          type: integer
          minimum: 1
`)

	err := validate.Value(schema, map[string]any{
		"name":    "far-too-long-a-name",
		"size_gb": float64(0),
	}, flat)
	if err == nil {
		t.Fatal("expected validation to fail")
	}

	// Both, not just the first: fixing one at a time costs a round trip each.
	for _, want := range []string{"--name", "--size_gb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestMissingRequiredFieldIsNamed(t *testing.T) {
	schema := schemaOf(t, `      type: object
      required: [disk_type_id]
      properties:
        disk_type_id:
          type: string
`)

	err := validate.Value(schema, map[string]any{}, flat)
	if err == nil {
		t.Fatal("expected validation to fail")
	}

	if !strings.Contains(err.Error(), "disk_type_id") {
		t.Errorf("the missing field was not named: %v", err)
	}
}

func TestEnumListsTheOptions(t *testing.T) {
	schema := schemaOf(t, `      type: string
      enum: [start, stop, reboot]
`)

	err := validate.Value(schema, "explode", flat)
	if err == nil {
		t.Fatal("expected validation to fail")
	}

	for _, want := range []string{"start", "stop", "reboot"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not offer %q: %v", want, err)
		}
	}
}

// maxLength counts characters, and reporting bytes would make someone with a
// CJK name think they had miscounted.
func TestLengthIsCountedInCharacters(t *testing.T) {
	schema := schemaOf(t, "      type: string\n      maxLength: 4\n")

	err := validate.Value(schema, "五个中文字", flat)
	if err == nil {
		t.Fatal("expected validation to fail")
	}

	if !strings.Contains(err.Error(), "got 5") {
		t.Errorf("expected a character count of 5: %v", err)
	}
}

// Nullable fields are `type: [string, "null"]` in OpenAPI 3.1.
func TestNullableFieldsAcceptNull(t *testing.T) {
	schema := schemaOf(t, "      type: [string, \"null\"]\n")

	if err := validate.Value(schema, nil, flat); err != nil {
		t.Errorf("null was rejected by a nullable field: %v", err)
	}
}
