package gateway_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/storage"
	"github.com/deyna256/clan/internal/usage"
)

func TestUploadFailuresReturnClientErrors(t *testing.T) {
	for _, tt := range []struct {
		name, framing, body string
		halfClose           bool
		want                int
	}{
		{name: "body deadline", framing: "Content-Length: 10", body: "{", want: 408},
		{name: "truncated body", framing: "Content-Length: 10", body: "{", halfClose: true, want: 400},
		{name: "invalid chunk size", framing: "Transfer-Encoding: chunked", body: "XYZ\r\n", want: 400},
		{name: "invalid chunk ending", framing: "Transfer-Encoding: chunked", body: "1\r\n{XX", want: 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "data: %s\n\n", completed) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				f.handler.ServeHTTP(shortReadDeadlineWriter{w}, r)
			}))
			defer server.Close()
			conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}

			_, err = fmt.Fprintf(conn, "POST /v1/responses HTTP/1.1\r\nHost: gateway\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\n%s\r\n\r\n%s", f.key, tt.framing, tt.body)
			if err != nil {
				t.Fatal(err)
			}
			if tt.halfClose {
				if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
					t.Fatal(err)
				}
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body := readBody(t, resp)

			if resp.StatusCode != tt.want || !strings.Contains(body, `"error":`) {
				t.Fatalf("upload failure response = %d, %s; want %d", resp.StatusCode, body, tt.want)
			}
		})
	}
}

type shortReadDeadlineWriter struct{ http.ResponseWriter }

func (w shortReadDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w shortReadDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	if deadline.After(time.Now()) {
		deadline = time.Now().Add(20 * time.Millisecond)
	}
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(deadline)
}

func TestRevocationInterruptsWriteAndWaitsForDeliveryCleanup(t *testing.T) {
	for _, tt := range []struct {
		name, body     string
		upstream, want int
	}{
		{name: "JSON completion", body: input, upstream: 200, want: 200},
		{name: "SSE completion", body: `{"model":"model","input":"hello","stream":true}`, upstream: 200, want: 200},
		{name: "JSON open failure", body: input, upstream: 502, want: 502},
		{name: "SSE open failure", body: `{"model":"model","input":"hello","stream":true}`, upstream: 502, want: 502},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.upstream)
				if tt.upstream == 200 {
					fmt.Fprintf(w, "data: %s\n\n", completed)
				}
			})
			w := &blockedWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), interrupted: make(chan struct{}), release: make(chan struct{})}
			unblock := sync.OnceFunc(func() { close(w.release) })
			defer unblock()
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(tt.body)).WithContext(t.Context())
			r.Header.Set("Authorization", "Bearer "+f.key)
			r.Header.Set("Content-Type", "application/json")
			done := make(chan struct{})
			go func() { f.handler.ServeHTTP(w, r); close(done) }()
			select {
			case <-w.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("response write did not start")
			}

			resp := f.request(t, "POST", "/v1/responses", input)
			body := readBody(t, resp)
			if resp.StatusCode != 429 || !strings.Contains(body, `"code":"concurrency_limit"`) {
				t.Fatalf("request while delivery is blocked = %d, %s; want concurrency limit", resp.StatusCode, body)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			revoked := make(chan error, 1)
			go func() { revoked <- f.executor.RevokeKey(ctx, "key") }()
			select {
			case <-w.interrupted:
			case <-time.After(2 * time.Second):
				t.Fatal("revocation did not interrupt the blocked downstream write")
			}
			// A canceled management wait reports unconfirmed cleanup while the writer
			// remains blocked; it must not report successful completion.
			cancel()
			if err := <-revoked; !errors.Is(err, context.Canceled) {
				t.Fatalf("revocation before delivery cleanup = %v, want cancellation", err)
			}
			select {
			case <-done:
				t.Fatal("handler returned while its response write was still blocked")
			default:
			}
			unblock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("handler did not finish after delivery cleanup")
			}
			if err := f.executor.RevokeKey(t.Context(), "key"); err != nil {
				t.Fatal(err)
			}
			logs := f.logs.String()
			if !strings.Contains(logs, `"result":"canceled"`) || strings.Contains(logs, `"result":"completed"`) {
				t.Fatalf("canceled delivery outcome = %s", logs)
			}
			row, err := f.store.GetRequest(t.Context(), w.Header().Get("X-Request-ID"))
			if err != nil || row.Result != "canceled" || !row.ResponseStarted || row.KeyID != "key" || row.AccountID != "one" {
				t.Fatalf("revocation record = %+v, error = %v", row, err)
			}
			var wantUsage usage.Snapshot
			if tt.upstream == 200 {
				wantUsage = usage.Snapshot{
					Input: usage.Counter{Known: true, Tokens: 7}, Output: usage.Counter{Known: true, Tokens: 3},
					Total: usage.Counter{Known: true, Tokens: 10},
				}
			}
			if row.Usage != wantUsage {
				t.Fatalf("revocation usage = %+v, want %+v", row.Usage, wantUsage)
			}

			if w.Code != tt.want {
				t.Fatalf("response status = %d, want %d", w.Code, tt.want)
			}
			if tt.want != 200 && w.Header().Get("X-Should-Retry") != "false" {
				t.Fatal("open failure response lacks retry suppression")
			}
		})
	}
}

func TestClientDisconnectPersistsCanceledStream(t *testing.T) {
	release := make(chan struct{})
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"hello\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.handler.ServeHTTP(w, r)
		close(done)
	}))
	defer server.Close()
	defer close(release)
	request, err := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/v1/responses", strings.NewReader(`{"model":"model","input":"hello","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+f.key)
	request.Header.Set("Content-Type", "application/json")
	server.Client().Timeout = 3 * time.Second

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if event := readEvent(t, bufio.NewReader(response.Body)); event["delta"] != "hello" {
		t.Fatalf("first stream event = %v", event)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnected request did not finish cleanup")
	}

	row, err := f.store.GetRequest(t.Context(), response.Header.Get("X-Request-ID"))
	if err != nil || row.Result != "canceled" || !row.ResponseStarted || row.KeyID != "key" || row.AccountID != "one" {
		t.Fatalf("disconnect record = %+v, error = %v", row, err)
	}
	if row.Usage != (usage.Snapshot{}) {
		t.Fatalf("disconnect invented usage = %+v", row.Usage)
	}
}

func TestCancellationInterruptsEarlyErrorDelivery(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "data: %s\n\n", completed) })
	if err := f.executor.SetConcurrency(t.Context(), "key", 0); err != nil {
		t.Fatal(err)
	}
	w := &blockedWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), interrupted: make(chan struct{}), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(w.release) })
	defer unblock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(input)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+f.key)
	r.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { f.handler.ServeHTTP(w, r); close(done) }()
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("early error write did not start")
	}
	if rows, err := f.store.ListRequests(t.Context(), storage.RequestFilter{}, nil, 100); err != nil || len(rows) != 0 {
		t.Fatalf("recorded before delivery cleanup: %+v %v", rows, err)
	}
	if strings.Contains(f.logs.String(), `"msg":"request finished"`) {
		t.Fatal("final log before delivery cleanup")
	}
	// Shutdown can close execution while this unadmitted HTTP handler is draining.
	f.executor.Close()

	cancel()
	select {
	case <-w.interrupted:
	case <-time.After(2 * time.Second):
		t.Fatal("request cancellation did not interrupt early error delivery")
	}
	unblock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("early error handler did not finish cleanup")
	}
	if w.Code != 429 {
		t.Fatalf("early error status = %d, want concurrency limit", w.Code)
	}
	row, err := f.store.GetRequest(t.Context(), w.Header().Get("X-Request-ID"))
	if err != nil || row.Result != "canceled" || !row.ResponseStarted {
		t.Fatalf("final row=%+v err=%v", row, err)
	}
}

type blockedWriter struct {
	*httptest.ResponseRecorder
	entered, interrupted, release chan struct{}
	interrupt                     sync.Once
}

func (w *blockedWriter) Write([]byte) (int, error) {
	close(w.entered)
	<-w.interrupted
	<-w.release
	return 0, io.ErrClosedPipe
}

func (*blockedWriter) SetReadDeadline(time.Time) error { return nil }

func (w *blockedWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		w.interrupt.Do(func() { close(w.interrupted) })
	}
	return nil
}

func (*blockedWriter) FlushError() error { return nil }
