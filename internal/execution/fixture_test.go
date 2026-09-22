package execution_test

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
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
	"github.com/deyna256/clan/internal/storage"
)

type fixture struct {
	executor *execution.Executor
	store    *storage.Store
	path     string
	key      string
	request  codex.Request
	logs     *bytes.Buffer
}

func newFixture(t *testing.T, generation http.RoundTripper) fixture {
	t.Helper()
	return newFixtureWithCatalog(t, generation, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"models":[{"slug":"model","visibility":"list"}]}`), nil
	}))
}

func newFixtureWithCatalog(t *testing.T, generation, catalog http.RoundTripper) fixture {
	t.Helper()
	if testing.Short() {
		t.Skip("temporary SQLite integration")
	}
	cipher, err := credentialcipher.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{path: filepath.Join(t.TempDir(), "execution.db"), logs: new(bytes.Buffer)}
	f.store, err = storage.Open(t.Context(), f.path, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.store.Close(); err != nil {
			t.Error(err)
		}
	})
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/models" {
			return catalog.RoundTrip(r)
		}
		return generation.RoundTrip(r)
	})
	httpClient := &http.Client{Transport: transport}
	oauthClient, err := codexoauth.NewClient(httpClient, "")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := codexoauth.NewManager(t.Context(), oauthClient, f.store, &idleListener{closed: make(chan struct{})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	client, err := codex.NewClient(httpClient, "https://provider.example", "test")
	if err != nil {
		t.Fatal(err)
	}
	f.executor, err = execution.New(f.store, manager, client, slog.New(slog.NewJSONHandler(f.logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.executor.Close)
	f.request, err = codex.ParseRequest([]byte(`{"model":"model","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	f.addAccount(t, "one")
	f.key = f.addKey(t, "key", 1)
	return f
}

func (f fixture) addAccount(t *testing.T, id account.ID) {
	t.Helper()
	a, err := account.New(account.Identity{ID: id, Name: string(id)}, account.OAuthCredentials{
		ChatGPTAccountID: string(id), AccessToken: "access-" + string(id), ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateAccount(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) addKey(t *testing.T, id accesskey.ID, limit int) string {
	t.Helper()
	raw, hash := accesskey.Generate()
	key, err := accesskey.New(accesskey.Identity{ID: id, Name: string(id)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateAccessKey(t.Context(), key, hash, limit); err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f fixture) open(t *testing.T, key string) *execution.Stream {
	t.Helper()
	s, err := f.executor.Stream(t.Context(), key, execution.RequestInfo{ID: "request"}, f.request)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// An unused listener lets virtual-time tests run without real sockets.
type idleListener struct{ closed chan struct{} }

func (l *idleListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *idleListener) Close() error              { close(l.closed); return nil }
func (l *idleListener) Addr() net.Addr            { return &net.TCPAddr{Port: 1455} }
