package codex_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/retry"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func httpResponse(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestClientPreservesLegacyTransportDial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"models":[]}`)
	}))
	defer server.Close()

	var dialCalls atomic.Int32
	transport := &http.Transport{
		//nolint:staticcheck // Exercise deprecated Dial to protect existing callers.
		Dial: func(network, addr string) (net.Conn, error) {
			dialCalls.Add(1)
			return net.DialTimeout(network, addr, 3*time.Second)
		},
	}

	client := testClient(t, &http.Client{Transport: transport}, server.URL)
	defer client.CloseIdleConnections()

	models, err := client.FetchModels(t.Context(), testAccount(t, "one"))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("models = %+v, want an empty catalog", models)
	}
	if got := dialCalls.Load(); got != 1 {
		t.Fatalf("legacy Dial calls = %d, want 1", got)
	}
	if transport.DialContext != nil {
		t.Error("NewClient mutated the supplied transport")
	}
}

func TestClientClosesItsClonedConnectionPool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		defer serverConn.Close()
		httpClient := &http.Client{Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil },
		}}
		client := testClient(t, httpClient, "http://example.test")
		defer client.CloseIdleConnections()
		closed := make(chan error, 1)
		go func() {
			defer serverConn.Close()
			if _, err := http.ReadRequest(bufio.NewReader(serverConn)); err != nil {
				closed <- err
				return
			}
			if _, err := io.WriteString(serverConn, "HTTP/1.1 200 OK\r\nContent-Length: 13\r\n\r\n{\"models\":[]}"); err != nil {
				closed <- err
				return
			}
			_, err := serverConn.Read(make([]byte, 1))
			closed <- err
		}()

		if _, err := client.FetchModels(t.Context(), testAccount(t, "one")); err != nil {
			t.Fatal(err)
		}
		httpClient.CloseIdleConnections()
		synctest.Wait()
		select {
		case err := <-closed:
			t.Fatalf("supplied client closed the private pool: %v", err)
		default:
		}
		client.CloseIdleConnections()

		if err := <-closed; !errors.Is(err, io.EOF) {
			t.Fatalf("peer read after closing pool = %v, want EOF", err)
		}
	})
}

func TestTLSHandshakeTimeoutPreservesDeadline(t *testing.T) {
	for _, tt := range []struct {
		name             string
		configured, want time.Duration
	}{
		{name: "default", want: 10 * time.Second},
		{name: "shorter explicit limit", configured: time.Second, want: time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clientConn, serverConn := net.Pipe()
				defer clientConn.Close()
				defer serverConn.Close()
				client := testClient(t, &http.Client{Transport: &http.Transport{
					DialContext:         func(context.Context, string, string) (net.Conn, error) { return clientConn, nil },
					TLSHandshakeTimeout: tt.configured,
				}}, "https://secret.example")
				defer client.CloseIdleConnections()
				closed := make(chan error, 1)
				go func() { _, err := io.Copy(io.Discard, serverConn); closed <- err }()
				started := time.Now()

				_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

				var failure *codex.Failure
				if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &failure) || failure.Category != codex.TransportFailure || failure.SafeToRetry {
					t.Fatalf("TLS timeout = %v, want transport deadline without safe retry", err)
				}
				if time.Since(started) != tt.want || strings.Contains(err.Error(), "secret") {
					t.Errorf("error = %v after %v, want safe deadline after %v", err, time.Since(started), tt.want)
				}
				if err := <-closed; err != nil {
					t.Errorf("peer read after timeout = %v, want EOF", err)
				}
			})
		})
	}
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

			_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

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

	_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

	var failure *codex.Failure
	if !errors.As(err, &failure) || failure.HTTPStatus != 307 || reached.Load() {
		t.Fatalf("redirect followed or wrong error: %v", err)
	}
}

func TestHTTPQuotaResetTiming(t *testing.T) {
	for _, tt := range []struct {
		name, header, body string
		want               time.Duration
	}{
		{name: "absolute reset", body: `{"error":{"type":"usage_limit_reached","resets_at":946685100,"resets_in_seconds":2}}`, want: 5 * time.Minute},
		{name: "relative reset", body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":90}}`, want: 90 * time.Second},
		{name: "expired absolute reset", body: `{"error":{"type":"usage_limit_reached","resets_at":1,"resets_in_seconds":90}}`, want: 90 * time.Second},
		{name: "malformed absolute reset", body: `{"error":{"type":"usage_limit_reached","resets_at":"secret","resets_in_seconds":90}}`, want: 90 * time.Second},
		{name: "header wins", header: "20", body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":90}}`, want: 20 * time.Second},
		{name: "invalid header", header: "secret", body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":90}}`, want: 90 * time.Second},
		{name: "non quota error", body: `{"error":{"type":"rate_limit_error","resets_in_seconds":90}}`},
		{name: "negative reset", body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":-1}}`},
		{name: "zero reset", body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":0}}`},
		{name: "fraction", body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":1.5}}`},
		{name: "overflow", body: `{"error":{"type":"usage_limit_reached","resets_at":9223372036854775808,"resets_in_seconds":9223372036854775808}}`},
		{name: "malformed", body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":90`},
		{name: "oversized", body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":90},"message":"` + strings.Repeat("x", 64<<10) + `"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := &watchedBody{Reader: strings.NewReader(tt.body)}
				client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {tt.header}}, Body: body}, nil
				})}, "https://example.test")
				received := time.Now()

				_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

				var failure *codex.Failure
				if !errors.As(err, &failure) || failure.Category != codex.ProviderLimit || failure.HTTPStatus != 429 || !failure.SafeToRetry {
					t.Fatalf("failure = %+v, want safe provider limit with HTTP429", err)
				}
				if tt.want == 0 {
					if failure.RetryAfter.Kind != retry.NoCooldown {
						t.Errorf("cooldown = %+v, want unknown", failure.RetryAfter)
					}
				} else if failure.RetryAfter.Kind != retry.RetryAt || !failure.RetryAfter.Until.Equal(received.Add(tt.want)) {
					t.Errorf("cooldown = %+v, want %v after receipt", failure.RetryAfter, tt.want)
				}
				if body.closed.Load() != 1 || strings.Contains(err.Error(), "secret") {
					t.Errorf("close count = %d, error = %v", body.closed.Load(), err)
				}
			})
		})
	}
}

func TestHTTPQuotaReadFailureRetainsClassification(t *testing.T) {
	body := &watchedBody{Reader: failedReader{}}
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: body}, nil
	})}, "https://example.test")

	_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

	var failure *codex.Failure
	if !errors.As(err, &failure) || failure.Category != codex.ProviderLimit || failure.HTTPStatus != 429 || !failure.SafeToRetry || failure.RetryAfter.Kind != retry.NoCooldown {
		t.Fatalf("failure = %+v, want safe HTTP429 without timing", err)
	}
	if body.closed.Load() != 1 || strings.Contains(err.Error(), "secret") {
		t.Errorf("close count = %d, error = %v", body.closed.Load(), err)
	}
}

func TestHTTPQuotaReadTimeoutRetainsClassification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body := &blockingBody{ctx: t.Context(), entered: make(chan struct{}), closed: make(chan struct{})}
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 429, Body: body}, nil
		})}, "https://example.test")
		started := time.Now()

		_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

		var failure *codex.Failure
		if !errors.As(err, &failure) || failure.Category != codex.ProviderLimit || failure.HTTPStatus != 429 || !failure.SafeToRetry || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("failure = %+v, want safe HTTP429 with read deadline", err)
		}
		if body.closeCalls.Load() != 1 || time.Since(started) != 5*time.Minute {
			t.Errorf("body closed %d times after %v, want once after5m", body.closeCalls.Load(), time.Since(started))
		}
	})
}

func TestHTTPResponseHeaderTimeout(t *testing.T) {
	for _, tt := range []struct {
		name             string
		configured, want time.Duration
	}{
		{name: "default", want: 5 * time.Minute},
		{name: "shorter supplied limit", configured: time.Second, want: time.Second},
		{name: "longer supplied limit", configured: 10 * time.Minute, want: 5 * time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.Copy(io.Discard, r.Body)
					<-r.Context().Done()
				}))
				httpClient := server.Client()
				transport := httpClient.Transport.(*http.Transport)
				transport.ResponseHeaderTimeout = tt.configured
				client := testClient(t, httpClient, server.URL)
				started := time.Now()

				_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

				if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != tt.want {
					t.Fatalf("error = %v after %v, want deadline after %v", err, time.Since(started), tt.want)
				}
				if transport.ResponseHeaderTimeout != tt.configured {
					t.Error("supplied transport was mutated")
				}
			})
		})
	}
}

func TestHTTPRequestWriteTimeout(t *testing.T) {
	for _, tt := range []struct {
		name           string
		deadline, want time.Duration
	}{
		{name: "opening timeout", want: 5 * time.Minute},
		{name: "earlier caller deadline", deadline: time.Second, want: time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				clientConn, serverConn := net.Pipe()
				defer serverConn.Close()
				client := testClient(t, &http.Client{Transport: &http.Transport{
					DialContext: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil },
				}}, "http://provider.test")
				defer client.CloseIdleConnections()
				ctx := t.Context()
				if tt.deadline != 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tt.deadline)
					defer cancel()
				}
				input, a := request(t, `{"model":"m","input":"hello"}`), testAccount(t, "one")
				started := time.Now()

				_, err := client.Stream(ctx, a, input)

				var failure *codex.Failure
				if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &failure) || failure.SafeToRetry {
					t.Fatalf("write error = %v, want deadline without safe retry", err)
				}
				if time.Since(started) != tt.want {
					t.Fatalf("request stalled for %v, want %v", time.Since(started), tt.want)
				}
			})
		})
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
	retryAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
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
				return &http.Response{StatusCode: tt.status, Header: http.Header{
					"Content-Type": {tt.contentType}, "Retry-After": {retryAt.Format(http.TimeFormat)},
				}, Body: body}, nil
			})}, "https://example.test")

			_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

			var failure *codex.Failure
			if !errors.As(err, &failure) || failure.Category != tt.category {
				t.Fatalf("error = %v, want %s", err, tt.category)
			}
			if failure.RetryAfter.Kind != retry.RetryAt || !failure.RetryAfter.Until.Equal(retryAt) {
				t.Errorf("retry timing = %+v, want %v", failure.RetryAfter, retryAt)
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
	_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
	if strings.Contains(err.Error(), "secret") || errors.Unwrap(err) != nil {
		t.Errorf("transport cause leaked: %v", err)
	}
}

func TestMissingContentTypeStillRequiresCompleteSSE(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		valid      bool
	}{
		{name: "complete SSE", body: "event: response.completed\n" + completeSSE, valid: true},
		{name: "truncated SSE", body: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"},
		{name: "HTML", body: "<!doctype html><title>Unavailable</title>"},
		{name: "JSON", body: `{"id":"r","status":"completed","output":[]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &watchedBody{Reader: strings.NewReader(tt.body)}
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
			})}, "https://example.test")

			result, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

			if tt.valid {
				if err != nil {
					t.Fatal(err)
				}
				assertJSON(t, result.Response, `{"id":"resp_1","status":"completed","output":[]}`)
			} else {
				var failure *codex.Failure
				if !errors.As(err, &failure) || failure.Category != codex.InvalidResponse || failure.HTTPStatus != 200 || failure.SafeToRetry {
					t.Fatalf("invalid stream error = %v, want invalid response without safe replay", err)
				}
				if len(result.Response) != 0 {
					t.Error("invalid stream produced a terminal response")
				}
			}
			if body.closed.Load() != 1 {
				t.Errorf("body closed %d times, want 1", body.closed.Load())
			}
		})
	}
}

func TestStreamCleanupFailureRetainsTerminalResult(t *testing.T) {
	retryAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	body := &watchedBody{Reader: strings.NewReader(completeSSE), closeErr: errors.New("secret close failure")}
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{
			"Content-Type": {"text/event-stream"}, "Retry-After": {retryAt.Format(http.TimeFormat)},
		}, Body: body}, nil
	})}, "https://example.test")

	result, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

	var failure *codex.Failure
	if !errors.As(err, &failure) || failure.Category != codex.TransportFailure {
		t.Fatalf("cleanup error = %v", err)
	}
	if failure.RetryAfter.Kind != retry.RetryAt || !failure.RetryAfter.Until.Equal(retryAt) {
		t.Errorf("retry timing = %+v, want %v", failure.RetryAfter, retryAt)
	}
	if strings.Contains(err.Error(), "secret") || len(result.Response) == 0 || body.closed.Load() != 1 {
		t.Errorf("lost result or unsafe cleanup: %v", err)
	}
}

func TestCloseBeforeReadRetainsRetryTiming(t *testing.T) {
	retryAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := httpResponse(200, "text/event-stream", completeSSE)
		response.Header.Set("Retry-After", retryAt.Format(http.TimeFormat))
		return response, nil
	})}, "https://example.test")
	stream, err := client.Stream(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Next()

	var failure *codex.Failure
	if !errors.Is(err, context.Canceled) || !errors.As(err, &failure) || failure.HTTPStatus != 200 || failure.SafeToRetry {
		t.Fatalf("closed stream failure = %v, want cancellation with status 200 and no safe replay", err)
	}
	if failure.RetryAfter.Kind != retry.RetryAt || !failure.RetryAfter.Until.Equal(retryAt) {
		t.Errorf("retry timing = %+v, want %v", failure.RetryAfter, retryAt)
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

func TestHTTPClientTimeoutPreservesDeadline(t *testing.T) {
	for _, tt := range []struct {
		name    string
		headers bool
		status  int
	}{
		{name: "before headers"},
		{name: "during stream", headers: true, status: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := testClient(t, &http.Client{
					Timeout: time.Second,
					Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						if !tt.headers {
							<-r.Context().Done()
							return nil, r.Context().Err()
						}
						response := httpResponse(200, "text/event-stream", "")
						response.Body = &blockingBody{ctx: r.Context(), entered: make(chan struct{}), closed: make(chan struct{})}
						return response, nil
					}),
				}, "https://secret.example")
				a := testAccount(t, "one")
				r := request(t, `{"model":"m","input":"hi"}`)

				_, err := consumeClientStream(client, t.Context(), a, r)

				if !errors.Is(err, context.DeadlineExceeded) || t.Context().Err() != nil {
					t.Fatalf("client timeout = %v, caller context = %v", err, t.Context().Err())
				}
				var failure *codex.Failure
				if !errors.As(err, &failure) || failure.Category != codex.TransportFailure || failure.HTTPStatus != tt.status || failure.SafeToRetry {
					t.Fatalf("timeout failure = %+v, want unsafe transport failure with status %d", failure, tt.status)
				}
				var urlError *url.Error
				if errors.As(err, &urlError) || strings.Contains(err.Error(), "secret") {
					t.Errorf("transport details leaked: %v", err)
				}
			})
		})
	}
}

func TestFetchModelsPreservesHTTPClientTimeoutDuringRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		retryAt := time.Now().Add(10 * time.Second)
		client := testClient(t, &http.Client{
			Timeout: time.Second,
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				response := httpResponse(200, "application/json", "")
				response.Header.Set("Retry-After", "10")
				response.Body = &blockingBody{ctx: r.Context(), entered: make(chan struct{}), closed: make(chan struct{})}
				return response, nil
			}),
		}, "https://example.test")
		a := testAccount(t, "one")

		_, err := client.FetchModels(t.Context(), a)

		if !errors.Is(err, context.DeadlineExceeded) || t.Context().Err() != nil {
			t.Fatalf("catalog timeout = %v, caller context = %v", err, t.Context().Err())
		}
		var failure *codex.Failure
		if !errors.As(err, &failure) || failure.RetryAfter.Kind != retry.RetryAt || !failure.RetryAfter.Until.Equal(retryAt) {
			t.Errorf("retry timing = %+v, want header receipt time plus ten seconds", failure)
		}
	})
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
