// Package transport sends every request made against the platform. It owns the
// three things a caller should not have to: which token an operation takes,
// which address it goes to, and how a refusal reads.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LeaflowNET/leaflow/pkg/spec"
)

// ErrRequestFailed is a request that never got an answer — the connection
// failed, the reply was not JSON. It unwraps to ErrUnavailable, because to a
// caller deciding what to do next it is the same situation as a 503.
var ErrRequestFailed = fmt.Errorf("request failed: %w", ErrUnavailable)

// Credentials supplies the token an operation takes.
//
// An interface, because the two callers want opposite things from one. A
// command line serves one person on one machine: it keeps a refresh token in
// the keychain, renews it, and exchanges it for a project token, and "the
// current user" is a thing it can meaningfully have. A service serves many
// people at once and is handed a token per request; it must not touch a
// keychain, and there is no current user for it to ask about.
//
// Everything below this line is the same either way — the retry, the error
// codes, the addresses — so the difference is confined to here.
type Credentials interface {
	Token(ctx context.Context, kind spec.Credential) (string, error)

	// Invalidate drops a cached token the service has just refused, so that the
	// one retry fetches a fresh one. An error means the retry cannot help and is
	// not attempted — which is the honest answer when the token was handed in
	// from outside and there is no other one to get.
	Invalidate(kind spec.Credential) error
}

// Addresses says where a service answers.
//
// It is given the address the contract declares and returns the one to use, so
// that pointing at another deployment is a decision made in one place rather
// than a string edited in several.
type Addresses interface {
	ServiceURL(service, declared string) (string, error)
}

type Client struct {
	addresses   Addresses
	credentials Credentials
	http        *http.Client
	Verbose     bool
	Log         io.Writer
}

func New(addresses Addresses, credentials Credentials, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 60 * time.Second,
		}
	}

	if addresses == nil {
		addresses = Endpoints{}
	}

	return &Client{
		addresses:   addresses,
		credentials: credentials,
		http:        httpClient,
	}
}

type Request struct {
	Operation *spec.Operation

	// Path already has its {placeholders} substituted.
	Path string

	Query  url.Values
	Header http.Header
	Body   any
}

// APIError is a refusal from a service. Callers branch on Code rather than
// matching prose: the wording changes, the code is contract.
type APIError struct {
	Status  int
	Code    string
	Message string
	Meta    map[string]any
}

func (e *APIError) Error() string {
	if advice := explain(e); advice != "" {
		return advice
	}

	if e.Message != "" {
		return e.Message
	}

	return fmt.Sprintf("service returned %d", e.Status)
}

func (c *Client) Do(ctx context.Context, request *Request) (any, int, error) {
	result, status, err := c.send(ctx, request)

	// An expired token is the only failure worth retrying automatically: a fresh
	// one fixes it, and making the user rerun the command just moves the work.
	var apiErr *APIError
	if errors.As(err, &apiErr) && canRetry(apiErr) {
		if invalidated := c.credentials.Invalidate(request.Operation.Credential); invalidated == nil {
			return c.send(ctx, request)
		}
	}

	return result, status, err
}

func canRetry(err *APIError) bool {
	if err.Status != http.StatusUnauthorized {
		return false
	}

	switch err.Code {
	case "TOKEN_EXPIRED", "TOKEN_INVALID", "IDENTITY_INVALID":
		return true
	default:
		// The gateway and IAM do not word this identically, and one extra round
		// trip is the whole cost of guessing wrong.
		return err.Code == ""
	}
}

func (c *Client) send(ctx context.Context, request *Request) (any, int, error) {
	token, err := c.credentials.Token(ctx, request.Operation.Credential)
	if err != nil {
		return nil, 0, err
	}

	base, err := c.baseURL(request.Operation)
	if err != nil {
		return nil, 0, err
	}

	target := base + request.Path

	if len(request.Query) > 0 {
		target += "?" + request.Query.Encode()
	}

	var body io.Reader

	if request.Body != nil {
		encoded, err := json.Marshal(request.Body)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %v", ErrRequestFailed, err)
		}

		body = bytes.NewReader(encoded)

		c.trace("> %s %s\n> %s\n", request.Operation.Method, target, encoded)
	} else {
		c.trace("> %s %s\n", request.Operation.Method, target)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, request.Operation.Method, target, body)
	if err != nil {
		return nil, 0, err
	}

	for key, values := range request.Header {
		for _, value := range values {
			httpRequest.Header.Add(key, value)
		}
	}

	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Accept", "application/json")

	if body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}

	response, err := c.http.Do(httpRequest)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %s: %v", ErrRequestFailed, target, err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, response.StatusCode, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	c.trace("< %d %s\n", response.StatusCode, http.StatusText(response.StatusCode))

	if response.StatusCode >= 400 {
		return nil, response.StatusCode, parseError(response.StatusCode, raw)
	}

	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, response.StatusCode, nil
	}

	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, response.StatusCode, fmt.Errorf("%w: reply is not JSON: %s", ErrRequestFailed, truncate(string(raw)))
	}

	return result, response.StatusCode, nil
}

func (c *Client) trace(format string, args ...any) {
	if c.Verbose && c.Log != nil {
		fmt.Fprintf(c.Log, format, args...)
	}
}

func (c *Client) baseURL(op *spec.Operation) (string, error) {
	return c.addresses.ServiceURL(op.Service, op.BaseURL)
}

func parseError(status int, raw []byte) error {
	failure := &APIError{
		Status: status,
	}

	var parsed struct {
		Status  int            `json:"status"`
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Detail  string         `json:"detail"`
		Title   string         `json:"title"`
		Meta    map[string]any `json:"meta"`
	}

	if err := json.Unmarshal(raw, &parsed); err == nil {
		failure.Code = parsed.Code
		failure.Meta = parsed.Meta

		switch {
		case parsed.Message != "":
			failure.Message = parsed.Message
		case parsed.Detail != "":
			failure.Message = parsed.Detail
		case parsed.Title != "":
			failure.Message = parsed.Title
		}
	}

	if failure.Message == "" && len(bytes.TrimSpace(raw)) > 0 {
		failure.Message = truncate(string(raw))
	}

	return failure
}

// explain turns the gateway's and IAM's refusal codes into what to do next.
// They fall into two groups that are handled differently: a token problem is
// fixed by getting another one, a person problem is not. Collapsing them into
// "authentication failed" makes people retry logins that cannot work.
func explain(err *APIError) string {
	switch err.Code {
	case "TOKEN_MISSING", "TOKEN_EXPIRED", "TOKEN_INVALID":
		return "token is invalid or expired; run: leaflow login"
	case "IDENTITY_MISSING", "IDENTITY_INVALID":
		return "identity is invalid; run: leaflow login"
	case "NOT_A_MEMBER":
		return "you are not a member of this project; see: leaflow project list"
	case "USER_SUSPENDED":
		return "this account is suspended"
	case "USER_BANNED":
		return "this account is banned"
	}

	if err.Status == http.StatusForbidden {
		if err.Message != "" {
			return "not permitted: " + err.Message
		}

		return "not permitted"
	}

	return ""
}

func truncate(text string) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) <= 500 {
		return text
	}

	return string([]rune(text)[:500]) + "..."
}
