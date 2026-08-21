package naming_test

import (
	"testing"

	"github.com/LeaflowNET/leaflow/pkg/naming"
)

func TestKebabLeavesOperationIDsAlone(t *testing.T) {
	// operationIds are already kebab, so this must be the identity on them.
	// Anything else would mean command names differ from the identifier the SDKs
	// and an MCP tool use.
	for _, id := range []string{
		"create-disk",
		"list-disks",
		"act-on-instance",
		"set-floating-ip-bandwidth",
		"suggest-subnet-cidr",
		"accept-invitation-by-token",
		"list-tunnel-usage-series",
	} {
		if got := naming.Kebab(id); got != id {
			t.Errorf("Kebab(%q) = %q, want it unchanged", id, got)
		}
	}
}

func TestKebabSplitsTags(t *testing.T) {
	for _, tc := range []struct {
		tag, want string
	}{
		{"Disk", "disk"},
		{"Instance", "instance"},
		{"FloatingIP", "floating-ip"},
		{"SecurityGroup", "security-group"},
		{"PrivateNetwork", "private-network"},
		{"OperationLog", "operation-log"},
		{"MaintenanceWindow", "maintenance-window"},
		{"WebCheck", "web-check"},
		{"ProjectToken", "project-token"},
		{"Observability", "observability"},

		// An acronym followed by a word splits once; an acronym alone does not.
		{"SSHKey", "ssh-key"},
		{"SLA", "sla"},
		{"API", "api"},
		{"APIKey", "api-key"},
	} {
		if got := naming.Kebab(tc.tag); got != tc.want {
			t.Errorf("Kebab(%q) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

func TestKebabSlugifiesAnythingElse(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"Web Checks", "web-checks"},
		{"web/checks", "web-checks"},
		{"  spaced  ", "spaced"},
		{"a__b", "a-b"},
		{"", ""},
		{"---", ""},
	} {
		if got := naming.Kebab(tc.in); got != tc.want {
			t.Errorf("Kebab(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
