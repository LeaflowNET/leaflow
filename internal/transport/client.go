// Package transport sends every request this CLI makes. It owns the three
// things a command should not have to: which token an operation takes, which
// address it goes to, and how a refusal reads.
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

	"github.com/LeaflowNET/leaflow/internal/auth"
	"github.com/LeaflowNET/leaflow/internal/config"
	"github.com/LeaflowNET/leaflow/internal/spec"
)

var ErrRequestFailed = errors.New("request failed")

type Client struct {
	ctx     *config.Context
	tokens  *auth.Manager
	http    *http.Client
	Verbose bool
	Log     io.Writer
}

func New(cfg *config.Config, tokens *auth.Manager, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}

	return &Client{ctx: cfg.Context(), tokens: tokens, http: httpClient}
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
	if errors.As(err, &apiErr) && retriable(apiErr) {
		if invalidated := c.tokens.InvalidateProject(); invalidated == nil {
			return c.send(ctx, request)
		}
	}

	return result, status, err
}

func retriable(err *APIError) bool {
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
	token, err := c.tokens.Token(ctx, request.Operation.Credential)
	if err != nil {
		return nil, 0, err
	}

	target := c.baseURL(request.Operation) + request.Path

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

func (c *Client) baseURL(op *spec.Operation) string {
	return c.ctx.ServiceURL(op.Service)
}

func parseError(status int, raw []byte) error {
	failure := &APIError{Status: status}

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
