package transport_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/deyna256/clan/internal/provider/openai/internal/transport"
)

func TestResourceRoutePreservesRootAndEscapesIDs(t *testing.T) {
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		_, _ = io.WriteString(w, "file bytes")
	}))
	t.Cleanup(server.Close)
	client := newClient(t, server.URL+"/proxy%20root/v1/", server.Client())

	response, err := client.Do(t.Context(), "key-a", transport.Request{Method: http.MethodGet,
		Path: []string{"files", "file?#%2F", "content"}, Query: url.Values{"after": {"cursor&part=2"}}, Accept: "application/octet-stream"})
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, response)
	got := <-requests

	if got.URL.EscapedPath() != "/proxy%20root/v1/files/file%3F%23%252F/content" || got.URL.Query().Get("after") != "cursor&part=2" {
		t.Fatalf("resource URL = %s", got.URL)
	}
	if body != "file bytes" || got.Method != http.MethodGet || got.Header.Get("Authorization") != "Bearer key-a" || got.Header.Get("Content-Type") != "" {
		t.Fatalf("request = %v, headers = %v, body = %q", got.Method, got.Header, body)
	}
}

func TestResourceRouteRejectsTraversalBeforeDispatch(t *testing.T) {
	client := newClient(t, "https://example.invalid/v1", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid path reached transport")
		return nil, nil
	})})
	for _, id := range []string{"", ".", "..", "../files", "a/b", "a\\b", "a\r\nb"} {
		t.Run(id, func(t *testing.T) {
			response, err := client.Do(t.Context(), "key-a", transport.Request{Method: http.MethodDelete, Path: []string{"files", id}, Accept: "application/json"})

			if response != nil || err == nil {
				t.Fatalf("Do = %v, %v; want path error", response, err)
			}
		})
	}
}
