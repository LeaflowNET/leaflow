package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/LeaflowNET/leaflow/pkg/spec"
)

type credentialRecorder struct {
	tokens, invalidated []spec.Credential
}

func (c *credentialRecorder) Token(_ context.Context, kind spec.Credential) (string, error) {
	c.tokens = append(c.tokens, kind)
	return kind.String(), nil
}

func (c *credentialRecorder) Invalidate(kind spec.Credential) error {
	c.invalidated = append(c.invalidated, kind)
	return nil
}

func TestPublicRequestDoesNotUseCredentials(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		for _, withCredentials := range []bool{false, true} {
			t.Run(http.StatusText(status)+"/"+map[bool]string{false: "anonymous", true: "signed-in"}[withCredentials], func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if auth := r.Header.Get("Authorization"); auth != "" {
						t.Errorf("public request sent %q", auth)
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"code":"TOKEN_EXPIRED"}`))
				}))
				defer server.Close()
				recorder := &credentialRecorder{}
				var credentials Credentials
				if withCredentials {
					credentials = recorder
				}
				client := New(nil, credentials, server.Client())
				_, got, err := client.Do(context.Background(), &Request{
					Operation: &spec.Operation{Method: http.MethodGet, BaseURL: server.URL, Credential: spec.NoCredential},
					Path:      "/public", Header: http.Header{"Authorization": {"Bearer must-not-leak"}},
				})
				if got != status || (err != nil) != (status >= 400) {
					t.Fatalf("status=%d, error=%v", got, err)
				}
				if calls != 1 || len(recorder.tokens) != 0 || len(recorder.invalidated) != 0 {
					t.Fatalf("calls=%d tokens=%v invalidated=%v", calls, recorder.tokens, recorder.invalidated)
				}
			})
		}
	}
}

func TestProtectedRequestUsesAndRefreshesRequiredToken(t *testing.T) {
	for _, kind := range []spec.Credential{spec.AccountToken, spec.AccessToken} {
		t.Run(kind.String(), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if got := r.Header.Get("Authorization"); got != "Bearer "+kind.String() {
					t.Errorf("wrong token: %s", got)
				}
				if calls == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"code":"TOKEN_EXPIRED"}`))
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			request := &Request{Operation: &spec.Operation{Method: http.MethodGet, BaseURL: server.URL, Credential: kind}, Path: "/protected"}
			if _, _, err := New(nil, nil, server.Client()).Do(context.Background(), request); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("missing credentials: %v", err)
			}
			recorder := &credentialRecorder{}
			_, status, err := New(nil, recorder, server.Client()).Do(context.Background(), request)
			if err != nil || status != http.StatusOK {
				t.Fatalf("status=%d, error=%v", status, err)
			}
			if !reflect.DeepEqual(recorder.tokens, []spec.Credential{kind, kind}) || !reflect.DeepEqual(recorder.invalidated, []spec.Credential{kind}) {
				t.Fatalf("tokens=%v invalidated=%v", recorder.tokens, recorder.invalidated)
			}
		})
	}
}
