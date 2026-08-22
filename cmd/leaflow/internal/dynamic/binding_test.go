package dynamic

import (
	"errors"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/pkg/spec"
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

// A body field named "body" wants the flag that carries the whole request, and
// cobra panics on the second registration of a name. That is not a monitoring
// problem: the tree is built before anything runs, so `leaflow --version` died
// too, on every machine, the moment the contract adding the field landed.
func TestBodyFieldNeverTakesTheBodyFlag(t *testing.T) {
	binding := Bind(operation(t, "monitoring", "post-status-page-incident-update"))

	var field *BodyField

	for _, candidate := range binding.Fields {
		if candidate.Name == "body" {
			field = candidate
		}
	}

	if field == nil {
		t.Fatal("this operation no longer has a body field named body; point the test at one that does")
	}

	if field.Flag == "body" {
		t.Fatalf("the field took --body, which carries the whole request as JSON")
	}

	cmd := &cobra.Command{
		Use: "post-status-page-incident-update",
	}
	binding.Register(cmd)

	if err := cmd.Flags().Set(field.Flag, "we are investigating"); err != nil {
		t.Fatal(err)
	}

	_, _, body, err := binding.Values(cmd, []string{"00000000-0000-0000-0000-000000000000"})
	if err != nil {
		t.Fatalf("collecting values: %v", err)
	}

	// The flag was renamed, not the field: the request still says "body".
	object, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("body = %T, want an object", body)
	}

	if object["body"] != "we are investigating" {
		t.Errorf("body = %v, want the text under the contract's own key", object)
	}
}

// Registration happens while the tree is built, which is before a command line
// is even parsed. Nothing else in this package's tests builds the whole tree, so
// a duplicate flag name reached users through a contract sync — `go test`,
// which is what guards that sync, had nothing to say about it.
func TestEveryOperationRegistersItsFlags(t *testing.T) {
	defer func() {
		if problem := recover(); problem != nil {
			t.Fatalf("building the command tree panicked: %v", problem)
		}
	}()

	if len(Build(load(t), &Runtime{})) == 0 {
		t.Fatal("no commands were built")
	}
}

func TestValuesRejectsInvalidArgumentsBeforeSending(t *testing.T) {
	binding := Bind(operation(t, "compute", "create-disk"))

	cmd := &cobra.Command{
		Use: "create-disk",
	}
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

	cmd := &cobra.Command{
		Use: "create-disk",
	}
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

	cmd := &cobra.Command{
		Use: "create-disk",
	}
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

		if got := readPrimaryType(ref.Value.Schema.Value); got == "null" {
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

// "accepts 1 arg(s), received 0" says how many are missing, not what they are.
// Everything reported here comes from the contract.
func TestMissingArgumentsAreNamed(t *testing.T) {
	binding := Bind(operation(t, "compute", "detach-disk"))

	cmd := &cobra.Command{
		Use: "detach-disk",
	}
	binding.Register(cmd)

	err := cmd.Args(cmd, nil)
	if err == nil {
		t.Fatal("expected missing arguments to be rejected")
	}

	if !errors.Is(err, ErrMissingArguments) {
		t.Errorf("err = %v, want ErrMissingArguments", err)
	}

	for _, want := range []string{"<instance-id>", "<disk-id>", "uuid"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

// A format is more useful than a bare type: knowing an argument is a UUID is
// what stops someone passing a name.
func TestArgumentHelpStatesTypesAndLimits(t *testing.T) {
	uuidArg := describeArguments(Bind(operation(t, "compute", "get-instance")))
	if !strings.Contains(uuidArg, "uuid") {
		t.Errorf("expected a uuid format: %s", uuidArg)
	}

	bounded := describeArguments(Bind(operation(t, "canopy", "get-model")))
	if !strings.Contains(bounded, "at most 128 characters") {
		t.Errorf("expected the length limit: %s", bounded)
	}
}

// Filenames are never a valid id, and offering them answers a question nobody
// asked.
func TestPositionalArgsDoNotCompleteFilenames(t *testing.T) {
	cmd := NewCommand("get-instance", operation(t, "compute", "get-instance"), &Runtime{})

	if cmd.ValidArgsFunction == nil {
		t.Fatal("no completion function was set")
	}

	values, directive := cmd.ValidArgsFunction(cmd, nil, "")

	if len(values) != 0 {
		t.Errorf("unexpected suggestions: %v", values)
	}

	if directive&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Errorf("directive = %v, want NoFileComp", directive)
	}
}
