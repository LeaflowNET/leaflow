package leaflow

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/LeaflowNET/leaflow/pkg/validate"
	"github.com/getkin/kin-openapi/openapi3"
)

func load(t *testing.T) *spec.Set {
	t.Helper()

	specs, err := spec.Load()
	if err != nil {
		t.Fatalf("loading contracts: %v", err)
	}

	return specs
}

func find(t *testing.T, service, id string) *Operation {
	t.Helper()

	svc, ok := load(t).Service(service)
	if !ok {
		t.Fatalf("no %s contract", service)
	}

	op, ok := svc.Operation(id)
	if !ok {
		t.Fatalf("%s has no operation %q", service, id)
	}

	return newOperation(op)
}

// object walks into a nested schema, failing rather than panicking when a level
// is missing — the assertion is about the shape, so a wrong shape should read
// as a wrong shape.
func object(t *testing.T, value any, path ...string) map[string]any {
	t.Helper()

	current, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %v", value)
	}

	for _, key := range path {
		next, ok := current[key]
		if !ok {
			t.Fatalf("no %q in %v", key, current)
		}

		current, ok = next.(map[string]any)
		if !ok {
			t.Fatalf("%q is not an object: %v", key, next)
		}
	}

	return current
}

// The whole reason for reading OpenAPI rather than hand-writing tools: what the
// contract states about a field has to reach the client, or the client learns
// the rules by failing calls.
func TestInputSchemaCarriesTheContractsConstraints(t *testing.T) {
	schema := find(t, "compute", "create-disk").Schema()

	name := object(t, schema, "properties", "body", "properties", "name")

	if name["maxLength"] == nil || name["minLength"] == nil {
		t.Errorf("name lost its length limits: %v", name)
	}

	diskType := object(t, schema, "properties", "body", "properties", "disk_type_id")

	if diskType["format"] != "uuid" {
		t.Errorf("disk_type_id lost format: uuid, got %v", diskType["format"])
	}

	size := object(t, schema, "properties", "body", "properties", "size_gb")

	if size["minimum"] == nil {
		t.Errorf("size_gb lost its minimum: %v", size)
	}

	body := object(t, schema, "properties", "body")

	if required, ok := body["required"].([]string); !ok || len(required) == 0 {
		t.Errorf("the body's required fields did not survive: %v", body["required"])
	}
}

// Path parameters are the operation's subject and query parameters usually
// narrow a list; a caller cannot tell them apart by name.
func TestInputSchemaSaysWhereAParameterGoes(t *testing.T) {
	schema := find(t, "compute", "list-instance-disks").Schema()

	instance := object(t, schema, "properties", "instanceId")

	if description, _ := instance["description"].(string); !strings.Contains(description, "path parameter") {
		t.Errorf("instanceId does not say it is a path parameter: %v", instance["description"])
	}
}

// A tool takes `diskTypeId`, not `--disk-type-id`. The payload is JSON, and the
// body's own fields have to be spelled the contract's way whatever happens at
// the top level.
func TestInputSchemaUsesTheContractsNames(t *testing.T) {
	schema := find(t, "compute", "detach-disk").Schema()

	properties := object(t, schema, "properties")

	for _, want := range []string{"instanceId", "diskId"} {
		if _, ok := properties[want]; !ok {
			t.Errorf("no %q; got %v", want, sortedNames(properties))
		}
	}

	if _, kebab := properties["instance-id"]; kebab {
		t.Errorf("a parameter was renamed for the shell: %v", sortedNames(properties))
	}
}

func TestRequestFillsThePath(t *testing.T) {
	op := find(t, "compute", "get-disk")

	request, err := op.Request(map[string]any{
		"diskId": "019fb8c2-cde8-7a55-b5e5-a4538cf2597a",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	if strings.Contains(request.Path, "{") {
		t.Errorf("path still has a placeholder: %s", request.Path)
	}

	if !strings.HasSuffix(request.Path, "019fb8c2-cde8-7a55-b5e5-a4538cf2597a") {
		t.Errorf("path = %s, want it to end in the id", request.Path)
	}
}

// Every problem at once. One per attempt would be one model turn per mistake.
func TestRequestReportsEveryProblemTogether(t *testing.T) {
	op := find(t, "compute", "create-disk")

	_, err := op.Request(map[string]any{
		"body": map[string]any{
			"name":         "",
			"size_gb":      float64(0),
			"disk_type_id": "not-a-uuid",
		},
	})

	var invalid *validate.Error
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want a validation error", err)
	}

	if len(invalid.Problems) < 3 {
		t.Errorf("problems = %v, want one per bad field", invalid.Problems)
	}
}

// An argument the operation does not accept is refused by name. Dropped
// silently, a misspelled filter is a call that appears to have worked.
func TestRequestRefusesUnknownArguments(t *testing.T) {
	op := find(t, "compute", "get-disk")

	_, err := op.Request(map[string]any{
		"diskId":  "019fb8c2-cde8-7a55-b5e5-a4538cf2597a",
		"regionn": "cn-north-1",
	})

	var invalid *validate.Error
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want a validation error", err)
	}

	// Named, and alongside what the operation does accept: that is what lets a
	// caller fix it without listing the operation again.
	if !strings.Contains(err.Error(), "regionn") || !strings.Contains(err.Error(), "diskId") {
		t.Errorf("the error does not name the argument and the alternatives: %v", err)
	}
}

func TestRequestRefusesAMissingRequiredBody(t *testing.T) {
	op := find(t, "compute", "create-disk")

	if _, err := op.Request(map[string]any{}); err == nil {
		t.Fatal("a required body was allowed to be absent")
	}
}

// An optional body left out is omitted, not sent as {} — which some operations
// would read as an explicit clear.
func TestRequestOmitsAnAbsentBody(t *testing.T) {
	op := find(t, "compute", "list-disks")

	request, err := op.Request(map[string]any{})
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	if request.Body != nil {
		t.Errorf("body = %v, want none", request.Body)
	}
}

// Every JSON number decodes to float64. A page sent as "2.0" is not a page the
// server recognises.
func TestQueryNumbersAreWrittenWhole(t *testing.T) {
	got, ok := formatScalar(float64(2))
	if !ok || got != "2" {
		t.Errorf("formatScalar(2) = %q, %v; want \"2\", true", got, ok)
	}
}

func TestQueryListsExplodeIntoOneEntryEach(t *testing.T) {
	values, err := queryValues("status", []any{"ACTIVE", "PENDING"})
	if err != nil {
		t.Fatalf("queryValues: %v", err)
	}

	if len(values) != 2 || values[0] != "ACTIVE" || values[1] != "PENDING" {
		t.Errorf("values = %v, want [ACTIVE PENDING]", values)
	}
}

// A value with a / in it would otherwise point the request at another path.
func TestPathValuesAreEscaped(t *testing.T) {
	op := find(t, "compute", "get-disk")

	request, err := op.Request(map[string]any{
		"diskId": "a/b",
	})
	if err != nil {
		// The contract may refuse this before escaping matters, which is also a
		// correct outcome — the request is what must never be malformed.
		return
	}

	if strings.Contains(strings.TrimPrefix(request.Path, "/api/v1/disks/"), "/") {
		t.Errorf("an id escaped its segment: %s", request.Path)
	}
}

// A schema that refers to itself must terminate. Without a cycle check this
// expands until it runs out of memory.
func TestSchemaConversionTerminatesOnRecursion(t *testing.T) {
	node := openapi3.NewObjectSchema()
	node.Description = "a node"
	node.Properties = openapi3.Schemas{
		"child": openapi3.NewSchemaRef("", node),
	}

	converted := convertSchema(node)

	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatalf("the converted schema does not marshal: %v", err)
	}

	if !strings.Contains(string(encoded), "repeats the structure above") {
		t.Errorf("a cycle was not reported: %s", encoded)
	}
}

// $ref must not survive into a tool schema: the client has no components
// section to resolve it against, and would see an untyped field where the
// contract was specific.
func TestSchemaCarriesNoReferences(t *testing.T) {
	for _, op := range mustCatalog(t, nil, false).operations {
		encoded, err := json.Marshal(op.Schema())
		if err != nil {
			t.Fatalf("%s does not marshal: %v", toolLabel(op), err)
		}

		if strings.Contains(string(encoded), `"$ref"`) {
			t.Errorf("%s has an unresolved reference", toolLabel(op))
		}
	}
}

// Two tools with one name is not a panic, it is a silent overwrite: one
// operation would simply stop existing, and nothing would say which. The
// command tree gets the same guarantee from leaflow-doctor; this is the same
// check for the same identifiers on this surface.
func TestToolNamesAreUniqueAndWellFormed(t *testing.T) {
	seen := map[string]string{}

	for _, op := range mustCatalog(t, nil, false).operations {
		name := toolLabel(op)

		if previous, clash := seen[name]; clash {
			t.Errorf("%s and %s both become the tool %q", previous, op.Name(), name)
		}

		seen[name] = op.Name()

		// The protocol allows letters, digits, underscore and hyphen, up to 128
		// characters. Kebab produces nothing else, but the day a contract carries
		// an operationId with a dot in it, this is where it is caught.
		if len(name) > 128 {
			t.Errorf("tool name is too long: %q", name)
		}

		for _, r := range name {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
				continue
			}

			t.Errorf("tool name %q has a character the protocol does not allow: %q", name, r)

			break
		}
	}
}

func mustCatalog(t *testing.T, services []string, readOnly bool) *catalog {
	t.Helper()

	c, err := newCatalog(load(t), services, catalogLimits{
		readOnly: readOnly,
	})
	if err != nil {
		t.Fatalf("newCatalog: %v", err)
	}

	return c
}

// --read-only has to drop writes before a client sees them. A tool a model can
// see is a tool it can decide to call.
func TestReadOnlyExposesNoWrites(t *testing.T) {
	for _, op := range mustCatalog(t, nil, true).operations {
		if !op.ReadOnly() {
			t.Errorf("%s is a %s and was exposed read-only", toolLabel(op), op.Method())
		}
	}
}

// An unknown service is a mistake in a config file nobody rereads; a server
// that silently exposed nothing would look like a working one.
func TestUnknownServiceIsRefusedAtStartup(t *testing.T) {
	if _, err := newCatalog(load(t), []string{"nope"}, catalogLimits{}); !errors.Is(err, ErrNoSuchService) {
		t.Fatalf("error = %v, want ErrNoSuchService", err)
	}
}

func TestServiceFilterKeepsOnlyWhatWasAskedFor(t *testing.T) {
	c := mustCatalog(t, []string{"tunnel"}, false)

	for _, op := range c.operations {
		if op.Service() != "tunnel" {
			t.Errorf("%s leaked past the filter", toolLabel(op))
		}
	}

	if len(c.operations) == 0 {
		t.Error("the filter kept nothing")
	}
}

// Both spellings are in circulation — the contract's operationId and the
// command line's — and a caller reading the CLI's documentation should not have
// to know they differ.
func TestLookupAcceptsEitherSpelling(t *testing.T) {
	c := mustCatalog(t, nil, false)

	for _, id := range []string{"create-disk", "createDisk"} {
		if _, err := c.find("compute", id); err != nil {
			t.Errorf("lookup(%q): %v", id, err)
		}
	}
}

// A refusal that lists the near misses is what lets a model correct itself
// without another round of listing.
func TestLookupSuggestsNearMisses(t *testing.T) {
	c := mustCatalog(t, nil, false)

	_, err := c.find("compute", "get-disk-typo")
	if !errors.Is(err, ErrNoSuchOperation) {
		t.Fatalf("error = %v, want ErrNoSuchOperation", err)
	}

	if !strings.Contains(err.Error(), "get-disk") {
		t.Errorf("no suggestion in %v", err)
	}
}

// Groups are what a caller narrows by before it knows any operation names, and
// most contracts declare no top-level tag list at all — the tag lives on the
// operation. Reading only the declared list reported no groups for a service
// with fourteen of them, which left the tag filter undiscoverable.
func TestServicesReportTheGroupsTheyActuallyHave(t *testing.T) {
	c := mustCatalog(t, nil, false)

	service, ok := c.specs.Service("compute")
	if !ok {
		t.Fatal("no compute contract")
	}

	groups := c.groups(service)
	if len(groups) == 0 {
		t.Fatal("compute reported no groups")
	}

	total := 0

	for _, entry := range groups {
		group, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("group is not an object: %v", entry)
		}

		count, ok := group["operations"].(int)
		if !ok || count == 0 {
			t.Errorf("group %v has no operations, so it should not be offered", group["name"])
		}

		total += count
	}

	if total > len(c.byService["compute"]) {
		t.Errorf("groups count %d operations, but the service exposes %d",
			total, len(c.byService["compute"]))
	}
}

// A group whose operations were all dropped must not be offered: filtering by
// it would return nothing, which reads as "none exist" rather than "none are
// exposed here".
func TestReadOnlyDropsEmptiedGroups(t *testing.T) {
	c := mustCatalog(t, nil, true)

	service, ok := c.specs.Service("compute")
	if !ok {
		t.Fatal("no compute contract")
	}

	for _, entry := range c.groups(service) {
		group, _ := entry.(map[string]any)

		name, _ := group["name"].(string)

		found := 0

		for _, op := range c.byService["compute"] {
			if op.Group() == name {
				found++
			}
		}

		if found == 0 {
			t.Errorf("group %q is offered but nothing is in it", name)
		}
	}
}

func sortedNames(properties map[string]any) []string {
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}

	return names
}

// toolLabel is how a presentation layer names an operation, checked here
// because uniqueness is a property of the identifiers, not of any one surface.
func toolLabel(op *Operation) string {
	return op.Service() + "-" + op.Name()
}
