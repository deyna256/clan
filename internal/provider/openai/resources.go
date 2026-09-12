package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/provider/openai/internal/transport"
)

func (c *Client) resourceJSON(ctx context.Context, attempt Attempt, method string, path []string, query url.Values, payload any) ([]byte, error) {
	var body io.Reader
	contentType := ""
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, &InputError{Field: "body", Problem: "cannot encode resource request"}
		}
		body = bytes.NewReader(encoded)
		contentType = "application/json"
	}
	request := transport.Request{Method: method, Path: path, Query: query, Body: body, ContentType: contentType, Accept: "application/json"}
	if len(path) > 0 && path[0] == "vector_stores" {
		request.Beta = "assistants=v2"
	}
	response, err := c.transport.Do(ctx, attempt.Credentials.Key, request)
	return c.resourceBody(attempt, response, err)
}

func (c *Client) resourceBody(attempt Attempt, response *http.Response, err error) ([]byte, error) {
	if err != nil {
		return nil, transportFailure(err)
	}
	defer response.Body.Close()
	body, err := readBounded(response.Body, c.config.MaxResponseBytes)
	if err != nil {
		return nil, transportFailure(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		diagnostics := c.diagnostics(attempt)
		defer diagnostics.finish()
		_, err = decodeHTTPFailure(response, body, diagnostics)
	}
	return body, err
}

func (c *Client) resourceContent(ctx context.Context, attempt Attempt, path []string) (io.ReadCloser, error) {
	response, err := c.transport.Do(ctx, attempt.Credentials.Key, transport.Request{Method: http.MethodGet, Path: path, Accept: "*/*"})
	if err != nil {
		return nil, transportFailure(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, err := c.resourceBody(attempt, response, nil)
		return nil, err
	}
	return response.Body, nil
}

func (c *Client) resourceUpload(ctx context.Context, attempt Attempt, path []string, fields map[string]string, filename string, content *uploadSource) ([]byte, error) {
	if content.ReadCloser == nil || filename == "" || !utf8.ValidString(filename) || strings.ContainsFunc(filename, func(r rune) bool { return r < 32 && r != '\t' || r == 127 }) {
		return nil, &InputError{Field: "file", Problem: "content and a valid filename are required"}
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return nil, err
		}
	}
	if _, err := writer.CreateFormFile("file", filename); err != nil {
		return nil, err
	}
	prefix := bytes.Clone(buffer.Bytes())
	buffer.Reset()
	if err := writer.Close(); err != nil {
		return nil, err
	}
	// Keep file bytes streaming without a producer goroutine or a temporary file.
	body := struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(prefix), content, bytes.NewReader(buffer.Bytes())), content}
	response, err := c.transport.Do(ctx, attempt.Credentials.Key, transport.Request{Method: http.MethodPost, Path: path, Body: body, ContentType: writer.FormDataContentType(), Accept: "application/json"})
	return c.resourceBody(attempt, response, err)
}

// uploadSource owns the reader from public call entry. The HTTP transport and
// operation cleanup may both close it, but the source is closed exactly once.
type uploadSource struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (source *uploadSource) Close() error {
	source.once.Do(func() {
		if source.ReadCloser != nil {
			source.err = source.ReadCloser.Close()
		}
	})
	return source.err
}

func (source *uploadSource) finish(err *error) {
	if closeErr := source.Close(); closeErr != nil && *err == nil {
		*err = errors.New("openai: upload source cleanup failed")
	}
}

func decodeResource[T any](body []byte, requestErr error) (T, error) {
	var value T
	if requestErr != nil {
		return value, requestErr
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(trimmed) || json.Unmarshal(trimmed, &value) != nil {
		return value, protocolError()
	}
	return value, nil
}

// resourceRequired checks presence separately from zero values in typed replies.
func resourceRequired(body []byte, fields ...string) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return protocolError()
	}
	for _, field := range fields {
		value := bytes.TrimSpace(object[field])
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			return protocolError()
		}
	}
	return nil
}
