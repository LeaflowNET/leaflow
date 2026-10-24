package leaflow

import (
	"github.com/LeaflowNET/leaflow/pkg/spec"
	"net/http"
	"testing"
)

func TestSWASContractRequests(t *testing.T) {
	client, err := New(Options{Services: []string{"swas"}})
	if err != nil {
		t.Fatal(err)
	}
	id := "10000000-0000-4000-8000-000000000001"
	for _, action := range []string{"start", "stop", "reboot"} {
		t.Run(action, func(t *testing.T) {
			op, err := client.Operation("swas", action+"-instance")
			if err != nil {
				t.Fatal(err)
			}
			request, err := op.Request(map[string]any{"instanceId": id, "Idempotency-Key": "synthetic-request-key", "body": map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			if request.Path != "/api/v1/instances/"+id+":"+action {
				t.Fatalf("custom verb path: %s", request.Path)
			}
			if request.Header.Get("Idempotency-Key") != "synthetic-request-key" {
				t.Fatalf("idempotency header missing; query=%v", request.Query)
			}
			if request.Query.Has("Idempotency-Key") {
				t.Fatal("header leaked into query")
			}
			if request.Operation.Method != http.MethodPost || request.Operation.Credential != spec.AccessToken {
				t.Fatal("method or scoped-token requirement changed")
			}
			if request.Operation.BaseURL != "https://swas.leaflow.cloud" {
				t.Fatalf("service URL: %s", request.Operation.BaseURL)
			}
			for _, args := range []map[string]any{{"instanceId": id, "body": map[string]any{}}, {"instanceId": id, "Idempotency-Key": "synthetic", "body": map[string]any{"project_id": id}}} {
				if _, err := op.Request(args); err == nil {
					t.Fatalf("invalid action arguments accepted: %v", args)
				}
			}
		})
	}
	op, err := client.Operation("swas", "list-instances")
	if err != nil {
		t.Fatal(err)
	}
	req, err := op.Request(map[string]any{"region_id": "synthetic-region", "page_size": float64(200)})
	if err != nil {
		t.Fatal(err)
	}
	if req.Query.Get("page_size") != "200" || len(req.Header) != 0 {
		t.Fatalf("query/header split: %v / %v", req.Query, req.Header)
	}
	if _, err := op.Request(map[string]any{"region_id": "x", "page_size": float64(201)}); err == nil {
		t.Fatal("oversized shared page accepted")
	}
}
