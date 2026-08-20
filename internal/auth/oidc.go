package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

var (
	ErrDiscovery = errors.New("cannot reach the login service")

	// ErrNoDeviceFlow names the thing an operator has to change, because
	// otherwise this reads like a bug in the CLI.
	ErrNoDeviceFlow = errors.New(
		"this realm has no device authorization grant; enable OAuth 2.0 Device Authorization Grant on the client")

	ErrUnknownClient = errors.New("the login service does not recognise this client; it must be a public client with the device flow enabled")

	ErrLoginTimeout = errors.New("login timed out, run leaflow login again")

	ErrLoginDenied = errors.New("login was denied")

	ErrDeviceCodeExpired = errors.New("device code expired, run leaflow login again")
)

// endpoints comes from the realm's discovery document rather than hardcoded
// Keycloak paths, so the CLI need not assume which implementation is behind the
// issuer or where it is mounted.
type endpoints struct {
	Authorize string `json:"authorization_endpoint"`
	Device    string `json:"device_authorization_endpoint"`
	Token     string `json:"token_endpoint"`
	Revoke    string `json:"revocation_endpoint"`
}

func discover(ctx context.Context, client *http.Client, issuer string) (*endpoints, error) {
	target := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w %s: %v", ErrDiscovery, issuer, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"%w: %s returned %d — the issuer must include the realm, e.g. https://auth.leaflow.net/realms/user",
			ErrDiscovery, issuer, response.StatusCode)
	}

	var found endpoints
	if err := json.NewDecoder(response.Body).Decode(&found); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiscovery, err)
	}

	return &found, nil
}

type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	CompleteURI     string `json:"verification_uri_complete"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// startDevice requests a device code.
//
// PKCE travels here too. RFC 8628 does not define it, but Keycloak implements
// the extension and enforces it whenever the client requires PKCE — and it is
// worth having: it binds redemption to the process that started the flow, so a
// leaked device code alone cannot be exchanged.
func startDevice(ctx context.Context, client *http.Client, ep *endpoints, clientID, scope string, proof *pkce) (*DeviceCode, error) {
	form := url.Values{}
	form.Set("client_id", clientID)

	if scope != "" {
		form.Set("scope", scope)
	}

	if proof != nil {
		form.Set("code_challenge", proof.challenge)
		form.Set("code_challenge_method", "S256")
	}

	response, err := postForm(ctx, client, ep.Device, form)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		var failed tokenResponse
		_ = json.NewDecoder(response.Body).Decode(&failed)

		if failed.Error == "invalid_client" {
			return nil, fmt.Errorf("%w: %s", ErrUnknownClient, clientID)
		}

		return nil, fmt.Errorf("%w: %s %s", ErrDiscovery, failed.Error, failed.ErrorDescription)
	}

	var code DeviceCode
	if err := json.NewDecoder(response.Body).Decode(&code); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiscovery, err)
	}

	if code.Interval <= 0 {
		code.Interval = 5 // RFC 8628 default
	}

	return &code, nil
}

// pollDevice waits for the browser half of the flow. Polling faster than asked
// is what must not happen: ignoring slow_down makes Keycloak drop the flow, and
// that surfaces as login failing for no visible reason.
func pollDevice(ctx context.Context, client *http.Client, ep *endpoints, clientID string, code *DeviceCode, proof *pkce) (*tokenResponse, error) {
	interval := time.Duration(code.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("device_code", code.DeviceCode)
	form.Set("client_id", clientID)

	if proof != nil {
		form.Set("code_verifier", proof.verifier)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		if time.Now().After(deadline) {
			return nil, ErrLoginTimeout
		}

		result, err := exchangeForm(ctx, client, ep.Token, form)
		if err != nil {
			return nil, err
		}

		switch result.Error {
		case "":
			return result, nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return nil, ErrLoginDenied
		case "expired_token":
			return nil, ErrDeviceCodeExpired
		default:
			return nil, fmt.Errorf("%w: %s %s", ErrDiscovery, result.Error, result.ErrorDescription)
		}
	}
}

// renew exchanges a refresh token. A rejected one means expired, revoked, or
// already spent on a realm with rotation enabled — all of which mean "log in
// again", so they collapse into ErrNoCredentials.
func renew(ctx context.Context, client *http.Client, ep *endpoints, clientID, refreshToken string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)

	result, err := exchangeForm(ctx, client, ep.Token, form)
	if err != nil {
		return nil, err
	}

	if result.Error != "" {
		return nil, ErrNoCredentials
	}

	return result, nil
}

// revoke tells the realm to forget a refresh token (RFC 7009). Failure is not
// an error: local credentials are already gone, and the fallback is waiting for
// natural expiry.
func revoke(ctx context.Context, client *http.Client, ep *endpoints, clientID, refreshToken string) {
	if ep.Revoke == "" || refreshToken == "" {
		return
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("token", refreshToken)
	form.Set("token_type_hint", "refresh_token")

	if response, err := postForm(ctx, client, ep.Revoke, form); err == nil {
		_ = response.Body.Close()
	}
}

func exchangeForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) (*tokenResponse, error) {
	response, err := postForm(ctx, client, endpoint, form)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var result tokenResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("%w: unreadable reply (%d)", ErrDiscovery, response.StatusCode)
	}

	return &result, nil
}

func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDiscovery, err)
	}

	return response, nil
}

// OpenBrowser is best effort and reports nothing: the URL is already on screen,
// and a "could not open browser" error would read as though login had failed.
func OpenBrowser(target string) {
	var cmd string

	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}

	_ = exec.Command(cmd, append(args, target)...).Start()
}
