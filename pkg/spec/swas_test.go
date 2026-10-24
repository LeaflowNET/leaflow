package spec

import "testing"

func TestSWASContractShape(t *testing.T) {
	set, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	service, ok := set.Service("swas")
	if !ok {
		t.Fatal("SWAS contract missing")
	}
	if len(service.Operations()) != 10 {
		t.Fatalf("operations: %d", len(service.Operations()))
	}
	if service.BaseURL != "https://swas.leaflow.cloud" {
		t.Fatalf("address: %s", service.BaseURL)
	}
	for _, op := range service.Operations() {
		if op.Credential != AccessToken {
			t.Fatalf("%s is not scoped-token", op.ID)
		}
	}
	pagination := service.Doc.Components.Schemas["InstancePagination"].Value
	if _, old := pagination.Properties["total"]; old {
		t.Fatal("old total field retained")
	}
	if field := pagination.Properties["total_count"]; field == nil || field.Value.Format != "int64" {
		t.Fatal("shared total_count missing")
	}
	errSchema := service.Doc.Components.Schemas["Error"].Value
	for _, key := range []string{"status", "message"} {
		found := false
		for _, required := range errSchema.Required {
			if required == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("shared error missing %s", key)
		}
	}
	legacy, ok := set.Service("swas-legacy")
	if !ok || len(legacy.Operations()) != 16 {
		t.Fatal("legacy operation set changed")
	}
	for _, action := range []string{"start", "stop", "reboot"} {
		op, ok := legacy.Operation(action + "-instance")
		if !ok || op.Path != "/api/v1/instances/{instanceId}/"+action {
			t.Fatalf("legacy compatibility: %s", action)
		}
	}
}
