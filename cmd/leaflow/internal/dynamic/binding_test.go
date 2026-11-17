package dynamic

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/spf13/pflag"

	"github.com/LeaflowNET/leaflow/pkg/output"
	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/LeaflowNET/leaflow/pkg/transport"
)

type staticCreds struct{}

func (staticCreds) Token(context.Context, spec.Credential) (string, error) { return "tok", nil }
func (staticCreds) Invalidate(spec.Credential) error                       { return nil }

func TestRegisterFlagTreatsBackticksAsDescription(t *testing.T) {
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	registerFlag(flags, "resource-type", &openapi3.Schema{}, "Give it together with `resource_id`.")
	name, usage := pflag.UnquoteUsage(flags.Lookup("resource-type"))
	if name != "string" || !strings.Contains(usage, "resource_id") {
		t.Fatalf("got placeholder %q and usage %q", name, usage)
	}
}

func TestCommandUploadsRepeatedImageFilesInOrder(t *testing.T) {
	dir := t.TempDir()
	img1 := filepath.Join(dir, "person.png")
	img2 := filepath.Join(dir, "coat.jpg")
	if err := os.WriteFile(img1, []byte("png-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(img2, []byte("jpg-2"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		defer r.MultipartForm.RemoveAll()
		uploads := r.MultipartForm.File["image"]
		if len(uploads) != 2 || uploads[0].Filename != "person.png" || uploads[1].Filename != "coat.jpg" {
			t.Fatalf("unexpected uploads: %+v", uploads)
		}
		f0, _ := uploads[0].Open()
		b0, _ := io.ReadAll(f0)
		_ = f0.Close()
		if string(b0) != "png-1" {
			t.Fatalf("unexpected first file bytes: %s", b0)
		}
		if r.FormValue("model") != "qwen-image-3.0-pro" || r.FormValue("prompt") != "换外套" {
			t.Fatalf("unexpected form fields: %v", r.MultipartForm.Value)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"url":"https://cdn.example/out.png"}]}`)
	}))
	defer server.Close()

	set, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rt := &Runtime{
		Client:  transport.New(transport.Endpoints{Overrides: map[string]string{"canopy": server.URL}}, staticCreds{}, server.Client()),
		Printer: &output.Printer{Format: output.FormatJSON, Out: &out},
	}
	roots := Build(set, rt)
	var canopyCmd = roots[0]
	for _, root := range roots {
		if root.Use == "canopy" {
			canopyCmd = root
			break
		}
	}
	canopyCmd.SetOut(&out)
	canopyCmd.SetArgs([]string{"create-image-edit", "--model", "qwen-image-3.0-pro", "--prompt", "换外套", "--image", img1, "--image", "@" + img2})
	if err := canopyCmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://cdn.example/out.png") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}
