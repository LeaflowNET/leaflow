package mcp

import (
	"regexp"
	"testing"

	"github.com/LeaflowNET/leaflow/pkg/leaflow"
)

// Tool names are limited to letters, digits, hyphens and underscores. A
// subpackage's service name carries a slash, and a tool named with it would be
// refused by the client, so the slash has to become a hyphen.
func TestSubpackageToolNamesStayWithinTheAllowedCharacters(t *testing.T) {
	client, err := leaflow.New(leaflow.Options{Services: []string{"billing/catalog"}})
	if err != nil {
		t.Fatal(err)
	}

	op, err := client.Operation("billing/catalog", "list-prices")
	if err != nil {
		t.Fatal(err)
	}

	if got := buildToolName(op); got != "billing-catalog-list-prices" {
		t.Fatalf("tool name %q", got)
	}

	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(buildToolName(op)) {
		t.Fatalf("tool name %q has characters a client refuses", buildToolName(op))
	}

	if got := op.Command(); got != "leaflow billing catalog list-prices" {
		t.Fatalf("command %q", got)
	}
}
