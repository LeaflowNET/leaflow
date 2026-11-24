package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeaflowNET/leaflow/pkg/naming"
	"github.com/LeaflowNET/leaflow/pkg/spec"

	"github.com/LeaflowNET/leaflow/internal/auth"
	"github.com/LeaflowNET/leaflow/internal/config"
)

func TestProjectSelectionPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, environment, flag, context, want string
	}{
		{name: "file", want: "p-file"},
		{name: "environment beats file", environment: "p-A", want: "p-A"},
		{name: "flag beats file", flag: "p-B", want: "p-B"},
		{name: "flag beats environment", environment: "p-A", flag: "p-B", want: "p-B"},
		{name: "flag with context", environment: "p-A", flag: "p-B", context: "other", want: "p-B"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEAFLOW_CONFIG_DIR", t.TempDir())
			for _, name := range []string{"LEAFLOW_CONFIG", "LEAFLOW_CONTEXT", "LEAFLOW_CURRENT", "LEAFLOW_TOKEN"} {
				t.Setenv(name, "")
			}
			t.Setenv("LEAFLOW_CREDENTIAL_STORE", "file")
			t.Setenv("LEAFLOW_PROJECT", tc.environment)
			cfg, err := config.Load("")
			if err != nil {
				t.Fatal(err)
			}
			cfg.EditContext("").Project = "p-file"
			cfg.EditContext("other").Project = "p-other"
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}

			args := []string{"project", "current", "-o", "json"}
			if tc.flag != "" {
				args = append(args, "--project", tc.flag)
			}
			if tc.context != "" {
				args = append(args, "--context", tc.context)
			}
			var out, stderr bytes.Buffer
			app := &App{Out: &out, Err: &stderr, In: strings.NewReader("")}
			if code := app.Run(args); code != ExitOK {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			var result struct{ Project, Context string }
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Project != tc.want {
				t.Errorf("project=%q, want %q", result.Project, tc.want)
			}
			wantContext := config.DefaultContextName
			if tc.context != "" {
				wantContext = tc.context
			}
			if result.Context != wantContext {
				t.Errorf("context=%q, want %q", result.Context, wantContext)
			}
			if got := os.Getenv("LEAFLOW_PROJECT"); got != tc.environment {
				t.Errorf("environment changed to %q", got)
			}
		})
	}
}

func TestProjectFlagUsesMatchingCredentials(t *testing.T) {
	t.Setenv("LEAFLOW_CONFIG_DIR", t.TempDir())
	for _, name := range []string{"LEAFLOW_CONFIG", "LEAFLOW_CONTEXT", "LEAFLOW_CURRENT", "LEAFLOW_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("LEAFLOW_CREDENTIAL_STORE", "file")
	t.Setenv("LEAFLOW_PROJECT", "p-A")

	specs, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	account, ok := specs.Service("account")
	if !ok {
		t.Fatal("missing account service")
	}
	exchange, ok := account.Operation(auth.ExchangeOperation)
	if !ok {
		t.Fatal("missing token exchange operation")
	}
	exchangePath := strings.ReplaceAll(exchange.Path, "{projectId}", "p-B")
	var exchanges, requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case exchangePath:
			exchanges.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer account-token" {
				t.Errorf("unexpected exchange request: %s %s", r.Method, r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"token":"token-B","expires_in":3600}`))
		case "/api/v1/enablement":
			requests.Add(1)
			if got := r.Header.Get("Authorization"); got != "Bearer token-B" {
				t.Errorf("authorization=%q, want project B token", got)
			}
			_, _ = w.Write([]byte(`{"status":"active"}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.EditContext("").Project = "p-file"
	cfg.EditContext("").Endpoints = map[string]string{"account": server.URL, "canopy": server.URL}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	store, err := auth.NewStore(cfg.CurrentName(), auth.StorageFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&auth.Credentials{
		AccountToken: "account-token",
		AccessToken:  "token-A",
		ProjectID:    "p-A",
	}); err != nil {
		t.Fatal(err)
	}

	var out, stderr bytes.Buffer
	app := &App{Out: &out, Err: &stderr, In: strings.NewReader("")}
	for range 2 {
		if code := app.Run([]string{"--project", "p-B", "canopy", "get-enablement", "-o", "json"}); code != ExitOK {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	}
	if got := exchanges.Load(); got != 1 {
		t.Errorf("exchanges=%d, want 1 followed by reuse of project B's cached token", got)
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("requests=%d, want 2", got)
	}
	creds, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if creds.ProjectID != "p-B" || creds.AccessToken != "token-B" {
		t.Errorf("cached credentials do not belong to project B")
	}
}

func TestAnonymousCommandsWithoutLoginOrProject(t *testing.T) {
	t.Setenv("LEAFLOW_CONFIG_DIR", t.TempDir())
	for _, name := range []string{"LEAFLOW_CONFIG", "LEAFLOW_TOKEN", "LEAFLOW_PROJECT", "LEAFLOW_CONTEXT"} {
		t.Setenv(name, "")
	}
	specs, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	operations := map[spec.Credential]*spec.Operation{}
	for _, service := range specs.Services() {
		for _, op := range service.Operations() {
			if op.Method != http.MethodGet || len(op.Inputs().Path) > 0 || op.RequiresBody() {
				continue
			}
			required := false
			for _, parameter := range op.Inputs().Query {
				required = required || parameter.Required
			}
			if !required && operations[op.Credential] == nil {
				operations[op.Credential] = op
			}
		}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("public command sent authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.CredentialStore = "file"
	cfg.EditContext("").Endpoints = map[string]string{}
	for _, op := range operations {
		cfg.EditContext("").Endpoints[op.Service] = server.URL
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, credential := range []spec.Credential{spec.NoCredential, spec.AccountToken, spec.AccessToken} {
		op := operations[credential]
		if op == nil {
			t.Fatalf("no argument-free GET available for %s credential", credential)
		}
		command := append(strings.Split(op.Service, "/"), naming.Kebab(op.ID), "-o", "json")
		var out, stderr bytes.Buffer
		app := &App{Out: &out, Err: &stderr, In: strings.NewReader("")}
		code := app.Run(command)
		if credential == spec.NoCredential {
			if code != ExitOK || !strings.Contains(out.String(), `"items"`) {
				t.Fatalf("%v: code=%d output=%s stderr=%s", command, code, out.String(), stderr.String())
			}
		} else if code != ExitAuth {
			t.Fatalf("%v: code=%d stderr=%s", command, code, stderr.String())
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests=%d, want only the public call", got)
	}
}
