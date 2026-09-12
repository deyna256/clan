package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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
		return body, transportFailure(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		diagnostics := c.diagnostics(attempt)
		defer diagnostics.finish()
		_, err = decodeHTTPFailure(response, body, diagnostics)
	}
	return body, err
}

func decodeResource[T any](body []byte, requestErr error) (T, error) {
	var value T
	if requestErr != nil {
		return value, requestErr
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &value) != nil || !utf8.Valid(trimmed) {
		return value, protocolError()
	}
	return value, nil
}

type resourcePage struct {
	Data    []json.RawMessage `json:"data"`
	FirstID *string           `json:"first_id"`
	LastID  *string           `json:"last_id"`
	HasMore *bool             `json:"has_more"`
	Object  string            `json:"object"`
}

func decodeResourcePage(body []byte, requestErr error) (resourcePage, error) {
	page, err := decodeResource[resourcePage](body, requestErr)
	if err != nil {
		return resourcePage{}, err
	}
	if page.Data == nil || page.FirstID == nil || page.LastID == nil || page.HasMore == nil || page.Object != "list" {
		return resourcePage{}, protocolError()
	}
	return page, nil
}
