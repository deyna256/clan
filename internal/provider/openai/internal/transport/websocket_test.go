package transport_test

import (
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/deyna256/clan/internal/provider/openai/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestWebSocketHandshakeUsesConfiguredRootWithoutCookiesOrRedirects(t *testing.T) {
	var requests atomic.Int32
	captured := make(chan requestCapture, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		captured <- requestCapture{method: r.Method, path: r.URL.EscapedPath(), header: r.Header.Clone()}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	base, err := url.Parse(server.URL)
	require.NoError(t, err)
	jar.SetCookies(base, []*http.Cookie{{Name: "session", Value: "another-account"}})
	original := server.Client()
	original.Jar = jar
	client := newClient(t, server.URL+"/proxy%20root/v1", original)

	conn, response, err := client.DialWebSocket(t.Context(), "key-a")

	if conn != nil || err == nil || response == nil || response.StatusCode != 307 || requests.Load() != 1 {
		t.Fatalf("handshake = %v, %#v, %v; requests=%d", conn, response, err, requests.Load())
	}
	got := <-captured
	if got.method != "GET" || got.path != "/proxy%20root/v1/responses" || got.header.Get("Authorization") != "Bearer key-a" || got.header.Get("Cookie") != "" || got.header.Get("Upgrade") != "websocket" {
		t.Fatalf("handshake request = %#v", got)
	}
	if original.Jar != jar || original.CheckRedirect != nil {
		t.Fatal("caller HTTP client changed")
	}
}

func TestWebSocketRejectsInvalidKeyBeforeDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid key reached transport") }))
	t.Cleanup(server.Close)
	client := newClient(t, server.URL, server.Client())

	conn, response, err := client.DialWebSocket(t.Context(), "private\r\nkey")

	if conn != nil || response != nil || !errors.Is(err, transport.ErrInvalidKey) {
		t.Fatalf("handshake = %v, %v, %v; want invalid key", conn, response, err)
	}
}
