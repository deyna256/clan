package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/provider/openai/internal/transport"
)

func TestCreatePreservesRequestAndErrorResponse(t *testing.T) {
	captured := make(chan requestCapture, 1)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if requests.Add(1) == 1 {
			captured <- requestCapture{method: r.Method, path: r.URL.EscapedPath(), header: r.Header.Clone(), body: string(body), err: err}
		}
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"},"usage":{"input_tokens":4}}`)
	}))
	t.Cleanup(server.Close)
	client := newClient(t, server.URL+"/proxy%20root/v1/", server.Client())
	payload := []byte(`{"model":"test-model","input":"hello","stream":true}`)

	response, err := client.Create(t.Context(), "key-a", payload, true)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, response)
	got := <-captured

	if got.err != nil || got.method != "POST" || got.path != "/proxy%20root/v1/responses" {
		t.Fatalf("request = %#v", got)
	}
	if got.body != `{"model":"test-model","input":"hello","stream":true}` || string(payload) != got.body {
		t.Fatalf("request payload changed: sent %q, source %q", got.body, payload)
	}
	if got.header.Get("Authorization") != "Bearer key-a" || got.header.Get("Content-Type") != "application/json" || got.header.Get("Accept") != "text/event-stream" {
		t.Fatalf("unexpected request headers: %v", got.header)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d; want 1", requests.Load())
	}
	if response.StatusCode != 429 || response.Header.Get("Retry-After") != "3" || body != `{"error":{"code":"rate_limit_exceeded"},"usage":{"input_tokens":4}}` {
		t.Fatalf("error response changed: status %d, headers %v, body %q", response.StatusCode, response.Header, body)
	}
}

func TestCreateDoesNotFollowRedirectsOrShareCookies(t *testing.T) {
	var requests atomic.Int32
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/responses" {
			headers <- r.Header.Clone()
			http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(base, []*http.Cookie{{Name: "session", Value: "another-account"}})
	original := server.Client()
	original.Jar = jar
	client := newClient(t, server.URL, original)

	response, err := client.Create(t.Context(), "key-a", []byte(`{"input":"hello"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, response)
	got := <-headers

	if response.StatusCode != 307 || requests.Load() != 1 || got.Get("Cookie") != "" || got.Get("Accept") != "application/json" {
		t.Fatalf("status = %d, requests = %d, headers = %v", response.StatusCode, requests.Load(), got)
	}
	if original.Jar != jar || original.CheckRedirect != nil {
		t.Fatal("caller HTTP client was modified")
	}
}

func TestCancellationControlsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "start")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	client := newClient(t, server.URL, server.Client())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	response, err := client.Create(ctx, "key-a", []byte(`{"input":"hello"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if _, err := io.ReadFull(response.Body, make([]byte, 5)); err != nil {
		t.Fatalf("body was not usable after Create: %v", err)
	}
	finished := make(chan struct{})
	var readErr error
	t.Cleanup(func() {
		cancel()
		_ = response.Body.Close()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("response reader did not stop")
		}
	})
	go func() {
		_, readErr = io.ReadAll(response.Body)
		close(finished)
	}()

	cancel()

	select {
	case <-finished:
		if !errors.Is(readErr, context.Canceled) {
			t.Fatalf("body error = %v; want context cancellation", readErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt body reading")
	}
}

func TestConcurrentRequestsKeepCredentialsSeparate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	keys := []string{"key-a", "key-b", "key-c", "key-d"}
	captured := make(chan requestCapture, len(keys))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		select {
		case captured <- requestCapture{header: r.Header.Clone(), body: string(body), err: err}:
		case <-ctx.Done():
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := newClient(t, server.URL, server.Client())
	finished := make(chan error, len(keys))
	var workers sync.WaitGroup
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("request workers did not stop")
		}
	})

	for _, key := range keys {
		workers.Go(func() {
			payload, err := json.Marshal(key)
			if err != nil {
				finished <- err
				return
			}
			response, err := client.Create(ctx, key, payload, false)
			if err == nil {
				err = response.Body.Close()
			}
			finished <- err
		})
	}
	go func() { workers.Wait(); close(finished); close(done) }()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("concurrent requests did not finish")
	}
	for err := range finished {
		if err != nil {
			t.Errorf("Create: %v", err)
		}
	}
	if t.Failed() {
		return
	}
	seen := make(map[string]bool)
	for range keys {
		var got requestCapture
		select {
		case got = <-captured:
		case <-ctx.Done():
			t.Fatal("request succeeded without a captured request")
		}
		var key string
		if err := json.Unmarshal([]byte(got.body), &key); err != nil {
			t.Fatalf("request body: %v", err)
		}
		if got.err != nil || got.header.Get("Authorization") != "Bearer "+key || seen[key] {
			t.Fatalf("request credentials mixed or duplicated: %#v", got)
		}
		seen[key] = true
	}
	for _, key := range keys {
		if !seen[key] {
			t.Errorf("request for %s missing", key)
		}
	}
}

func TestNoReplayAndSafeTransportError(t *testing.T) {
	want := errors.New("private network detail")
	var requests int
	client := newClient(t, "https://example.invalid/private-path", &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests++
			if r.GetBody != nil {
				t.Error("request body permits automatic replay")
			}
			return nil, want
		}),
	})

	response, err := client.Create(t.Context(), "secret-key", []byte(`{"input":"secret prompt"}`), false)

	if response != nil || !errors.Is(err, want) || requests != 1 {
		t.Fatalf("Create = %v, %v; calls = %d", response, err, requests)
	}
	for _, secret := range []string{"private-path", "private network detail", "secret-key", "secret prompt"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error discloses %q", secret)
		}
	}
}

func TestNewRejectsInvalidRoots(t *testing.T) {
	for _, root := range []string{"", "/relative", "ftp://example.com", "https://", "https://user:secret@example.com", "https://example.com?secret=key", "https://example.com?", "https://example.com#part"} {
		t.Run(root, func(t *testing.T) {
			client, err := transport.New(root, http.DefaultClient)

			if client != nil || err == nil {
				t.Fatalf("New = %v, %v; want nil and error", client, err)
			}
		})
	}
}

func TestCreateRejectsInvalidInputBeforeDispatch(t *testing.T) {
	var requests int
	client := newClient(t, "https://example.invalid", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return nil, errors.New("unexpected dispatch")
		}),
	})
	tests := []struct {
		name, key, body string
	}{
		{name: "missing key", body: `{}`},
		{name: "header injection", key: "key\r\nX-Injected: yes", body: `{}`},
		{name: "space in key", key: "key value", body: `{}`},
		{name: "non-ASCII key", key: "ключ", body: `{}`},
		{name: "missing payload", key: "key-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := client.Create(t.Context(), tt.key, []byte(tt.body), false)

			if response != nil || err == nil || requests != 0 {
				t.Fatalf("Create = %v, %v; requests = %d", response, err, requests)
			}
		})
	}
}

func newClient(t *testing.T, root string, client *http.Client) *transport.Client {
	t.Helper()
	result, err := transport.New(root, client)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

type requestCapture struct {
	method, path, body string
	header             http.Header
	err                error
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
