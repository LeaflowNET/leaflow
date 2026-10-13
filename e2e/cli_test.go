package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectInferenceCLI(t *testing.T) {
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "leaflow")
	build := exec.Command("go", "build", "-o", binary, "./cmd/leaflow")
	build.Dir = repo
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	command := func(t *testing.T, endpoint, id string, arguments ...string) *exec.Cmd {
		t.Helper()
		directory := t.TempDir()
		config := fmt.Sprintf("current: e2e\ncredential_store: file\ncontexts:\n  e2e:\n    project: 11111111-1111-4111-8111-111111111111\n    endpoints:\n      canopy: %q\n", endpoint)
		configPath := filepath.Join(directory, "config.yaml")
		if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		t.Cleanup(cancel)
		args := append([]string{"--config", configPath, "--output", "json", "canopy", id}, arguments...)
		cmd := exec.CommandContext(ctx, binary, args...)
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "LEAFLOW_") {
				cmd.Env = append(cmd.Env, item)
			}
		}
		cmd.Env = append(cmd.Env, "LEAFLOW_TOKEN=e2e-project-token", "LEAFLOW_CONFIG_DIR="+directory)
		return cmd
	}

	for _, example := range []struct{ name, operation, path, method, body, response string }{
		{"models", "list-inference-models", "/v1/models", "get", "", `{"object":"list","data":[{"id":"e2e-model"}]}`},
		{"completion", "create-chat-completion", "/v1/chat/completions", "post", `{"model":"e2e-model","messages":[{"role":"user","content":"hello"}]}`, `{"object":"chat.completion","choices":[{"message":{"content":"hello"}}]}`},
		{"embeddings", "create-embeddings", "/v1/embeddings", "post", `{"model":"e2e-model","input":["hello"],"dimensions":2}`, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2]}]}`},
	} {
		t.Run(example.name, func(t *testing.T) {
			observed := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != example.path || strings.ToLower(r.Method) != example.method || r.Header.Get("Authorization") != "Bearer e2e-project-token" || r.Header.Get("OpenAI-Project") != "" {
					observed <- "incorrect route, method or project credential"
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				body, _ := io.ReadAll(r.Body)
				if example.body != "" {
					var expected, actual any
					if json.Unmarshal([]byte(example.body), &expected) != nil || json.Unmarshal(body, &actual) != nil {
						observed <- "request is not JSON"
						http.Error(w, "invalid JSON", 400)
						return
					}
					want, _ := json.Marshal(expected)
					got, _ := json.Marshal(actual)
					if !bytes.Equal(want, got) {
						observed <- "request payload changed"
						http.Error(w, "changed payload", 400)
						return
					}
				}
				observed <- ""
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, example.response)
			}))
			defer server.Close()
			var arguments []string
			if example.body != "" {
				arguments = []string{"--body", "@-"}
			}
			cmd := command(t, server.URL, example.operation, arguments...)
			cmd.Stdin = strings.NewReader(example.body)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("CLI failed: %v\n%s", err, output)
			}
			if problem := <-observed; problem != "" {
				t.Fatal(problem)
			}
			var expected, actual any
			if err := json.Unmarshal(output, &actual); err != nil {
				t.Fatalf("output is not JSON: %v\n%s", err, output)
			}
			_ = json.Unmarshal([]byte(example.response), &expected)
			want, _ := json.Marshal(expected)
			got, _ := json.Marshal(actual)
			if !bytes.Equal(want, got) {
				t.Fatalf("response changed: %s", output)
			}
		})
	}

	t.Run("SSE arrives before the response ends", func(t *testing.T) {
		release := make(chan struct{})
		first := "data: {\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\n"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer e2e-project-token" {
				http.Error(w, "wrong token", 401)
				return
			}
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["stream"] != true {
				http.Error(w, "stream not requested", 400)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, first)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		}))
		defer server.Close()
		cmd := command(t, server.URL, "create-chat-completion", "--body", `{"model":"e2e-model","messages":[{"role":"user","content":"hello"}],"stream":true}`)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(stdout)
		line := make(chan string, 1)
		go func() { value, _ := reader.ReadString('\n'); line <- value }()
		select {
		case got := <-line:
			close(release)
			tail, readErr := io.ReadAll(reader)
			waitErr := cmd.Wait()
			if got+string(tail) != first+"data: [DONE]\n\n" || readErr != nil || waitErr != nil {
				t.Fatalf("stream changed or failed: %q %v %v %s", got+string(tail), readErr, waitErr, stderr.String())
			}
		case <-time.After(5 * time.Second):
			close(release)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("SSE was buffered until the response ended")
		}
	})

	t.Run("OpenAI refusal retains its code and message", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, `{"error":{"code":"E2E_PROJECT_REFUSED","message":"project inference is refused","type":"invalid_request_error"}}`)
		}))
		defer server.Close()
		cmd := command(t, server.URL, "list-inference-models")
		output, err := cmd.CombinedOutput()
		if err == nil || !bytes.Contains(output, []byte("E2E_PROJECT_REFUSED")) || !bytes.Contains(output, []byte("project inference is refused")) {
			t.Fatalf("refusal lost its meaning: %v\n%s", err, output)
		}
	})
}
