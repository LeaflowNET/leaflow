package spec

import (
	"slices"
	"testing"
)

// A split service has no contract at its own level. Looking one level deep only
// would drop billing from the command tree without an error, so the embedded
// subpackages must each come out as a service named by its path.
func TestSubpackageContractsAreServices(t *testing.T) {
	names, err := EmbeddedNames()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"billing/account", "billing/catalog", "billing/project"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s is missing from %v", want, names)
		}
	}

	if slices.Contains(names, "billing") {
		t.Errorf("billing has no contract of its own, yet it is listed: %v", names)
	}
}

// A subpackage's contract refers to the shared types one level further up
// (../../../type/v1). Loading it proves the reference resolves inside the
// embedded tree.
func TestSubpackageContractsLoad(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	catalog, ok := set.Service("billing/catalog")
	if !ok {
		t.Fatalf("billing/catalog not loaded; have %v", set.Names())
	}

	op, ok := catalog.Operation("list-prices")
	if !ok {
		t.Fatalf("list-prices not in billing/catalog: %v", catalog.OperationIDs())
	}

	if op.Service != "billing/catalog" || op.Path != "/catalog/v1/prices" || op.BaseURL != "https://billing.leaflow.cloud" {
		t.Fatalf("unexpected operation: service=%s path=%s base=%s", op.Service, op.Path, op.BaseURL)
	}
}
