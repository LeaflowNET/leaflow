// Package auth owns the CLI's credentials: how they are obtained, where they
// are kept, and when they are replaced.
//
// The platform issues two tokens and this package handles both:
//
//	access token   from Keycloak, says who you are, renewed with a refresh token
//	scoped token   from IAM, says who you are and where, obtained by exchange
//
// Scoped tokens are not renewable by design. One asserts that you are still a
// member of that project, and membership changes; renewal would extend the
// claim without re-proving it.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/LeaflowNET/leaflow/internal/config"
)

const keyringService = "leaflow-cli"

var (
	// ErrNoCredentials means this context has never logged in, or its refresh
	// token is gone. Callers turn it into "run leaflow login" rather than
	// surfacing a keyring failure.
	ErrNoCredentials = errors.New("not logged in")

	ErrCredentialsUnwritable = errors.New("cannot store credentials")

	ErrCredentialsUnreadable = errors.New("cannot read credentials")

	ErrNoKeychain = errors.New("no usable system keychain")
)

// StorageMode says where credentials are kept.
//
// The system keychain is the default on all three platforms, reached through
// the OS API directly — macOS Keychain, Windows Credential Manager, and the
// freedesktop Secret Service on Linux — so nothing extra has to be installed.
//
// It is not always available: CI containers, machines reached over ssh, and
// Linux without a session bus have no keychain. StorageFile is for those, and
// choosing it explicitly is better than discovering the fallback by accident.
type StorageMode string

const (
	// StorageAuto uses the keychain and falls back to a file.
	StorageAuto StorageMode = "auto"

	// StorageKeychain refuses to fall back. Worth setting where a credential
	// silently landing in a file would be a problem worth failing over.
	StorageKeychain StorageMode = "keychain"

	// StorageFile always writes a 0600 file and never touches the keychain,
	// which also avoids the OS prompting for access on every run.
	StorageFile StorageMode = "file"
)

var StorageModes = []string{
	string(StorageAuto),
	string(StorageKeychain),
	string(StorageFile),
}

func ParseStorageMode(value string) (StorageMode, error) {
	switch StorageMode(value) {
	case "":
		return StorageAuto, nil
	case StorageAuto, StorageKeychain, StorageFile:
		return StorageMode(value), nil
	default:
		return "", fmt.Errorf("%w: unknown credential store %q", ErrCredentialsUnwritable, value)
	}
}

// Credentials is one context's full credential set. Keeping them per context
// matters because working against production and a local stack at once is
// normal, and mixing the two only ever shows up as a 401.
type Credentials struct {
	AccountToken   string    `json:"account_token,omitempty"`
	AccountExpires time.Time `json:"account_expires"`
	RefreshToken   string    `json:"refresh_token,omitempty"`

	Account string `json:"account,omitempty"`

	// Offline records that the refresh token is an offline token, which is not
	// tied to an SSO session and so does not expire when that one does.
	Offline bool `json:"offline,omitempty"`

	// Renaming these makes an entry written by an older version read as having
	// no access token. That is a cheap thing to be wrong about: the refresh
	// token and the access token are untouched, so the next call exchanges for
	// a fresh one and nobody is asked to sign in again.
	AccessToken        string    `json:"access_token,omitempty"`
	AccessTokenExpires time.Time `json:"access_token_expires"`

	// ProjectID records which project the cached token belongs to. Switching
	// projects without clearing it would not fail — it would succeed against the
	// wrong project, which is worse than any error.
	ProjectID string `json:"project_id,omitempty"`
}

type keyringBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (osKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (osKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

type Store struct {
	contextName string
	fallback    string
	mode        StorageMode
	keyring     keyringBackend
}

func NewStore(contextName string, mode StorageMode) (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}

	if mode == "" {
		mode = StorageAuto
	}

	return &Store{
		contextName: contextName,
		fallback:    filepath.Join(dir, "credentials", contextName+".json"),
		mode:        mode,
		keyring:     osKeyring{},
	}, nil
}

func (s *Store) backend() keyringBackend {
	if s.keyring != nil {
		return s.keyring
	}

	return osKeyring{}
}

func (s *Store) Load() (*Credentials, error) {
	if s.mode == StorageFile {
		return s.loadFile()
	}

	if s.mode == StorageAuto {
		// In auto mode, Save removes the fallback file whenever the keychain
		// succeeds, so a file on disk means credentials fell back here (for
		// example after outgrowing the OS keychain size limit). Prefer it over
		// any stale keychain entry left behind by an older CLI build.
		if creds, err := s.loadFile(); err == nil {
			return creds, nil
		} else if !errors.Is(err, ErrNoCredentials) {
			return nil, err
		}
	}

	data, err := s.backend().Get(keyringService, s.contextName)
	if err == nil {
		return decode([]byte(data))
	}

	if s.mode == StorageKeychain {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrNoCredentials
		}

		return nil, fmt.Errorf("%w: %v", ErrNoKeychain, err)
	}

	// A keychain that exists but cannot be opened (locked, no session bus)
	// or has no entry means the context is not logged in once the fallback
	// file has already been checked above.
	return nil, ErrNoCredentials
}

func (s *Store) loadFile() (*Credentials, error) {
	raw, err := os.ReadFile(s.fallback)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCredentials
		}

		return nil, fmt.Errorf("%w: %v", ErrCredentialsUnreadable, err)
	}

	return decode(raw)
}

// decode treats corrupt storage as "not logged in": logging in again fixes it,
// which beats asking someone to delete a file they have never heard of.
func decode(data []byte) (*Credentials, error) {
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, ErrNoCredentials
	}

	return &creds, nil
}

func (s *Store) Save(creds *Credentials) error {
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialsUnwritable, err)
	}

	if s.mode != StorageFile {
		if err := s.backend().Set(keyringService, s.contextName, string(data)); err == nil {
			// Never leave two copies: a revoked keychain entry with a stale file
			// beside it would still work, which is the whole reason this removal
			// is not best-effort. A file that will not go is reported, because
			// silently keeping a second copy of someone's credentials is the one
			// outcome this function exists to prevent.
			if err := os.Remove(s.fallback); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%w: credentials were saved to the keychain but %s could not be removed, "+
					"and a stale copy there would still work: %v",
					ErrCredentialsUnwritable, s.fallback, err)
			}

			return nil
		} else if s.mode == StorageKeychain {
			return fmt.Errorf("%w: %v", ErrNoKeychain, err)
		}
	}

	if err := s.saveFile(data); err != nil {
		return err
	}

	// Never leave a stale keychain entry beside the fallback file: when a
	// fresh token payload exceeds the OS keychain entry limit (e.g. macOS
	// security(1) 4096-byte limit or Windows Credential Manager 2560-byte
	// limit), leaving an older entry in the keychain would keep a second copy
	// around and break older readers or logout invariants.
	if err := s.backend().Delete(keyringService, s.contextName); err != nil &&
		!errors.Is(err, keyring.ErrNotFound) {
		if _, getErr := s.backend().Get(keyringService, s.contextName); getErr == nil {
			return fmt.Errorf("%w: credentials were saved to %s but the previous keychain entry could not be removed: %v",
				ErrCredentialsUnwritable, s.fallback, err)
		}
	}

	return nil
}

func (s *Store) saveFile(data []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.fallback), 0o700); err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialsUnwritable, err)
	}

	// Written to a temporary file and renamed: an interrupted write would
	// otherwise leave a truncated file that reads as "not logged in".
	tmp := s.fallback + ".tmp"

	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialsUnwritable, err)
	}

	if err := os.Rename(tmp, s.fallback); err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialsUnwritable, err)
	}

	return nil
}

// Clear removes both copies regardless of mode. Logging out has to be complete,
// including a file left behind by an earlier run under a different setting.
func (s *Store) Clear() error {
	if err := s.backend().Delete(keyringService, s.contextName); err != nil &&
		!errors.Is(err, keyring.ErrNotFound) {
		// An unopenable keychain is not a logout failure; removing the file is
		// what matters.
		_ = err
	}

	if err := os.Remove(s.fallback); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w: %v", ErrCredentialsUnwritable, err)
	}

	return nil
}

func (s *Store) Fallback() string {
	return s.fallback
}

// Describe says where credentials actually live, for `leaflow auth status`.
func (s *Store) Describe() string {
	switch s.mode {
	case StorageFile:
		return "file " + s.fallback
	case StorageKeychain:
		return "system keychain"
	default:
		if _, err := os.Stat(s.fallback); err == nil {
			return "file " + s.fallback
		}

		return "system keychain"
	}
}

// isUsable allows 30 seconds of slack: a token with two seconds left expires in
// flight, and that surfaces as an unexplained 401.
func isUsable(token string, expires time.Time) bool {
	if token == "" {
		return false
	}

	// An unknown expiry is treated as valid; a 401 has a retry path, while
	// assuming expiry would double every request.
	if expires.IsZero() {
		return true
	}

	return time.Now().Add(30 * time.Second).Before(expires)
}
