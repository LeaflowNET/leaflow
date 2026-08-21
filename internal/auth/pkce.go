package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrLoopbackUnavailable = errors.New("cannot listen on the loopback interface")

	ErrAuthorizationDenied = errors.New("authorization was denied")

	// ErrStateMismatch means the callback did not belong to this login attempt.
	// Continuing anyway is how an authorization code from another origin gets
	// accepted, so it is fatal rather than a retry.
	ErrStateMismatch = errors.New("callback state did not match")
)

// pkce is one login attempt's proof that the client redeeming the code is the
// one that requested it (RFC 7636).
//
// A public client cannot hold a secret — this binary ships to everyone — so the
// secret is created per attempt instead: the verifier never leaves the process,
// and only its SHA-256 hash travels in the authorization request. An
// intercepted code is useless without the verifier.
type pkce struct {
	verifier  string
	challenge string
	state     string
}

func newPKCE() (*pkce, error) {
	verifier, err := makeRandomString(32)
	if err != nil {
		return nil, err
	}

	state, err := makeRandomString(16)
	if err != nil {
		return nil, err
	}

	sum := sha256.Sum256([]byte(verifier))

	return &pkce{
		verifier:  verifier,
		challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
		state:     state,
	}, nil
}

func makeRandomString(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("%w: %v", ErrLoopbackUnavailable, err)
	}

	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// callback is a one-shot loopback listener for the redirect.
type callback struct {
	listener net.Listener
	results  chan callbackResult
}

type callbackResult struct {
	code string
	err  error
}

// listenLoopback binds 127.0.0.1 on a port the OS picks.
//
// Loopback and not 0.0.0.0: the redirect carries an authorization code, and
// binding every interface would offer it to the network. Port 0 because a fixed
// port would collide with a second leaflow running, and RFC 8252 requires the
// authorization server to accept any port on a loopback redirect — which this
// realm does.
func listenLoopback() (*callback, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoopbackUnavailable, err)
	}

	return &callback{
		listener: listener,
		results:  make(chan callbackResult, 1),
	}, nil
}

func (c *callback) redirectURI() string {
	return "http://" + c.listener.Addr().String() + "/callback"
}

// serve answers exactly one redirect and then stops.
func (c *callback) serve(state string) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		if failure := query.Get("error"); failure != "" {
			description := query.Get("error_description")
			c.finish(w, "", fmt.Errorf("%w: %s %s", ErrAuthorizationDenied, failure, description))

			return
		}

		if query.Get("state") != state {
			c.finish(w, "", ErrStateMismatch)

			return
		}

		c.finish(w, query.Get("code"), nil)
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		// Serve always returns a non-nil error. ErrServerClosed is this listener
		// being stopped once the redirect has arrived, which is how a successful
		// sign-in ends. Anything else means the redirect can never arrive — and
		// left unsaid, the sign-in would go on waiting for a callback that is not
		// coming, until whichever timeout runs out first.
		if err := server.Serve(c.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.deliver(callbackResult{
				err: fmt.Errorf("%w: %v", ErrLoopbackUnavailable, err),
			})
		}
	}()

	return server
}

func (c *callback) finish(w http.ResponseWriter, code string, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(renderResultPage("Sign-in failed", "You can close this tab and check the terminal.")))
	} else {
		_, _ = w.Write([]byte(renderResultPage("Signed in", "You can close this tab and return to the terminal.")))
	}

	c.deliver(callbackResult{
		code: code,
		err:  err,
	})
}

// deliver hands the first outcome to whoever is waiting and drops the rest.
//
// Non-blocking because there is exactly one waiter and it takes exactly one
// answer: a second delivery is a stray browser reload, or the server reporting
// its own shutdown after the code already arrived.
func (c *callback) deliver(result callbackResult) {
	select {
	case c.results <- result:
	default:
	}
}

func renderResultPage(title, detail string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<title>` + title + `</title>` +
		`<style>body{font:16px/1.6 system-ui,sans-serif;margin:20vh auto;max-width:28rem;text-align:center;color:#111}` +
		`h1{font-size:1.25rem;font-weight:600}p{color:#555}</style></head>` +
		`<body><h1>` + title + `</h1><p>` + detail + `</p></body></html>`
}

// authorizeURL builds the authorization request.
func authorizeURL(ep *endpoints, clientID, redirectURI, scope string, proof *pkce) (string, error) {
	authorize, err := readAuthorizationEndpoint(ep)
	if err != nil {
		return "", err
	}

	query := url.Values{}
	query.Set("client_id", clientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", scope)
	query.Set("state", proof.state)
	query.Set("code_challenge", proof.challenge)
	query.Set("code_challenge_method", "S256")

	return authorize + "?" + query.Encode(), nil
}

// redeem exchanges the authorization code, proving possession of the verifier.
func redeem(ctx context.Context, client *http.Client, ep *endpoints, clientID, code, redirectURI, verifier string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)

	result, err := exchangeForm(ctx, client, ep.Token, form)
	if err != nil {
		return nil, err
	}

	if result.Error != "" {
		return nil, fmt.Errorf("%w: %s %s", ErrAuthorizationDenied, result.Error, result.ErrorDescription)
	}

	return result, nil
}

// readAuthorizationEndpoint is derived from the token endpoint when discovery did
// not name it, which keeps this working against a server that publishes a
// partial document.
func readAuthorizationEndpoint(ep *endpoints) (string, error) {
	if ep.Authorize != "" {
		return ep.Authorize, nil
	}

	if ep.Token == "" {
		return "", ErrDiscovery
	}

	return strings.TrimSuffix(ep.Token, "/token") + "/auth", nil
}
