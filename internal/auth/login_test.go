package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeaflowNET/leaflow/internal/config"
)

// realm stands in for Keycloak: enough of the discovery document for a login
// attempt to get as far as opening its loopback listener.
func realm(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	var base string

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                           base,
			"authorization_endpoint":           base + "/protocol/openid-connect/auth",
			"token_endpoint":                   base + "/protocol/openid-connect/token",
			"device_authorization_endpoint":    base + "/protocol/openid-connect/auth/device",
			"revocation_endpoint":              base + "/protocol/openid-connect/revoke",
			"code_challenge_methods_supported": "S256",
		})
	})

	server := httptest.NewServer(mux)
	base = server.URL

	t.Cleanup(server.Close)

	return server
}

func manager(t *testing.T, issuer string) *Manager {
	t.Helper()

	t.Setenv("LEAFLOW_CONFIG_DIR", t.TempDir())
	// The keychain is shared per context name, so tests must not touch the one
	// a real login on this machine wrote.
	t.Setenv("LEAFLOW_CREDENTIAL_STORE", string(StorageFile))

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}

	cfg.CredentialStore = string(StorageFile)
	cfg.EditContext("").Issuer = issuer

	m, err := NewManager(cfg, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	return m
}

// Over ssh the URL is printed on the server while the browser opens on the
// laptop, so the callback reaches the laptop's own port where nothing is
// listening. Without a way out, sign-in hangs until it times out.
func TestLoginCanBeAbandonedForTheDeviceFlow(t *testing.T) {
	server := realm(t)
	m := manager(t, server.URL)

	abort := make(chan struct{})

	notified := make(chan string, 1)

	go func() {
		// Abandon only once the URL has been shown, which is when a reader would
		// realise it points at the wrong machine.
		<-notified
		close(abort)
	}()

	_, err := m.Login(t.Context(), func(target string) { notified <- target }, abort)

	if !errors.Is(err, ErrLoginAborted) {
		t.Fatalf("err = %v, want ErrLoginAborted", err)
	}

	if len(notified) == 0 && err == nil {
		t.Error("the caller was never told where to sign in")
	}
}

// The redirect must stay on the loopback interface: it carries an
// authorization code, and binding every interface would offer it to the
// network.
func TestLoginRedirectsToLoopback(t *testing.T) {
	server := realm(t)
	m := manager(t, server.URL)

	abort := make(chan struct{})
	seen := make(chan string, 1)

	go func() {
		target := <-seen
		close(abort)

		for _, want := range []string{"127.0.0.1", "code_challenge_method=S256", "response_type=code"} {
			if !strings.Contains(target, want) {
				t.Errorf("authorization URL is missing %q: %s", want, target)
			}
		}
	}()

	_, _ = m.Login(t.Context(), func(target string) { seen <- target }, abort)
}

// A cancelled context is Ctrl-C, which is not the same as choosing the device
// flow and must not be reported as it.
func TestLoginStopsOnCancellation(t *testing.T) {
	server := realm(t)
	m := manager(t, server.URL)

	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := m.Login(ctx, func(string) {}, make(chan struct{}))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
