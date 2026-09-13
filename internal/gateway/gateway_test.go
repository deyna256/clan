package gateway_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/gateway"
)

func TestNewRequiresExecutor(t *testing.T) {
	if _, err := gateway.New(nil, slog.Default()); err == nil {
		t.Fatal("New accepted a nil executor")
	}
}

func TestResponsesDoNotWaitForUnusedBody(t *testing.T) {
	for _, tt := range []struct {
		name, method, path, mediaType string
		authenticated                 bool
		want                          int
	}{
		{name: "authentication", method: "POST", path: "/v1/responses", mediaType: "application/json", want: 401},
		{name: "media type", method: "POST", path: "/v1/responses", mediaType: "text/plain", authenticated: true, want: 415},
		{name: "query", method: "POST", path: "/v1/responses?unsupported=1", mediaType: "application/json", authenticated: true, want: 400},
		{name: "models success", method: "GET", path: "/v1/models", mediaType: "application/json", authenticated: true, want: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) })
			conn, err := net.DialTimeout("tcp", f.server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			key := "invalid"
			if tt.authenticated {
				key = f.key
			}

			// Keep-alive and a small absent body exercise net/http's automatic
			// body draining as well as the gateway's own authentication order.
			_, err = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: gateway\r\nAuthorization: Bearer %s\r\nContent-Type: %s\r\nContent-Length: 10\r\n\r\n", tt.method, tt.path, key, tt.mediaType)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("rejection waited for the absent body: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("rejection = %d, %v; want %d", resp.StatusCode, resp.Header, tt.want)
			}
			if tt.method == "POST" && resp.Header.Get("X-Should-Retry") != "false" {
				t.Fatal("POST rejection lacks retry hint")
			}
			if tt.want == 401 && resp.Header.Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("authentication rejection lacks Bearer challenge")
			}
			if calls.Load() != 0 {
				t.Fatalf("rejected generation calls = %d", calls.Load())
			}
		})
	}
}

func TestOrdinaryTerminalResponsesPreserveStatusAndUsage(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				errorField := ""
				if status == "failed" {
					errorField = `,"error":{"code":"server_error","message":"secret-upstream-detail"}`
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.%s\",\"response\":{\"id\":\"r\",\"status\":\"%s\",\"output\":[],\"usage\":{\"input_tokens\":7}%s}}\n\n", status, status, errorField)
			})

			resp := f.request(t, "POST", "/v1/responses", input)
			body := readBody(t, resp)

			if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("terminal response = %d, %s", resp.StatusCode, body)
			}
			var result struct {
				ID, Status string
				Output     []json.RawMessage
				Usage      struct {
					Input int `json:"input_tokens"`
				}
			}
			if err := json.Unmarshal([]byte(body), &result); err != nil {
				t.Fatal(err)
			}
			if result.ID != "r" || result.Status != status || result.Output == nil || result.Usage.Input != 7 {
				t.Fatalf("terminal result = %+v", result)
			}
			if strings.Contains(body, "secret-upstream-detail") {
				t.Fatal("provider error detail leaked")
			}
			if resp.Header.Get("X-Request-ID") == "" || resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("missing response metadata: %v", resp.Header)
			}
		})
	}
}

func TestClientErrorsUseOpenAIEnvelopeAndRetryHint(t *testing.T) {
	for _, tt := range []struct {
		name, method, path, body string
		upstream, want           int
		allow                    string
	}{
		{name: "invalid field", method: "POST", path: "/v1/responses", body: `{"model":"model","secret-input-field":true}`, want: 400},
		{name: "query", method: "POST", path: "/v1/responses?unexpected=1", body: input, want: 400},
		{name: "provider limit", method: "POST", path: "/v1/responses", body: input, upstream: 429, want: 429},
		{name: "account credentials", method: "POST", path: "/v1/responses", body: input, upstream: 401, want: 503},
		{name: "upstream failure", method: "POST", path: "/v1/responses", body: input, upstream: 502, want: 502},
		{name: "missing first event", method: "POST", path: "/v1/responses", body: `{"model":"model","input":"hello","stream":true}`, upstream: 200, want: 502},
		{name: "unknown resource", method: "GET", path: "/v1/unknown", want: 404},
		{name: "wrong method", method: "PUT", path: "/v1/responses", want: 405, allow: "POST"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.upstream != 0 {
					w.WriteHeader(tt.upstream)
				}
				if tt.upstream != 200 {
					io.WriteString(w, `{"error":{"message":"secret-upstream-detail"}}`)
				}
			})

			resp := f.request(t, tt.method, tt.path, tt.body)
			body := readBody(t, resp)

			var envelope struct{ Error map[string]any }
			if resp.StatusCode != tt.want || json.Unmarshal([]byte(body), &envelope) != nil || envelope.Error["code"] == nil {
				t.Fatalf("error response = %d, %s; want %d and OpenAI envelope", resp.StatusCode, body, tt.want)
			}
			if strings.Contains(body, "secret-") || strings.Contains(body, f.key) {
				t.Fatalf("private detail leaked in %s", body)
			}
			if tt.method == "POST" && resp.Header.Get("X-Should-Retry") != "false" {
				t.Fatal("POST error lacks retry suppression hint")
			}
			if resp.Header.Get("Allow") != tt.allow {
				t.Fatalf("Allow = %q, want %q", resp.Header.Get("Allow"), tt.allow)
			}
		})
	}
}

func TestRequestMediaTypeAndEncoding(t *testing.T) {
	for _, tt := range []struct {
		name, mediaType, encoding string
		want                      int
	}{
		{name: "JSON charset", mediaType: "application/json; charset=utf-8", want: 200},
		{name: "identity encoding", mediaType: "application/json", encoding: "identity", want: 200},
		{name: "missing media type", want: 415},
		{name: "plain text", mediaType: "text/plain", want: 415},
		{name: "compressed", mediaType: "application/json", encoding: "gzip", want: 415},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				fmt.Fprintf(w, "data: %s\n\n", completed)
			})
			r, err := http.NewRequestWithContext(t.Context(), "POST", f.server.URL+"/v1/responses", strings.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+f.key)
			r.Header.Set("Content-Type", tt.mediaType)
			if tt.encoding != "" {
				r.Header.Set("Content-Encoding", tt.encoding)
			}

			resp, err := f.server.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body := readBody(t, resp)

			if resp.StatusCode != tt.want {
				t.Fatalf("response = %d, %s; want %d", resp.StatusCode, body, tt.want)
			}
			if tt.want == 415 && (calls.Load() != 0 || resp.Header.Get("X-Should-Retry") != "false") {
				t.Fatalf("rejected media triggered generation or lacks retry hint: calls=%d, headers=%v", calls.Load(), resp.Header)
			}
		})
	}
}

func TestResponsesBodyLimitIsInclusive(t *testing.T) {
	for _, tt := range []struct {
		name  string
		extra int
		want  int
	}{
		{name: "exact boundary", want: 200},
		{name: "one byte over", extra: 1, want: 413},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "data: %s\n\n", completed) })
			body := input + strings.Repeat(" ", (16<<20)-len(input)+tt.extra)

			resp := f.request(t, "POST", "/v1/responses", body)
			response := readBody(t, resp)

			if resp.StatusCode != tt.want {
				t.Fatalf("body of %d bytes = %d, %s; want %d", len(body), resp.StatusCode, response, tt.want)
			}
		})
	}
}

func TestModelsDoNotUseGenerationSlots(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "data: %s\n\n", completed) })
	request, err := codex.ParseRequest([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := f.executor.Stream(t.Context(), f.key, "occupied", request)
	if stream != nil {
		defer stream.Close()
	}
	if err != nil {
		t.Fatal(err)
	}

	resp := f.request(t, "GET", "/v1/models", "")
	body := readBody(t, resp)
	if resp.StatusCode != 200 || body != `{"object":"list","data":[{"id":"model","object":"model","created":0,"owned_by":"openai"}]}` {
		t.Fatalf("models with occupied slot = %d, %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Should-Retry") != "" {
		t.Fatal("model listing changed normal client retries")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.executor.DisableAccount(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	resp = f.request(t, "GET", "/v1/models", "")
	if body := readBody(t, resp); resp.StatusCode != 200 || body != `{"object":"list","data":[]}` {
		t.Fatalf("empty models = %d, %s", resp.StatusCode, body)
	}
}

func TestUnavailableCatalogReturnsServiceUnavailable(t *testing.T) {
	var generations atomic.Int32
	f := newFixtureWithTransport(t, func(http.ResponseWriter, *http.Request) {
		generations.Add(1)
	}, func(transport http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/models" {
				return nil, errors.New("private-catalog-failure")
			}
			return transport.RoundTrip(r)
		})
	})

	resp := f.request(t, "GET", "/v1/models", "")
	body := readBody(t, resp)

	var envelope struct{ Error struct{ Code string } }
	if resp.StatusCode != 503 || json.Unmarshal([]byte(body), &envelope) != nil || envelope.Error.Code != "service_unavailable" {
		t.Fatalf("unavailable catalog = %d, %s; want 503 service_unavailable", resp.StatusCode, body)
	}
	if strings.Contains(body+f.logs.String(), "private-catalog-failure") {
		t.Fatal("catalog failure detail leaked")
	}
	if generations.Load() != 0 {
		t.Fatal("model discovery triggered generation")
	}
}

func TestFailedTerminalCannotHideUpstreamCleanupFailure(t *testing.T) {
	f := newFixtureWithTransport(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"r\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"server_error\"}}}\n\n")
	}, failResponseCleanup)

	resp := f.request(t, "POST", "/v1/responses", input)
	body := readBody(t, resp)

	var envelope struct{ Error map[string]any }
	if resp.StatusCode != 502 || json.Unmarshal([]byte(body), &envelope) != nil || envelope.Error["code"] == nil {
		t.Fatalf("failed terminal with failed cleanup = %d, %s; want upstream error", resp.StatusCode, body)
	}
	if strings.Contains(body+f.logs.String(), "private-close-detail") {
		t.Fatal("upstream cleanup error leaked")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type closeFailureBody struct{ io.ReadCloser }

func (b closeFailureBody) Close() error {
	return errors.Join(b.ReadCloser.Close(), errors.New("private-close-detail"))
}

func failResponseCleanup(transport http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := transport.RoundTrip(r)
		if err == nil && r.URL.Path == "/responses" {
			resp.Body = closeFailureBody{resp.Body}
		}
		return resp, err
	})
}
