package management_test

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/credentialcipher"
	"github.com/deyna256/clan/internal/execution"
	"github.com/deyna256/clan/internal/management"
	"github.com/deyna256/clan/internal/storage"
)

const adminToken = "admin-test-token"
const authorization = "Bearer " + adminToken

type fixture struct {
	handler  http.Handler
	store    *storage.Store
	oauth    *codexoauth.Manager
	executor *execution.Executor
	logs     *bytes.Buffer
	path     string
}

func newFixture(t *testing.T, transport http.RoundTripper) fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("temporary SQLite integration")
	}
	cipher, err := credentialcipher.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{path: filepath.Join(t.TempDir(), "management.db"), logs: new(bytes.Buffer)}
	f.store, err = storage.Open(t.Context(), f.path, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.store.Close(); err != nil {
			t.Error(err)
		}
	})
	if transport == nil {
		transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(`{"models":[{"slug":"z-model","display_name":"Z model","visibility":"list"},` +
				`{"slug":"model","display_name":"Useful MODEL","visibility":"list"},` +
				`{"slug":"hidden","visibility":"hide"}]}`), nil
		})
	}
	httpClient := &http.Client{Transport: transport}
	oauthClient, err := codexoauth.NewClient(httpClient, "")
	if err != nil {
		t.Fatal(err)
	}
	f.oauth, err = codexoauth.NewManager(t.Context(), oauthClient, f.store, &idleListener{closed: make(chan struct{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.oauth.Close(); err != nil {
			t.Error(err)
		}
	})
	client, err := codex.NewClient(httpClient, "https://provider.example", "test")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(f.logs, nil))
	f.executor, err = execution.New(f.store, f.oauth, client, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.executor.Close)
	f.handler, err = management.New(management.Config{AdminToken: adminToken}, f.store, f.oauth, f.executor, logger)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) addAccount(t *testing.T, id, name string, enabled bool) {
	t.Helper()
	a, err := account.New(account.Identity{ID: account.ID(id), Name: name}, account.OAuthCredentials{
		ChatGPTAccountID: "provider-identity", AccessToken: "credential-marker", RefreshToken: "refresh-marker",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateAccount(t.Context(), a, enabled); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) addKey(t *testing.T, id, name string, enabled bool) string {
	t.Helper()
	raw, hash := accesskey.Generate()
	key, err := accesskey.New(accesskey.Identity{ID: accesskey.ID(id), Name: name}, enabled)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateAccessKey(t.Context(), key, hash, 1); err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f fixture) open(t *testing.T, key string) *execution.Stream {
	t.Helper()
	request, err := codex.ParseRequest([]byte(`{"model":"model","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := f.executor.Stream(t.Context(), key, "active", request)
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func call(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func requireStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, want, response.Body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response does not prevent caching")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type idleListener struct{ closed chan struct{} }

func (l *idleListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *idleListener) Close() error              { close(l.closed); return nil }
func (l *idleListener) Addr() net.Addr            { return &net.TCPAddr{Port: 1455} }
