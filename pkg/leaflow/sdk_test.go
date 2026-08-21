package leaflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/LeaflowNET/leaflow/pkg/transport"
)

// renewing stands for a caller whose tokens outlive nothing: it mints one on
// demand and mints another when the last was refused.
type renewing struct {
	minted   int
	dropped  int
	previous string
}

func (r *renewing) Token(context.Context, spec.Credential) (string, error) {
	r.minted++

	r.previous = "minted-" + string(rune('a'+r.minted-1))

	return r.previous, nil
}

func (r *renewing) Invalidate(spec.Credential) error {
	r.dropped++

	return nil
}

// An access token is good for a day; work that waits on a human can be paused
// for longer. When it resumes, the first call gets a 401 — and the transport
// only retries it if the credentials say a fresh one is available. A bare Token
// cannot, which is why Call carries this instead.
func TestCallCredentialsMakeTheRetryPossible(t *testing.T) {
	var seen []string

	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))

		if len(seen) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"TOKEN_EXPIRED","message":"expired"}`))

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"d-1"}`))
	}))
	defer service.Close()

	client, err := New(Options{
		Endpoints: transport.Endpoints{
			Overrides: map[string]string{
				"compute": service.URL,
			},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	source := &renewing{}

	result, err := client.Call(context.Background(), Call{
		Service:   "compute",
		Operation: "get-disk",
		Arguments: map[string]any{
			"diskId": "019fb8c2-cde8-7a55-b5e5-a4538cf2597a",
		},
		Credentials: source,
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("the expired token was not retried: %d request(s)", len(seen))
	}

	if seen[0] == seen[1] {
		t.Errorf("the retry reused the refused token: %q", seen[0])
	}

	if source.dropped != 1 {
		t.Errorf("the refused token was not dropped: %d", source.dropped)
	}

	if result.Status != http.StatusOK {
		t.Errorf("status = %d", result.Status)
	}
}

// A bare Token has nothing else to offer, and says so rather than letting the
// transport retry with the same value.
func TestBareTokenDoesNotRetry(t *testing.T) {
	attempts := 0

	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++

		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"TOKEN_EXPIRED"}`))
	}))
	defer service.Close()

	client, err := New(Options{
		Endpoints: transport.Endpoints{
			Overrides: map[string]string{
				"compute": service.URL,
			},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = client.Call(context.Background(), Call{
		Service:   "compute",
		Operation: "get-disk",
		Arguments: map[string]any{
			"diskId": "019fb8c2-cde8-7a55-b5e5-a4538cf2597a",
		},
		Token: Token{
			Access: "stale",
		},
	})

	if attempts != 1 {
		t.Errorf("a token that cannot be replaced was retried: %d attempts", attempts)
	}

	// The caller still has to be able to tell why, and to tell it apart from
	// "you may not do that".
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("error is not recognisable as an expired token: %v", err)
	}

	if !CanRetry(err) {
		t.Error("an expired token should read as worth retrying")
	}
}

// The account face only takes a token that comes from a person's sign-in
// session, which a service acting on someone's behalf does not have. Left in,
// those operations are ones a model finds, calls, and is refused by.
func TestAccessTokenOnlyDropsTheAccountFace(t *testing.T) {
	client, err := New(Options{
		AccessTokenOnly: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, op := range client.Operations() {
		if op.AccountToken() {
			t.Errorf("%s %s takes an account token and was exposed", op.Service(), op.Name())
		}
	}

	if len(client.Operations()) == 0 {
		t.Fatal("everything was dropped")
	}

	// And it is gone from the lookup too, so nothing can reach it by name.
	if _, err := client.Operation("account", "list-projects"); err == nil {
		t.Error("an account-token operation is still reachable by name")
	}
}

// Dropping the account face must not need a hand-written list of services,
// which would be wrong the day the platform adds one — silently, because a list
// of names cannot notice that it is short.
func TestAccessTokenOnlyReadsTheContract(t *testing.T) {
	client, err := New(Options{
		AccessTokenOnly: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, service := range client.Services() {
		if spec.ReadCredential(service.Name) == spec.AccountToken {
			t.Errorf("%s is an account-token service and was kept", service.Name)
		}
	}
}

// A caller not going through MCP needs the same rendering, or it writes its own
// and that one falls behind the day APIError grows a field.
func TestRenderErrorSaysWhatKindItIs(t *testing.T) {
	client, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, callErr := client.Call(context.Background(), Call{
		Service:   "compute",
		Operation: "create-disk",
		Token: Token{
			Access: "x",
		},
		Arguments: map[string]any{
			"body": map[string]any{
				"name":    "",
				"size_gb": float64(0),
			},
		},
	})
	if callErr == nil {
		t.Fatal("bad arguments were accepted")
	}

	rendered := RenderError(callErr, "error")

	for _, want := range []string{"<error>", "<kind>invalid_argument</kind>", "<problems>", "</error>"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendering is missing %q:\n%s", want, rendered)
		}
	}

	// Each complaint separately, because each is a separate thing to fix.
	if len(Problems(callErr)) < 2 {
		t.Errorf("problems = %v, want one per bad field", Problems(callErr))
	}
}

// Nothing here may fail: a caller handling a failure should not have to handle
// a second one from the attempt to describe the first.
func TestRenderErrorAlwaysProducesText(t *testing.T) {
	if got := RenderError(errors.New("plain"), ""); !strings.Contains(got, "plain") {
		t.Errorf("a plain error rendered as %q", got)
	}

	if got := RenderError(nil, ""); got != "" {
		t.Errorf("nil rendered as %q", got)
	}
}

// A service inside a cluster reaches the platform by service name, not by its
// public address. The model never sees either.
func TestEndpointsPointAtAnotherDeployment(t *testing.T) {
	var reached string

	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = r.Host

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"d-1"}`))
	}))
	defer service.Close()

	client, err := New(Options{
		Endpoints: transport.Endpoints{
			Overrides: map[string]string{
				"compute": service.URL,
			},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := client.Call(context.Background(), Call{
		Service:   "compute",
		Operation: "get-disk",
		Arguments: map[string]any{
			"diskId": "019fb8c2-cde8-7a55-b5e5-a4538cf2597a",
		},
		Token: Token{
			Access: "t",
		},
	}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	if !strings.Contains(service.URL, reached) {
		t.Errorf("the request went to %q, not to the override", reached)
	}
}
