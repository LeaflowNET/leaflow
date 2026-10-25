package leaflow

import "testing"

func TestMultiPeriodRequestNoNetwork(t *testing.T) {
	client, err := New(Options{Services: []string{"swas"}})
	if err != nil {
		t.Fatal(err)
	}
	op, err := client.Operation("swas", "list-plans")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		args map[string]any
		want string
		has  bool
	}{
		{"omitted", map[string]any{"region_id": "region-1"}, "", false},
		{"false", map[string]any{"region_id": "region-1", "include_prices": false}, "false", true},
		{"true", map[string]any{"region_id": "region-1", "include_prices": true}, "true", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := op.Request(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if req.Query.Has("include_prices") != tt.has || req.Query.Get("include_prices") != tt.want {
				t.Fatalf("query: %s", req.Query.Encode())
			}
			if req.Path != "/api/v1/catalog/plans" {
				t.Fatal(req.Path)
			}
			t.Logf("include_prices present=%v value=%q; request constructed only", tt.has, tt.want)
		})
	}
	if _, err := op.Request(map[string]any{"region_id": "region-1", "include_prices": "true"}); err == nil {
		t.Fatal("string bool accepted")
	}
}
