package gateway_test

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/credentialcipher"
	"github.com/deyna256/clan/internal/execution"
	"github.com/deyna256/clan/internal/gateway"
	"github.com/deyna256/clan/internal/storage"
)

const completed = `{"type":"response.completed","sequence_number":2,"response":{"id":"r","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}}`
const input = `{"model":"model","input":"hello"}`

type fixture struct {
	handler  http.Handler
	server   *httptest.Server
	executor *execution.Executor
	key      string
	logs     *lockedBuffer
}

func newFixture(t *testing.T, generate http.HandlerFunc) fixture {
	t.Helper()
	return newFixtureWithTransport(t, generate, nil)
}

func newFixtureWithTransport(t *testing.T, generate http.HandlerFunc, wrap func(http.RoundTripper) http.RoundTripper) fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("temporary SQLite integration")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"models":[{"slug":"model","visibility":"list"},{"slug":"hidden","visibility":"hide"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		generate(w, r)
	}))
	t.Cleanup(upstream.Close)
	cipher, err := credentialcipher.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "gateway.db"), cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	a, err := account.New(account.Identity{ID: "one", Name: "Main"}, account.OAuthCredentials{
		ChatGPTAccountID: "provider-account", AccessToken: "secret-provider-token", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAccount(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	raw, hash := accesskey.Generate()
	key, err := accesskey.New(accesskey.Identity{ID: "key", Name: "Client"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAccessKey(t.Context(), key, hash, 1); err != nil {
		t.Fatal(err)
	}
	oauthClient, err := codexoauth.NewClient(upstream.Client(), upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := codexoauth.NewManager(t.Context(), oauthClient, store, listener)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	httpClient := upstream.Client()
	if wrap != nil {
		httpClient.Transport = wrap(httpClient.Transport)
	}
	client, err := codex.NewClient(httpClient, upstream.URL, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	f := fixture{key: raw, logs: new(lockedBuffer)}
	logger := slog.New(slog.NewJSONHandler(f.logs, nil))
	f.executor, err = execution.New(store, manager, client, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.executor.Close)
	f.handler, err = gateway.New(f.executor, logger)
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(f.handler)
	f.server.Client().Timeout = 3 * time.Second
	t.Cleanup(f.server.Close)
	return f
}

func (f fixture) request(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+f.key)
	r.Header.Set("Content-Type", "application/json")
	resp, err := f.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
