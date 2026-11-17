package transport

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

type stringFileSource string

func (s stringFileSource) Open(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s))), nil
}

func TestWriteMultipartUsesBracketFieldNameForRepeatedImages(t *testing.T) {
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	values := map[string]any{
		"model":  "gpt-image-2",
		"prompt": "combine",
		"image":  []any{"a.png", "b.png"},
	}
	files := map[string][]File{
		"image": {
			{Filename: "a.png", MediaType: "image/png", Source: stringFileSource("png-1")},
			{Filename: "b.png", MediaType: "image/png", Source: stringFileSource("png-2")},
		},
	}
	if err := writeMultipart(context.Background(), form, values, files); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	_, params, err := mime.ParseMediaType(form.FormDataContentType())
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(&buf, params["boundary"])
	var fileFields []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if part.FileName() != "" {
			fileFields = append(fileFields, part.FormName())
		}
		_ = part.Close()
	}
	if len(fileFields) != 2 || fileFields[0] != "image[]" || fileFields[1] != "image[]" {
		t.Fatalf("expected repeated image uploads to use image[], got %v", fileFields)
	}
}
