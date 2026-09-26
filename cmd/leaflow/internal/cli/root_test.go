package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeaflowNET/leaflow/internal/config"
)

func TestAnonymousCommandsWithoutLoginOrProject(t *testing.T) {
	t.Setenv("LEAFLOW_CONFIG_DIR", t.TempDir())
	for _, name := range []string{"LEAFLOW_CONFIG", "LEAFLOW_TOKEN", "LEAFLOW_PROJECT", "LEAFLOW_CONTEXT"} {
		t.Setenv(name, "")
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
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
	cfg.EditContext("").Endpoints = map[string]string{"account": server.URL, "billing/catalog": server.URL}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"account", "list-locales"}, {"billing", "catalog", "list-products"}} {
		var out, stderr bytes.Buffer
		app := &App{Out: &out, Err: &stderr, In: strings.NewReader("")}
		if code := app.Run(append(command, "-o", "json")); code != ExitOK {
			t.Fatalf("%v: code=%d stderr=%s", command, code, stderr.String())
		}
		if !strings.Contains(out.String(), `"items"`) {
			t.Fatalf("missing response: %s", out.String())
		}
	}
	var out, stderr bytes.Buffer
	app := &App{Out: &out, Err: &stderr, In: strings.NewReader("")}
	if code := app.Run([]string{"account", "list-projects"}); code == ExitOK || !strings.Contains(stderr.String(), "not logged in") {
		t.Fatalf("protected command: code=%d stderr=%s", code, stderr.String())
	}
	if requests != 2 {
		t.Fatalf("requests=%d, want only the two public calls", requests)
	}
}
