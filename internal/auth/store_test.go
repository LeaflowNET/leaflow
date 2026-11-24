package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

type fakeKeyring struct {
	items     map[string]string
	setErr    error
	getErr    error
	deleteErr error
}

func newFakeKeyring() *fakeKeyring {
	return &fakeKeyring{items: map[string]string{}}
}

func (f *fakeKeyring) key(service, user string) string {
	return service + "\x00" + user
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}

	v, ok := f.items[f.key(service, user)]
	if !ok {
		return "", keyring.ErrNotFound
	}

	return v, nil
}

func (f *fakeKeyring) Set(service, user, password string) error {
	if f.setErr != nil {
		return f.setErr
	}

	f.items[f.key(service, user)] = password

	return nil
}

func (f *fakeKeyring) Delete(service, user string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}

	k := f.key(service, user)
	if _, ok := f.items[k]; !ok {
		return keyring.ErrNotFound
	}

	delete(f.items, k)

	return nil
}

func newTestStore(t *testing.T, mode StorageMode, kr *fakeKeyring) *Store {
	t.Helper()

	return &Store{
		contextName: "default",
		fallback:    filepath.Join(t.TempDir(), "credentials", "default.json"),
		mode:        mode,
		keyring:     kr,
	}
}

// Regression: when a newly issued credential payload exceeds the OS keychain
// limit (e.g. macOS security(1) 4096-byte command limit), Save falls back to
// the credentials file. Leaving a stale entry in the keychain caused Load to
// return the expired keychain token and report "not logged in" right after login.
func TestStoreFallbackRemovesStaleKeychainEntry(t *testing.T) {
	kr := newFakeKeyring()
	store := newTestStore(t, StorageAuto, kr)

	stale := &Credentials{
		Account:        "old@example.com",
		AccountToken:   "stale-token",
		AccountExpires: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
		RefreshToken:   "stale-refresh",
	}
	if err := store.Save(stale); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}

	kr.setErr = keyring.ErrSetDataTooBig

	fresh := &Credentials{
		Account:        "fresh@example.com",
		AccountToken:   strings.Repeat("a", 3200),
		AccountExpires: time.Now().Add(5 * time.Minute),
		RefreshToken:   "fresh-refresh",
	}
	if err := store.Save(fresh); err != nil {
		t.Fatalf("fallback save failed: %v", err)
	}

	if _, err := kr.Get(keyringService, "default"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("stale keychain entry was not removed after fallback save: err=%v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if loaded.Account != fresh.Account || loaded.RefreshToken != fresh.RefreshToken {
		t.Fatalf("Load() returned stale credentials: got %+v, want %+v", loaded, fresh)
	}

	if !strings.HasPrefix(store.Describe(), "file ") {
		t.Fatalf("Describe() = %q, want file fallback", store.Describe())
	}
}

// Upgrade path: if an older CLI build already left both a stale keychain entry
// and a newer fallback file on disk, Load in auto mode must read the fallback
// file (matching Describe) instead of being shadowed by the stale keychain item.
func TestStoreAutoPrefersFallbackFileWhenBothExist(t *testing.T) {
	kr := newFakeKeyring()
	store := newTestStore(t, StorageAuto, kr)

	if err := store.Save(&Credentials{
		Account:      "stale@example.com",
		RefreshToken: "stale-refresh",
	}); err != nil {
		t.Fatalf("seed keychain: %v", err)
	}

	if err := store.saveFile([]byte(`{"account":"fresh@example.com","refresh_token":"fresh-refresh"}`)); err != nil {
		t.Fatalf("seed fallback file: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if loaded.Account != "fresh@example.com" || loaded.RefreshToken != "fresh-refresh" {
		t.Fatalf("Load() shadowed by stale keychain entry: %+v", loaded)
	}

	if !strings.HasPrefix(store.Describe(), "file ") {
		t.Fatalf("Describe() = %q, want file fallback", store.Describe())
	}
}

func TestStoreKeychainSaveRemovesFallbackFile(t *testing.T) {
	kr := newFakeKeyring()
	store := newTestStore(t, StorageAuto, kr)

	if err := store.saveFile([]byte(`{"account":"old-file@example.com"}`)); err != nil {
		t.Fatalf("seed fallback file: %v", err)
	}

	if err := store.Save(&Credentials{Account: "keychain@example.com"}); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	if _, err := os.Stat(store.fallback); !os.IsNotExist(err) {
		t.Fatalf("fallback file should have been removed, stat err=%v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if loaded.Account != "keychain@example.com" {
		t.Fatalf("Load() account = %q, want keychain@example.com", loaded.Account)
	}

	if store.Describe() != "system keychain" {
		t.Fatalf("Describe() = %q, want system keychain", store.Describe())
	}
}

func TestStoreKeychainModeRefusesFallback(t *testing.T) {
	kr := newFakeKeyring()
	kr.setErr = keyring.ErrSetDataTooBig
	store := newTestStore(t, StorageKeychain, kr)

	err := store.Save(&Credentials{Account: "user@example.com"})
	if !errors.Is(err, ErrNoKeychain) {
		t.Fatalf("Save() err = %v, want ErrNoKeychain", err)
	}

	if store.Describe() != "system keychain" {
		t.Fatalf("Describe() = %q, want system keychain", store.Describe())
	}
}

func TestStoreFileModeIgnoresKeychainAndCleansStaleEntry(t *testing.T) {
	kr := newFakeKeyring()
	_ = kr.Set(keyringService, "default", `{"account":"stale@example.com"}`)
	store := newTestStore(t, StorageFile, kr)

	if _, err := store.Load(); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("file mode Load() should ignore keychain, got err=%v", err)
	}

	if err := store.Save(&Credentials{Account: "file@example.com"}); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	if _, err := kr.Get(keyringService, "default"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("file mode Save() should remove stale keychain entry, err=%v", err)
	}

	loaded, err := store.Load()
	if err != nil || loaded.Account != "file@example.com" {
		t.Fatalf("Load() = (%+v, %v), want file@example.com", loaded, err)
	}
}

func TestStoreFallbackSucceedsWhenKeychainUnavailable(t *testing.T) {
	kr := newFakeKeyring()
	backendErr := errors.New("no dbus session")
	kr.setErr = backendErr
	kr.getErr = backendErr
	kr.deleteErr = backendErr
	store := newTestStore(t, StorageAuto, kr)

	if err := store.Save(&Credentials{Account: "headless@example.com"}); err != nil {
		t.Fatalf("Save() should succeed when keychain is unreachable: %v", err)
	}

	loaded, err := store.Load()
	if err != nil || loaded.Account != "headless@example.com" {
		t.Fatalf("Load() = (%+v, %v), want headless@example.com", loaded, err)
	}
}
