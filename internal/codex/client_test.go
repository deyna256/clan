package codex_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/retry"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func httpResponse(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestHTTPProtocolAndOneAttempt(t *testing.T) {
	tests := []struct {
		status   int
		category codex.Category
		safe     bool
	}{
		{status: 400, category: codex.InvalidRequest, safe: true},
		{status: 401, category: codex.AccountProblem, safe: true},
		{status: 403, category: codex.AccountProblem, safe: true},
		{status: 429, category: codex.ProviderLimit, safe: true},
		{status: 503, category: codex.ProviderFailure},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			var calls atomic.Int32
			seen := make(chan *http.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				seen <- r.Clone(context.Background())
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tt.status)
				io.WriteString(w, `{"error":{"message":"secret-provider-body"}}`)
			}))
			defer server.Close()
			client := testClient(t, server.Client(), server.URL+"/backend-api/codex")
			start := time.Now()

			_, err := client.Generate(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

			var failure *codex.Failure
			if !errors.As(err, &failure) {
				t.Fatalf("got %v, want Failure", err)
			}
			if failure.Category != tt.category || failure.HTTPStatus != tt.status || failure.SafeToRetry != tt.safe {
				t.Errorf("incorrect failure facts: %+v", failure)
			}
			if failure.RetryAfter.Kind != retry.RetryAt || failure.RetryAfter.Until.Before(start.Add(2*time.Second)) {
				t.Errorf("missing retry timing: %+v", failure.RetryAfter)
			}
			if strings.Contains(err.Error(), "secret") || calls.Load() != 1 {
				t.Errorf("unsafe error or attempts: %v, %d", err, calls.Load())
			}
			r := <-seen
			if r.Method != "POST" || r.URL.Path != "/backend-api/codex/responses" {
				t.Errorf("request: %s %s", r.Method, r.URL)
			}
			for name, want := range map[string]string{"Authorization": "Bearer secret-one", "ChatGPT-Account-Id": "chatgpt-one", "Originator": "clan", "User-Agent": "clan/0.99.0", "Content-Type": "application/json", "Accept": "text/event-stream"} {
				if r.Header.Get(name) != want {
					t.Errorf("header %s mismatch", name)
				}
			}
		})
	}
}

func TestHTTPDoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	client := testClient(t, server.Client(), server.URL)

	_, err := client.Generate(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

	var failure *codex.Failure
	if !errors.As(err, &failure) || failure.HTTPStatus != 307 || reached.Load() {
		t.Fatalf("redirect followed or wrong error: %v", err)
	}
}

type watchedBody struct {
	io.Reader
	closed   atomic.Int32
	closeErr error
}

func (b *watchedBody) Close() error { b.closed.Add(1); return b.closeErr }

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("secret read error") }

func TestHTTPResourcesAndSafeTransportErrors(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		reader      io.Reader
		category    codex.Category
	}{
		{name: "error headers", status: 429, contentType: "application/json", reader: strings.NewReader("secret"), category: codex.ProviderLimit},
		{name: "wrong content type", status: 200, contentType: "application/json", reader: strings.NewReader("secret"), category: codex.InvalidResponse},
		{name: "read failure", status: 200, contentType: "text/event-stream", reader: failedReader{}, category: codex.TransportFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := &watchedBody{Reader: tt.reader}
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Header: http.Header{"Content-Type": []string{tt.contentType}}, Body: body}, nil
			})}, "https://example.test")

			_, err := client.Generate(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

			var failure *codex.Failure
			if !errors.As(err, &failure) || failure.Category != tt.category {
				t.Errorf("error = %v, want %s", err, tt.category)
			}
			if body.closed.Load() != 1 {
				t.Errorf("body closed %d times", body.closed.Load())
			}
			if strings.Contains(fmt.Sprintf("%+v", err), "secret") || errors.Unwrap(failure) != nil {
				t.Errorf("unsafe error: %v", err)
			}
		})
	}
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret transport URL") })}, "https://example.test")
	_, err := client.Generate(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
	if strings.Contains(err.Error(), "secret") || errors.Unwrap(err) != nil {
		t.Errorf("transport cause leaked: %v", err)
	}
}

func TestStreamCleanupFailureRetainsTerminalResult(t *testing.T) {
	body := &watchedBody{Reader: strings.NewReader(completeSSE), closeErr: errors.New("secret close failure")}
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
	})}, "https://example.test")

	result, err := client.Generate(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

	var failure *codex.Failure
	if !errors.As(err, &failure) || failure.Category != codex.TransportFailure {
		t.Fatalf("cleanup error = %v", err)
	}
	if strings.Contains(err.Error(), "secret") || len(result.Response) == 0 || body.closed.Load() != 1 {
		t.Errorf("lost result or unsafe cleanup: %v", err)
	}
}

func TestHTTPCancellation(t *testing.T) {
	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(serverDone)
	}))
	defer server.Close()
	client := testClient(t, server.Client(), server.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := client.Stream(ctx, testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Next(); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := stream.Next(); result <- err }()

	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next did not unblock")
	}
	if err := stream.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	select {
	case <-serverDone:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request did not cancel")
	}
}

type blockingBody struct {
	ctx        context.Context
	entered    chan struct{}
	closed     chan struct{}
	readOnce   sync.Once
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func (b *blockingBody) Read([]byte) (int, error) {
	b.readOnce.Do(func() { close(b.entered) })
	select {
	case <-b.closed:
		return 0, io.ErrClosedPipe
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}

func (b *blockingBody) Close() error {
	b.closeCalls.Add(1)
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestConcurrentCloseInterruptsBlockedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	body := &blockingBody{entered: make(chan struct{}), closed: make(chan struct{})}
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body.ctx = r.Context()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
	})}, "https://example.test")
	stream, err := client.Stream(ctx, testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() { _, err := stream.Next(); readDone <- err }()
	select {
	case <-body.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not enter Read")
	}

	closeDone := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() { stream.Close() })
		}
		wg.Wait()
		close(closeDone)
	}()

	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Close blocked")
	}
	select {
	case err := <-readDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("read cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next remained blocked after Close")
	}
	if body.closeCalls.Load() != 1 {
		t.Errorf("underlying Close called %d times", body.closeCalls.Load())
	}
}
