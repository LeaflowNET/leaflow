package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeaflowNET/leaflow/pkg/naming"
	"github.com/LeaflowNET/leaflow/pkg/spec"

	"github.com/LeaflowNET/leaflow/internal/config"
)

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
