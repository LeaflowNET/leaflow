package leaflow

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeaflowNET/leaflow/pkg/spec"
	"github.com/LeaflowNET/leaflow/pkg/transport"
)

type mediaCredentials struct{ refreshed bool }

func (c *mediaCredentials) Token(context.Context, spec.Credential) (string, error) {
	if c.refreshed {
		return "fresh", nil
	}
	return "old", nil
}
func (c *mediaCredentials) Invalidate(spec.Credential) error { c.refreshed = true; return nil }

type mediaSource struct{ opens int }

func (s *mediaSource) Open(context.Context) (io.ReadCloser, error) {
	s.opens++
	return io.NopCloser(strings.NewReader("audio bytes")), nil
}

func TestMediaMultipartReopensAfterAuthenticationRefresh(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		body, _ := io.ReadAll(file)
		if string(body) != "audio bytes" || header.Filename != "recording.wav" || header.Header.Get("Content-Type") != "audio/wav" {
			t.Errorf("wrong upload: %s %+v", body, header)
		}
		if r.FormValue("model") != "transcriber" || len(r.MultipartForm.Value["timestamp_granularities[]"]) != 2 {
			t.Error("multipart values lost")
		}
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"code":"TOKEN_EXPIRED","message":"expired"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fresh" {
			t.Error("credentials were not refreshed")
		}
		io.WriteString(w, `{"text":"hello"}`)
	}))
	defer server.Close()
	client, err := New(Options{Services: []string{"canopy"}, Endpoints: transport.Endpoints{Overrides: map[string]string{"canopy": server.URL}}, Credentials: &mediaCredentials{}})
	if err != nil {
		t.Fatal(err)
	}
	source := &mediaSource{}
	result, err := client.Call(t.Context(), Call{
		Service: "canopy", Operation: "create-transcription",
		Arguments: map[string]any{"body": map[string]any{"model": "transcriber", "file": "recording.wav", "timestamp_granularities[]": []any{"word", "segment"}}},
		Files:     map[string][]transport.File{"file": {{Filename: "recording.wav", MediaType: "audio/wav", Source: source}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || source.opens != 2 || result.Value.(map[string]any)["text"] != "hello" {
		t.Fatalf("attempts=%d opens=%d result=%+v", attempts, source.opens, result)
	}
}

func TestMediaResponsesAndMissingReceiver(t *testing.T) {
	for _, test := range []struct {
		name, media, body           string
		status                      int
		accept, wantFile, wantError bool
	}{
		{"audio", "audio/mpeg", "audio bytes", 200, true, true, false},
		{"error", "application/json", `{"error":{"code":"MODEL_ACCESS_REQUIRED","message":"not allowed"}}`, 403, true, false, true},
		{"text", "text/plain", "transcribed text", 200, true, true, false},
		{"missing receiver", "audio/mpeg", "audio bytes", 200, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", test.media)
				w.Header().Set("Content-Disposition", `attachment; filename="voice.mp3"`)
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			client, err := New(Options{Services: []string{"canopy"}, Endpoints: transport.Endpoints{Overrides: map[string]string{"canopy": server.URL}}, Credentials: &mediaCredentials{}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Call(t.Context(), Call{Service: "canopy", Operation: "create-speech", Arguments: map[string]any{"body": map[string]any{"model": "tts", "input": "hello", "voice": "test"}}, AcceptFiles: test.accept})
			if (err != nil) != test.wantError {
				t.Fatalf("err=%v", err)
			}
			if !test.accept && calls != 0 {
				t.Fatal("executed without a file receiver")
			}
			if test.wantFile {
				if result.File == nil {
					t.Fatal("missing file")
				}
				defer result.File.Body.Close()
				body, err := io.ReadAll(result.File.Body)
				if err != nil || string(body) != test.body || result.File.Filename != "voice.mp3" {
					t.Fatalf("file=%+v body=%s err=%v", result.File, body, err)
				}
			}
		})
	}
}

func TestMediaFileFieldsComeFromTheContract(t *testing.T) {
	client, err := New(Options{Services: []string{"canopy"}})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := client.Operation("canopy", "create-transcription")
	if err != nil {
		t.Fatal(err)
	}
	fields := operation.FileFields()
	repeated, present := fields["file"]
	if !present || repeated || len(fields) != 1 || operation.RequestMediaType() != "multipart/form-data" {
		t.Fatalf("fields=%v", fields)
	}

	editOp, err := client.Operation("canopy", "create-image-edit")
	if err != nil {
		t.Fatal(err)
	}
	editFields := editOp.FileFields()
	if !editFields["image"] || editFields["mask"] || len(editFields) != 2 || editOp.RequestMediaType() != "multipart/form-data" {
		t.Fatalf("image edit fields=%v media=%s", editFields, editOp.RequestMediaType())
	}
}
