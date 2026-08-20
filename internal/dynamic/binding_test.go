package dynamic

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/internal/spec"
)

func load(t *testing.T) *spec.Set {
	t.Helper()

	specs, err := spec.Load()
	if err != nil {
		t.Fatalf("loading contracts: %v", err)
	}

	return specs
}

func operation(t *testing.T, service, id string) *spec.Operation {
	t.Helper()

	svc, ok := load(t).Service(service)
	if !ok {
		t.Fatalf("no %s contract", service)
	}

	op, ok := svc.Operation(id)
	if !ok {
		t.Fatalf("%s has no operation %q", service, id)
	}

	return op
}

func TestBindSplitsParametersByLocation(t *testing.T) {
	binding := Bind(operation(t, "compute", "list-instance-disks"))

	if len(binding.Path) != 1 || binding.Path[0].Name != "instanceId" {
		t.Fatalf("path parameters = %v, want [instanceId]", names(binding.Path))
	}

	if binding.Body != nil {
		t.Errorf("a GET should have no request body")
	}
}

func TestBindOrdersPathParametersByPath(t *testing.T) {
	// /instances/{instanceId}/disks/{diskId}: reversing these would swap two
	// UUIDs and the server could only answer 404.
	binding := Bind(operation(t, "compute", "detach-disk"))

	got := names(binding.Path)
	want := []string{"instanceId", "diskId"}

	if len(got) != len(want) {
		t.Fatalf("path parameters = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path parameters = %v, want %v", got, want)
		}
	}
}

// Some contracts declare Authorization as an explicit required header
// parameter. Left alone it would become a --authorization flag the user has to
// fill in by hand.
func TestBindDropsTheAuthorizationHeader(t *testing.T) {
	binding := Bind(operation(t, "account", "list-projects"))

	for _, parameter := range binding.Query {
		if strings.EqualFold(parameter.Name, "authorization") {
			t.Fatalf("Authorization was exposed as a flag")
		}
	}
}

func TestBindFlattensScalarBodyFields(t *testing.T) {
	binding := Bind(operation(t, "compute", "create-disk"))

	found := map[string]bool{}
	for _, field := range binding.Fields {
		found[field.Flag] = true
	}

	for _, flag := range []string{"name", "size-gb", "disk-type-id", "snapshot-id"} {
		if !found[flag] {
			t.Errorf("--%s was not created; got %v", flag, found)
		}
	}

	// size_gb must reach the command line as --size-gb.
	if found["size_gb"] {
		t.Errorf("underscored flag name leaked through")
	}
}

func TestValuesRejectsInvalidArgumentsBeforeSending(t *testing.T) {
	binding := Bind(operation(t, "compute", "create-disk"))

	cmd := &cobra.Command{Use: "create-disk"}
	binding.Register(cmd)

	if err := cmd.Flags().Set("name", strings.Repeat("x", 200)); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Flags().Set("size-gb", "0"); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Flags().Set("disk-type-id", "not-a-uuid"); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := binding.Values(cmd, nil)
	if err == nil {
		t.Fatal("expected validation to fail")
	}

	// Every problem at once, named by the flag the user typed.
	for _, want := range []string{"--name", "--size-gb", "--disk-type-id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestValuesAcceptsValidArguments(t *testing.T) {
	binding := Bind(operation(t, "compute", "create-disk"))

	cmd := &cobra.Command{Use: "create-disk"}
	binding.Register(cmd)

	for flag, value := range map[string]string{
		"name":         "data",
		"size-gb":      "20",
		"disk-type-id": "6f1e4b7a-2c3d-4e5f-8a9b-0c1d2e3f4a5b",
	} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}

	_, _, body, err := binding.Values(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected validation failure: %v", err)
	}

	object, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body = %T, want map", body)
	}

	// Integers must leave as JSON numbers, not strings.
	if size, isNumber := object["size_gb"].(float64); !isNumber || size != 20 {
		t.Errorf("size_gb = %#v, want 20 as a number", object["size_gb"])
	}

	// Untouched optional fields must not be sent at all.
	if _, present := object["snapshot_id"]; present {
		t.Errorf("an unset optional field was included")
	}
}

func TestBodyFlagsAndBodyAreExclusive(t *testing.T) {
	binding := Bind(operation(t, "compute", "create-disk"))

	cmd := &cobra.Command{Use: "create-disk"}
	binding.Register(cmd)

	if err := cmd.Flags().Set("body", `{"name":"x"}`); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Flags().Set("name", "y"); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := binding.Values(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--body") {
		t.Fatalf("expected the two to be rejected together, got %v", err)
	}
}

// Nullable fields are `type: [string, "null"]` in OpenAPI 3.1. Reading only the
// first entry of a Types slice, or using kin-openapi's Type.Is, would treat
// every nullable integer as a string and send "20" instead of 20.
func TestPrimaryTypeIgnoresNull(t *testing.T) {
	specs := load(t)

	svc, _ := specs.Service("compute")

	op, ok := svc.Operation("list-disks")
	if !ok {
		t.Fatal("compute has no list-disks")
	}

	for _, ref := range op.Parameters {
		if ref == nil || ref.Value == nil || ref.Value.Schema == nil {
			continue
		}

		if got := primaryType(ref.Value.Schema.Value); got == "null" {
			t.Errorf("parameter %s resolved to the null type", ref.Value.Name)
		}
	}
}

func names(parameters []*openapi3.Parameter) []string {
	out := make([]string, 0, len(parameters))
	for _, parameter := range parameters {
		out = append(out, parameter.Name)
	}

	return out
}
