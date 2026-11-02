package dynamic

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/pflag"
)

func TestRegisterFlagTreatsBackticksAsDescription(t *testing.T) {
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	registerFlag(flags, "resource-type", &openapi3.Schema{}, "Give it together with `resource_id`.")
	name, usage := pflag.UnquoteUsage(flags.Lookup("resource-type"))
	if name != "string" || !strings.Contains(usage, "resource_id") {
		t.Fatalf("got placeholder %q and usage %q", name, usage)
	}
}
