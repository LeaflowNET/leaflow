package leaflow

import (
	"sync"
	"testing"
)

// A service shares one Client across every request it serves, so the contracts
// underneath are read by many goroutines at once. kin-openapi compiles patterns
// lazily while validating, which is exactly the kind of thing that turns a
// read-only structure into a data race under -race.
func TestClientIsSafeToShare(t *testing.T) {
	client, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wait sync.WaitGroup

	for range 16 {
		wait.Add(1)

		go func() {
			defer wait.Done()

			if len(client.Services()) == 0 {
				t.Error("no services")
			}

			if len(client.Operations()) == 0 {
				t.Error("no matches")
			}

			op, err := client.Operation("compute", "create-disk")
			if err != nil {
				t.Errorf("Operation: %v", err)

				return
			}

			_ = op.Schema()

			// Validation is where the contract gets written to, if anywhere.
			_, _ = op.Request(map[string]any{
				"body": map[string]any{
					"name":         "data",
					"size_gb":      float64(10),
					"disk_type_id": "019fb8c2-cde8-7a55-b5e5-a4538cf2597a",
				},
			})
		}()
	}

	wait.Wait()
}
