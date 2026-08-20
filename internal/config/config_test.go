package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeaflowNET/leaflow/internal/config"
)

func inTempDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("LEAFLOW_CONFIG_DIR", dir)

	return dir
}

// Save wrote through viper before, which reflects over the struct and ignores
// its tags: ClientID went out as "clientid" and came back as nothing, so a
// custom client id silently did not persist.
func TestSaveAndLoadRoundTrip(t *testing.T) {
	inTempDir(t)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}

	one := cfg.EditContext("local")
	one.Domain = "leaflow.test"
	one.ClientID = "custom-client"
	one.Issuer = "https://auth.example.test/realms/other"
	one.Project = "p-1"
	one.Endpoints = map[string]string{"compute": "https://compute.leaflow.test:18100"}
	cfg.Current = "local"

	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}

	got := reloaded.Context()

	for _, tc := range []struct{ field, got, want string }{
		{"domain", got.Domain, "leaflow.test"},
		{"client_id", got.ClientID, "custom-client"},
		{"issuer", got.Issuer, "https://auth.example.test/realms/other"},
		{"project", got.Project, "p-1"},
		{"endpoint", got.Endpoints["compute"], "https://compute.leaflow.test:18100"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

// Defaults must not reach the file: freezing today's values into every config
// means changing one later reaches nobody who has already run the CLI.
func TestSaveDoesNotPersistDefaults(t *testing.T) {
	dir := inTempDir(t)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}

	if domain := cfg.Context().Domain; domain != config.DefaultDomain {
		t.Fatalf("effective domain = %q, want the default", domain)
	}

	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, leaked := range []string{config.DefaultDomain, config.DefaultIssuer, config.DefaultClientID} {
		if strings.Contains(string(written), leaked) {
			t.Errorf("default %q was written to the config:\n%s", leaked, written)
		}
	}
}

// Reading the effective context must not mutate what gets saved.
func TestContextIsACopy(t *testing.T) {
	inTempDir(t)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}

	effective := cfg.Context()
	effective.Domain = "mutated.test"
	effective.Endpoints = map[string]string{"compute": "https://nope.test"}

	if stored := cfg.EditContext(""); stored.Domain != "" || len(stored.Endpoints) != 0 {
		t.Errorf("stored context was modified through the copy: %#v", stored)
	}
}

func TestServiceURLDerivesFromDomain(t *testing.T) {
	inTempDir(t)

	cfg, _ := config.Load("")

	one := cfg.EditContext("")
	one.Domain = "leaflow.test"
	one.Endpoints = map[string]string{"compute": "https://compute.leaflow.test:18100/"}

	active := cfg.Context()

	if got := active.ServiceURL("monitoring"); got != "https://monitoring.leaflow.test" {
		t.Errorf("derived address = %q", got)
	}

	// An override wins, and its trailing slash is dropped so paths concatenate.
	if got := active.ServiceURL("compute"); got != "https://compute.leaflow.test:18100" {
		t.Errorf("overridden address = %q", got)
	}
}

func TestEnvironmentBeatsFile(t *testing.T) {
	inTempDir(t)

	cfg, _ := config.Load("")
	cfg.EditContext("").Domain = "from-file.test"

	t.Setenv("LEAFLOW_DOMAIN", "from-env.test")
	t.Setenv("LEAFLOW_PROJECT", "p-env")

	active := cfg.Context()

	if active.Domain != "from-env.test" {
		t.Errorf("domain = %q, want the environment to win", active.Domain)
	}

	if active.Project != "p-env" {
		t.Errorf("project = %q, want the environment to win", active.Project)
	}
}

// A config named explicitly and missing is a typo worth reporting; a missing
// default config is just a fresh install.
func TestMissingExplicitConfigIsAnError(t *testing.T) {
	inTempDir(t)

	if _, err := config.Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("a missing --config file was accepted")
	}

	if _, err := config.Load(""); err != nil {
		t.Errorf("a missing default config should be fine, got %v", err)
	}
}
