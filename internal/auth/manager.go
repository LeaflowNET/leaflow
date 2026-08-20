package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/LeaflowNET/leaflow/internal/config"
	"github.com/LeaflowNET/leaflow/internal/spec"
)

// EnvToken carries a project token straight from the environment. It bypasses
// storage, renewal and exchange, because CI has no browser and cannot run an
// interactive login.
const EnvToken = "LEAFLOW_TOKEN"

// accountService is the face that mints project tokens.
const accountService = "account"

var (
	ErrNeedProject = errors.New("no project selected")

	// ErrAccountTokenInCI is separate because retrying cannot help: the value in
	// LEAFLOW_TOKEN is a project token and the operation needs an account one.
	ErrAccountTokenInCI = errors.New(
		"this command needs an account token (register, list projects, exchange), which " +
			EnvToken + " cannot provide; run leaflow login")

	ErrNotAMember = errors.New("cannot obtain a token for that project; you may not be a member")

	ErrExchangeFailed = errors.New("project token exchange failed")

	// ErrLoginAborted means the caller gave up on the browser flow, not that
	// anything failed.
	ErrLoginAborted = errors.New("browser sign-in abandoned")

	ErrExchangeUnknown = errors.New(
		"the bundled contract does not declare " + ExchangeOperation + ", so a project token cannot be obtained")

	ErrTokenRejected = errors.New("the realm rejected that refresh token; it may be expired, revoked, or from another realm")
)

// Manager is the single entry point for "give me a usable token". Commands ask
// for one and get renewal, exchange, or a clear "log in" without each having to
// remember to check.
type Manager struct {
	ctx         *config.Context
	contextName string
	store       *Store
	client      *http.Client

	// exchange is where the account face mints project tokens. Taken from the
	// contract rather than hardcoded: the path has moved once already, and a
	// stale constant fails as "you may not be a member", which sends people to
	// look at permissions instead of at this line.
	exchangePath string

	discovered *endpoints
}

// ExchangeOperation is the contract's id for minting a project token.
const ExchangeOperation = "exchange-project-token"

// UseExchangePath tells the manager where the exchange lives. The CLI wires
// this from the loaded contract at startup.
func (m *Manager) UseExchangePath(path string) { m.exchangePath = path }

func NewManager(cfg *config.Config, client *http.Client) (*Manager, error) {
	name := cfg.CurrentName()

	mode, err := ParseStorageMode(cfg.CredentialStore)
	if err != nil {
		return nil, err
	}

	store, err := NewStore(name, mode)
	if err != nil {
		return nil, err
	}

	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	return &Manager{
		ctx:         cfg.Context(),
		contextName: name,
		store:       store,
		client:      client,
	}, nil
}

func (m *Manager) Token(ctx context.Context, kind spec.Credential) (string, error) {
	if token := os.Getenv(EnvToken); token != "" {
		if kind == spec.AccountToken {
			return "", ErrAccountTokenInCI
		}

		return token, nil
	}

	if kind == spec.AccountToken {
		return m.accountToken(ctx)
	}

	return m.projectToken(ctx)
}

func (m *Manager) accountToken(ctx context.Context) (string, error) {
	creds, err := m.store.Load()
	if err != nil {
		return "", err
	}

	if usable(creds.AccountToken, creds.AccountExpires) {
		return creds.AccountToken, nil
	}

	if creds.RefreshToken == "" {
		return "", ErrNoCredentials
	}

	// Renew under a lock. On a realm with refresh token rotation the token is
	// single use, so two terminals (or one `xargs -P`) renewing at once kill the
	// session: the loser gets invalid_grant and both ends up logged out. That
	// looks exactly like being kicked out server-side and is miserable to trace.
	unlock := m.lock(ctx)
	defer unlock()

	// Re-read: whoever held the lock may have renewed already.
	creds, err = m.store.Load()
	if err != nil {
		return "", err
	}

	if usable(creds.AccountToken, creds.AccountExpires) {
		return creds.AccountToken, nil
	}

	ep, err := m.endpoints(ctx)
	if err != nil {
		return "", err
	}

	result, err := renew(ctx, m.client, ep, m.ctx.ClientID, creds.RefreshToken)
	if err != nil {
		return "", err
	}

	creds.AccountToken = result.AccessToken
	creds.AccountExpires = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)

	if result.RefreshToken != "" {
		creds.RefreshToken = result.RefreshToken
	}

	if err := m.store.Save(creds); err != nil {
		return "", err
	}

	return creds.AccountToken, nil
}

func (m *Manager) projectToken(ctx context.Context) (string, error) {
	if m.ctx.Project == "" {
		return "", ErrNeedProject
	}

	creds, err := m.store.Load()
	if err != nil {
		return "", err
	}

	// The project has to match. A cached token from the previous project would
	// not fail — it would succeed against the wrong project.
	if creds.ProjectID == m.ctx.Project && usable(creds.ProjectToken, creds.ProjectExpires) {
		return creds.ProjectToken, nil
	}

	account, err := m.accountToken(ctx)
	if err != nil {
		return "", err
	}

	token, expires, err := m.exchange(ctx, account, m.ctx.Project)
	if err != nil {
		return "", err
	}

	creds.ProjectToken = token
	creds.ProjectExpires = expires
	creds.ProjectID = m.ctx.Project

	if err := m.store.Save(creds); err != nil {
		return "", err
	}

	return token, nil
}

// exchange trades an account token for a project token. IAM has no renewal
// endpoint for project tokens; asking for another one is the renewal.
func (m *Manager) exchange(ctx context.Context, accountToken, projectID string) (string, time.Time, error) {
	path := m.exchangePath
	if path == "" {
		return "", time.Time{}, ErrExchangeUnknown
	}

	path = strings.ReplaceAll(path, "{projectId}", url.PathEscape(projectID))

	target := m.ctx.ServiceURL(accountService) + path

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return "", time.Time{}, err
	}

	request.Header.Set("Authorization", "Bearer "+accountToken)
	request.Header.Set("Accept", "application/json")

	response, err := m.client.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))

		switch response.StatusCode {
		case http.StatusForbidden, http.StatusNotFound:
			return "", time.Time{}, fmt.Errorf("%w: %s", ErrNotAMember, projectID)
		case http.StatusUnauthorized:
			return "", time.Time{}, ErrNoCredentials
		default:
			return "", time.Time{}, fmt.Errorf("%w (%d): %s", ErrExchangeFailed, response.StatusCode, body)
		}
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
		ExpiresIn int64     `json:"expires_in"`
	}

	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %v", ErrExchangeFailed, err)
	}

	expires := result.ExpiresAt
	if expires.IsZero() && result.ExpiresIn > 0 {
		expires = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	}

	return result.Token, expires, nil
}

// loginScope is what the CLI asks for.
//
// `iam` carries the audience mapper that makes IAM accept the resulting account
// token. Without it login succeeds and every IAM call then fails with a 401
// that looks like a session problem.
//
// `offline_access` is what makes signing in last. A normal refresh token is
// bound to the SSO session, and on this realm that session is capped at ten
// hours of absolute lifespan — so a CLI would demand a fresh browser login
// twice a day no matter how actively it was used. An offline token has no such
// cap and only lapses after thirty days of not being used at all.
//
// The cost is that it is a long-lived credential sitting on disk, which is
// exactly why it belongs in the system keychain and why `leaflow logout`
// revokes it rather than only forgetting it.
const loginScope = "openid profile email iam offline_access"

// Login signs in through the browser using authorization code with PKCE.
//
// A public client has no secret to prove it is itself, so PKCE supplies one per
// attempt: the verifier stays in this process and only its SHA-256 hash is sent
// with the request, which makes an intercepted code unusable. The realm
// requires S256 for this client, so a downgrade to `plain` is refused.
//
// The redirect is a loopback listener on a port the OS picks, per RFC 8252.
// abort, when closed, gives up on the browser flow and returns
// ErrLoginAborted. The caller offers it when a keypress should switch to the
// device flow instead.
func (m *Manager) Login(ctx context.Context, notify func(target string), abort <-chan struct{}) (*Credentials, error) {
	ep, err := m.endpoints(ctx)
	if err != nil {
		return nil, err
	}

	proof, err := newPKCE()
	if err != nil {
		return nil, err
	}

	loopback, err := listenLoopback()
	if err != nil {
		return nil, err
	}

	redirectURI := loopback.redirectURI()

	target, err := authorizeURL(ep, m.ctx.ClientID, redirectURI, loginScope, proof)
	if err != nil {
		return nil, err
	}

	server := loopback.serve(proof.state)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_ = server.Shutdown(shutdown)
	}()

	// The caller decides whether to open a browser: --no-browser is a command
	// line concern, and this package should not have to know about it.
	if notify != nil {
		notify(target)
	}

	var result callbackResult

	select {
	case result = <-loopback.results:
	case <-abort:
		return nil, ErrLoginAborted
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, ErrLoginTimeout
	}

	if result.err != nil {
		return nil, result.err
	}

	tokens, err := redeem(ctx, m.client, ep, m.ctx.ClientID, result.code, redirectURI, proof.verifier)
	if err != nil {
		return nil, err
	}

	return m.persist(tokens)
}

// LoginWithDeviceCode signs in without a browser on this machine.
//
// Needed wherever a loopback redirect cannot arrive: over ssh, inside a
// container, on a headless host. The two halves of the flow may happen on
// different machines, which is the whole point of RFC 8628.
//
// PKCE applies here too. It is not part of RFC 8628, but Keycloak implements
// the extension and this client requires it, so the verifier is carried through
// to redemption exactly as in the browser flow.
func (m *Manager) LoginWithDeviceCode(ctx context.Context) (*DeviceCode, func() (*Credentials, error), error) {
	ep, err := m.endpoints(ctx)
	if err != nil {
		return nil, nil, err
	}

	if ep.Device == "" {
		return nil, nil, ErrNoDeviceFlow
	}

	proof, err := newPKCE()
	if err != nil {
		return nil, nil, err
	}

	code, err := startDevice(ctx, m.client, ep, m.ctx.ClientID, loginScope, proof)
	if err != nil {
		return nil, nil, err
	}

	wait := func() (*Credentials, error) {
		tokens, err := pollDevice(ctx, m.client, ep, m.ctx.ClientID, code, proof)
		if err != nil {
			return nil, err
		}

		return m.persist(tokens)
	}

	return code, wait, nil
}

// LoginWithRefreshToken signs in from a refresh token read elsewhere, for CI.
//
// It redeems the token immediately rather than storing it as given: that proves
// it is valid and belongs to this realm, so a bad value fails at login instead
// of at the first real command, where it would look like an outage.
//
// A refresh token is what CI wants because it is the only credential this CLI
// can renew on its own. A project token expires in minutes, which is what
// LEAFLOW_TOKEN is for.
func (m *Manager) LoginWithRefreshToken(ctx context.Context, refreshToken string) (*Credentials, error) {
	ep, err := m.endpoints(ctx)
	if err != nil {
		return nil, err
	}

	tokens, err := renew(ctx, m.client, ep, m.ctx.ClientID, strings.TrimSpace(refreshToken))
	if err != nil {
		if errors.Is(err, ErrNoCredentials) {
			return nil, ErrTokenRejected
		}

		return nil, err
	}

	if tokens.RefreshToken == "" {
		tokens.RefreshToken = strings.TrimSpace(refreshToken)
	}

	return m.persist(tokens)
}

func (m *Manager) persist(tokens *tokenResponse) (*Credentials, error) {
	creds := &Credentials{
		AccountToken:   tokens.AccessToken,
		AccountExpires: time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second),
		RefreshToken:   tokens.RefreshToken,
		Account:        readSubject(tokens.AccessToken),
		Offline:        isOfflineToken(tokens.RefreshToken),
	}

	if err := m.store.Save(creds); err != nil {
		return nil, err
	}

	return creds, nil
}

func (m *Manager) Logout(ctx context.Context) error {
	creds, err := m.store.Load()
	if err == nil && creds.RefreshToken != "" {
		if ep, derr := m.endpoints(ctx); derr == nil {
			revoke(ctx, m.client, ep, m.ctx.ClientID, creds.RefreshToken)
		}
	}

	return m.store.Clear()
}

type Status struct {
	LoggedIn       bool
	Account        string
	AccountExpires time.Time
	Project        string
	ProjectExpires time.Time
	FromEnv        bool
	Storage        string

	// Offline reports whether the stored credential outlives the SSO session.
	// Worth surfacing: it is the difference between signing in twice a day and
	// signing in once a month.
	Offline bool
}

// Status reads state without renewing or exchanging: a command for inspecting
// state should not change it.
func (m *Manager) Status() (*Status, error) {
	if token := os.Getenv(EnvToken); token != "" {
		return &Status{LoggedIn: true, FromEnv: true, Project: m.ctx.Project}, nil
	}

	creds, err := m.store.Load()
	if err != nil {
		if errors.Is(err, ErrNoCredentials) {
			return &Status{}, nil
		}

		return nil, err
	}

	return &Status{
		LoggedIn:       creds.RefreshToken != "" || creds.AccountToken != "",
		Account:        creds.Account,
		AccountExpires: creds.AccountExpires,
		Project:        creds.ProjectID,
		ProjectExpires: creds.ProjectExpires,
		Storage:        m.store.Describe(),
		Offline:        creds.Offline || isOfflineToken(creds.RefreshToken),
	}, nil
}

// InvalidateProject drops the cached project token. Switching projects without
// it means the next command runs against the previous project and succeeds.
func (m *Manager) InvalidateProject() error {
	creds, err := m.store.Load()
	if err != nil {
		if errors.Is(err, ErrNoCredentials) {
			return nil
		}

		return err
	}

	creds.ProjectToken = ""
	creds.ProjectID = ""
	creds.ProjectExpires = time.Time{}

	return m.store.Save(creds)
}

func (m *Manager) endpoints(ctx context.Context) (*endpoints, error) {
	if m.discovered != nil {
		return m.discovered, nil
	}

	found, err := discover(ctx, m.client, m.ctx.Issuer)
	if err != nil {
		return nil, err
	}

	m.discovered = found

	return found, nil
}

// lock guards renewal across processes. Failing to acquire it costs at most one
// redundant renewal, whereas blocking forever costs everything.
func (m *Manager) lock(ctx context.Context) func() {
	dir, err := config.Dir()
	if err != nil {
		return func() {}
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}
	}

	lock := flock.New(dir + "/renew." + m.contextName + ".lock")

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if ok, err := lock.TryLockContext(waitCtx, 100*time.Millisecond); err != nil || !ok {
		return func() {}
	}

	return func() { _ = lock.Unlock() }
}
