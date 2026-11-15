package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"reflect"
	"sort"
)

// FileSource opens a fresh stream for each authentication attempt.
type FileSource interface {
	Open(context.Context) (io.ReadCloser, error)
}

type File struct {
	Filename  string
	MediaType string
	Source    FileSource
}

// FileResponse transfers ownership of Body to the caller, which must close it.
type FileResponse struct {
	Filename  string
	MediaType string
	Size      int64
	Body      io.ReadCloser
}

func multipartBody(ctx context.Context, request *Request) (io.ReadCloser, string, error) {
	fields := request.Operation.FileFields()
	values, ok := request.Body.(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("multipart body must be an object")
	}
	for name, files := range request.Files {
		repeated, exists := fields[name]
		if !exists || len(files) == 0 || (!repeated && len(files) != 1) {
			return nil, "", fmt.Errorf("invalid multipart files for %s", name)
		}
		if _, exists := values[name]; !exists {
			return nil, "", fmt.Errorf("multipart file %s is absent from body", name)
		}
		for _, file := range files {
			if file.Source == nil || file.Filename == "" {
				return nil, "", fmt.Errorf("multipart file %s requires a source and filename", name)
			}
		}
	}
	for name := range fields {
		if value, present := values[name]; present && value != nil && len(request.Files[name]) == 0 {
			return nil, "", fmt.Errorf("multipart field %s requires a file source", name)
		}
	}
	uploadContext, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		defer cancel()
		err := writeMultipart(uploadContext, form, values, request.Files)
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	return &uploadBody{PipeReader: reader, cancel: cancel}, form.FormDataContentType(), nil
}

type uploadBody struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (b *uploadBody) Close() error {
	b.cancel()
	return b.PipeReader.Close()
}

func writeMultipart(ctx context.Context, form *multipart.Writer, values map[string]any, files map[string][]File) error {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if uploads, ok := files[name]; ok {
			for _, file := range uploads {
				if err := writeFile(ctx, form, name, file); err != nil {
					return err
				}
			}
			continue
		}
		value := values[name]
		if value == nil {
			continue
		}
		items := []any{value}
		if values := reflect.ValueOf(value); values.Kind() == reflect.Slice || values.Kind() == reflect.Array {
			items = make([]any, values.Len())
			for index := range items {
				items[index] = values.Index(index).Interface()
			}
		}
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				encoded, err := json.Marshal(item)
				if err != nil {
					return err
				}
				text = string(encoded)
			}
			if err := form.WriteField(name, text); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeFile(ctx context.Context, form *multipart.Writer, name string, file File) error {
	source, err := file.Source.Open(ctx)
	if err != nil {
		return err
	}
	defer source.Close()
	mediaType := file.MediaType
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(mediaType); err != nil {
		return fmt.Errorf("invalid file media type")
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": name, "filename": file.Filename}))
	header.Set("Content-Type", mediaType)
	part, err := form.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, source)
	return err
}
