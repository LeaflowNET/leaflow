// Package config holds what the CLI remembers between runs: which platform to
// reach, which project is current, how to print.
//
// Credentials are not here — see internal/auth. Config files get pasted into
// issues and synced into dotfile repos; tokens must not.
package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var (
	// ErrConfigNotFound is only raised when a config was named explicitly and is
	// missing. A missing default config is normal: a fresh install has none.
	ErrConfigNotFound = errors.New("config file not found")

	ErrNoHomeDir = errors.New("cannot locate home directory")

	ErrConfigUnreadable = errors.New("cannot read config")

	ErrConfigMalformed = errors.New("malformed config")

	ErrNoServiceAddress = errors.New("no address for service")
)

// Context is one "which platform, as whom, in which project" triple, modelled
// on kubectl's: people work against production and a local stack at the same
// time, and a per-command flag is something you eventually forget to pass.
type Context struct {
	// Domain rewrites the domain part of every address a contract declares, for
	// a self-hosted deployment or a local stack: compute.leaflow.cloud becomes
	// compute.leaflow.test.
	//
	// It is a rewrite of a stated address, not a way of deriving one. Left
	// empty — which is the default — the contract is used exactly as written.
	Domain string `mapstructure:"domain" yaml:"domain,omitempty"`

	// Endpoints overrides Domain per service, for local stacks whose addresses
	// are not derivable.
	Endpoints map[string]string `mapstructure:"endpoints" yaml:"endpoints,omitempty"`

	// Issuer must include the realm. The end-user portal and the operator console
	// are separate realms backed by separate account stores.
	Issuer string `mapstructure:"issuer" yaml:"issuer,omitempty"`

	// ClientID must be a public client: this binary ships to every user's machine
	// and cannot hold a secret.
	ClientID string `mapstructure:"client_id" yaml:"client_id,omitempty"`

	// Project is the current project. A project token already names its project,
	// so no request path carries one — which makes the project a property of the
	// context rather than an argument to every command.
	Project string `mapstructure:"project" yaml:"project,omitempty"`

	// Account is a display-only snapshot taken at login.
	Account string `mapstructure:"account,omitempty" yaml:"account,omitempty"`
}

type Config struct {
	Current string `mapstructure:"current" yaml:"current"`
	// CredentialStore is auto, keychain or file. Not every machine has a
	// keychain, and on some the prompt it raises is itself the problem.
	CredentialStore string              `mapstructure:"credential_store" yaml:"credential_store,omitempty"`
	Contexts        map[string]*Context `mapstructure:"contexts" yaml:"contexts,omitempty"`
	Output          string              `mapstructure:"output" yaml:"output,omitempty"`

	path string `mapstructure:"-" yaml:"-"`
}

const (
	DefaultIssuer = "https://auth.leaflow.net/realms/user"

	// DefaultClientID is a public client dedicated to the CLI. The console's
	// client is confidential and cannot be reused here; a separate one also lets
	// the device flow be enabled for the CLI alone.
	DefaultClientID = "leaflow-cli"

	DefaultContextName = "default"
)

const envPrefix = "LEAFLOW"

func Dir() (string, error) {
	if dir := os.Getenv("LEAFLOW_CONFIG_DIR"); dir != "" {
		return dir, nil
	}

	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "leaflow"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoHomeDir, err)
	}

	return filepath.Join(home, ".config", "leaflow"), nil
}

// Load reads the config named by path, or the default one when path is empty.
//
// Precedence is --config > LEAFLOW_CONFIG > default location, for the same
// reason kubectl has --kubeconfig and KUBECONFIG: in CI the config is injected
// by the pipeline and cannot be required to sit under $HOME.
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("LEAFLOW_CONFIG")
	}

	v := viper.New()
	v.SetConfigType("yaml")

	resolved := path

	if path != "" {
		v.SetConfigFile(path)
	} else {
		dir, err := Dir()
		if err != nil {
			return nil, err
		}

		v.SetConfigName("config")
		v.AddConfigPath(dir)
		resolved = filepath.Join(dir, "config.yaml")
	}

	v.SetDefault("current", DefaultContextName)
	v.SetDefault("output", "table")

	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if !isNotFound(err) {
			return nil, fmt.Errorf("%w %s: %v", ErrConfigUnreadable, resolved, err)
		}

		if path != "" {
			return nil, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
	}

	cfg := &Config{path: resolved}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("%w %s: %v", ErrConfigMalformed, resolved, err)
	}

	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*Context{}
	}

	if name := os.Getenv("LEAFLOW_CONTEXT"); name != "" {
		cfg.Current = name
	}

	if store := os.Getenv("LEAFLOW_CREDENTIAL_STORE"); store != "" {
		cfg.CredentialStore = store
	}

	return cfg, nil
}

func isNotFound(err error) bool {
	if _, ok := err.(viper.ConfigFileNotFoundError); ok {
		return true
	}

	// viper reports a missing directory as *fs.PathError, not its own type.
	return os.IsNotExist(err)
}

// Context returns the effective current context: a copy with defaults and
// environment applied.
//
// A copy, because defaults must not reach the file. Filling them in on read and
// saving afterwards would freeze today's values into every config, and changing
// a default later would then reach nobody who had already run the CLI once.
//
// Failing here would break `leaflow --help` on a clean machine; the useful
// place to stop is the first request, where the error is "not logged in".
func (c *Config) Context() *Context {
	stored := c.EditContext(c.CurrentName())

	effective := *stored
	effective.Endpoints = maps.Clone(stored.Endpoints)

	effective.applyDefaults()
	effective.applyEnv()

	return &effective
}

// EditContext returns the stored context, creating it when absent. Changes made
// through it are what Save writes; only what a user set explicitly ends up in
// the file.
func (c *Config) EditContext(name string) *Context {
	if name == "" {
		name = c.CurrentName()
	}

	if c.Contexts == nil {
		c.Contexts = map[string]*Context{}
	}

	ctx, ok := c.Contexts[name]
	if !ok || ctx == nil {
		ctx = &Context{}
		c.Contexts[name] = ctx
	}

	return ctx
}

func (c *Config) CurrentName() string {
	if c.Current == "" {
		return DefaultContextName
	}

	return c.Current
}

func (x *Context) applyDefaults() {
	if x.Issuer == "" {
		x.Issuer = DefaultIssuer
	}

	if x.ClientID == "" {
		x.ClientID = DefaultClientID
	}
}

// applyEnv lets the environment win over the file: a config baked into a CI
// image describes the image, while the environment describes this run.
func (x *Context) applyEnv() {
	for _, pair := range []struct {
		env    string
		target *string
	}{
		{"LEAFLOW_DOMAIN", &x.Domain},
		{"LEAFLOW_ISSUER", &x.Issuer},
		{"LEAFLOW_CLIENT_ID", &x.ClientID},
		{"LEAFLOW_PROJECT", &x.Project},
	} {
		if value := os.Getenv(pair.env); value != "" {
			*pair.target = value
		}
	}
}

// ServiceURL is where a service answers.
//
// Resolution order: an explicit override, then the contract's own address with
// Domain applied if one is set, then the contract's address as written.
//
// Nothing is derived from the service name. That convention holds for every
// service but one, and the exception answers 404 — which reads as "no such
// endpoint" rather than "right address, wrong face". A contract that states no
// address produces an error naming the contract, because that is where the fix
// belongs.
func (x *Context) ServiceURL(service, declared string) (string, error) {
	if endpoint, ok := x.Endpoints[service]; ok && endpoint != "" {
		return strings.TrimRight(endpoint, "/"), nil
	}

	if declared == "" {
		return "", fmt.Errorf("%w: %s declares no address; set one with --endpoint %s=<url>",
			ErrNoServiceAddress, service, service)
	}

	if x.Domain == "" {
		return declared, nil
	}

	rewritten, err := rewriteDomain(declared, x.Domain)
	if err != nil {
		return "", err
	}

	return rewritten, nil
}

// rewriteDomain swaps everything after the first label of the host, so
// compute.leaflow.cloud becomes compute.leaflow.test while keeping the port
// and scheme a local stack needs.
func rewriteDomain(declared, domain string) (string, error) {
	parsed, err := url.Parse(declared)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrNoServiceAddress, declared, err)
	}

	// Read before writing: assigning Host discards the port, and reading it
	// afterwards returns nothing.
	port := parsed.Port()
	host := parsed.Hostname()

	label, _, found := strings.Cut(host, ".")
	if !found {
		label = host
	}

	parsed.Host = label + "." + domain

	if port != "" {
		parsed.Host += ":" + port
	}

	return strings.TrimRight(parsed.String(), "/"), nil
}

// Save writes the config back with 0600: it carries no tokens, but it does say
// which platform and which project you work on.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("%w: %v", ErrConfigUnreadable, err)
	}

	// An untouched context is not written: `default: {}` is noise, and its
	// absence means the same thing.
	saved := *c
	saved.Contexts = map[string]*Context{}

	for name, one := range c.Contexts {
		if one != nil && !one.isEmpty() {
			saved.Contexts[name] = one
		}
	}

	// Marshalled directly rather than through viper: viper.Set reflects over the
	// struct and ignores its tags, so ClientID was written as "clientid" and read
	// back as nothing — a custom client id silently would not persist.
	encoded, err := yaml.Marshal(&saved)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrConfigUnreadable, err)
	}

	if err := os.WriteFile(c.path, encoded, 0o600); err != nil {
		return fmt.Errorf("%w: %v", ErrConfigUnreadable, err)
	}

	return nil
}

func (x *Context) isEmpty() bool {
	return x.Domain == "" && x.Issuer == "" && x.ClientID == "" &&
		x.Project == "" && x.Account == "" && len(x.Endpoints) == 0
}

func (c *Config) Path() string { return c.path }
