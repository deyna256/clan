// Package transport sends OpenAI HTTP requests without retry loops or redirects.
package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Client must be constructed with New. It may serve concurrent requests.
type Client struct {
	root *url.URL
	http http.Client
}

var ErrInvalidKey = errors.New("openai: API key must contain only visible ASCII characters")
var ErrInvalidPath = errors.New("openai: invalid resource path")

// New uses baseURL as the API root and copies the HTTP client configuration.
// The transport is shared. Cookies and redirects are disabled; timeouts remain
// the caller's choice. The standard transport may recover stale connections for
// idempotent reads; generation and upload bodies cannot be replayed.
func New(baseURL string, client *http.Client) (*Client, error) {
	if client == nil {
		return nil, errors.New("openai: HTTP client is required")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Hostname() == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, errors.New("openai: API root must be an absolute HTTP or HTTPS URL")
	}
	if base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || strings.Contains(baseURL, "#") {
		return nil, errors.New("openai: API root must not contain credentials, query or fragment")
	}
	c := *client
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{root: base, http: c}, nil
}

// Create sends encoded request JSON; keep payload immutable during the request.
// The caller owns every returned response body, including non-2xx responses,
// and must bound reads and close it. Context controls body reads after return.
func (c *Client) Create(ctx context.Context, apiKey string, payload []byte, streaming bool) (*http.Response, error) {
	if len(payload) == 0 {
		return nil, errors.New("openai: request body is required")
	}
	accept := "application/json"
	if streaming {
		accept = "text/event-stream"
	}
	return c.Do(ctx, apiKey, Request{Method: http.MethodPost, Path: []string{"responses"}, Body: bytes.NewReader(payload), ContentType: "application/json", Accept: accept})
}

type Request struct {
	Method, ContentType, Accept, Beta string
	Path                              []string
	Query                             url.Values
	Body                              io.Reader
}

// Do sends a resource request. Path entries are individual unescaped segments.
// The caller owns the returned body and must close it on every status code.
func (c *Client) Do(ctx context.Context, apiKey string, request Request) (*http.Response, error) {
	if !validKey(apiKey) {
		return nil, ErrInvalidKey
	}
	escaped := make([]string, len(request.Path))
	for i, segment := range request.Path {
		if strings.TrimSpace(segment) == "" || segment == "." || segment == ".." || !utf8.ValidString(segment) || strings.ContainsAny(segment, "/\\") || strings.ContainsFunc(segment, unicode.IsControl) {
			return nil, ErrInvalidPath
		}
		escaped[i] = url.PathEscape(segment)
	}
	u := c.root.JoinPath(escaped...)
	u.RawQuery = request.Query.Encode()
	req, err := http.NewRequestWithContext(ctx, request.Method, u.String(), request.Body)
	if err != nil {
		return nil, errors.New("openai: cannot construct request")
	}
	// net/http can replay a rewindable body after some connection failures.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if request.ContentType != "" {
		req.Header.Set("Content-Type", request.ContentType)
	}
	req.Header.Set("Accept", request.Accept)
	if request.Beta != "" {
		req.Header.Set("OpenAI-Beta", request.Beta)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, &requestError{cause: err}
	}
	return response, nil
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i := range len(key) {
		if key[i] < '!' || key[i] > '~' {
			return false
		}
	}
	return true
}

// Keep transport errors inspectable inside the adapter without printing URLs.
type requestError struct{ cause error }

func (*requestError) Error() string   { return "openai: upstream request failed" }
func (e *requestError) Unwrap() error { return e.cause }
