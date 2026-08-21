package builtin

import (
	"errors"
	"testing"
)

// The server authenticates nobody while holding a logged-in user's credentials,
// so where it listens is a security decision — and not one to make by which
// spelling of an address came to mind first.
func TestResolveAddressDefaultsToThisMachine(t *testing.T) {
	for _, given := range []string{"8080", ":8080"} {
		got, err := listenAddress(given, false)
		if err != nil {
			t.Fatalf("listenAddress(%q): %v", given, err)
		}

		if got != "127.0.0.1:8080" {
			t.Errorf("listenAddress(%q) = %q, want 127.0.0.1:8080", given, got)
		}
	}
}

func TestResolveAddressRefusesTheWholeNetwork(t *testing.T) {
	for _, given := range []string{"0.0.0.0:8080", "192.168.1.10:8080"} {
		if _, err := listenAddress(given, false); !errors.Is(err, ErrAddressNotLocal) {
			t.Errorf("listenAddress(%q) error = %v, want ErrAddressNotLocal", given, err)
		}
	}
}

// Refused by default, not forbidden: someone who has put authentication in
// front of it is answering the question the refusal asks.
func TestResolveAddressAllowsTheNetworkWhenAsked(t *testing.T) {
	got, err := listenAddress("0.0.0.0:8080", true)
	if err != nil {
		t.Fatalf("resolveAddress: %v", err)
	}

	if got != "0.0.0.0:8080" {
		t.Errorf("resolveAddress = %q, want 0.0.0.0:8080", got)
	}
}

func TestResolveAddressAcceptsLoopbackByName(t *testing.T) {
	for _, given := range []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080"} {
		if _, err := listenAddress(given, false); err != nil {
			t.Errorf("listenAddress(%q): %v", given, err)
		}
	}
}
