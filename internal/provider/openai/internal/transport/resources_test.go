package transport_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/deyna256/clan/internal/provider/openai/internal/transport"
	"github.com/stretchr/testify/require"
)

func TestResourceRoutePreservesRootAndEscapesIDs(t *testing.T) {
	requests := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(server.Close)
	client := newClient(t, server.URL+"/proxy%20root/v1/", server.Client())

	response, err := client.Do(t.Context(), "key-a", transport.Request{Method: http.MethodGet,
		Path: []string{"responses", "resp?#%2F", "input_items"}, Query: url.Values{"after": {"cursor&part=2"}}, Accept: "application/json"})
	require.NoError(t, err)
	body := readBody(t, response)
	got := <-requests

	if got.URL.EscapedPath() != "/proxy%20root/v1/responses/resp%3F%23%252F/input_items" || got.URL.Query().Get("after") != "cursor&part=2" {
		t.Fatalf("resource URL = %s", got.URL)
	}
	if body != `{"object":"list","data":[]}` || got.Method != http.MethodGet || got.Header.Get("Authorization") != "Bearer key-a" || got.Header.Get("Content-Type") != "" || got.Header.Get("Accept") != "application/json" {
		t.Fatalf("request = %v, headers = %v, body = %q", got.Method, got.Header, body)
	}
}

func TestResourceRouteRejectsTraversalBeforeDispatch(t *testing.T) {
	client := newClient(t, "https://example.invalid/v1", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid path reached transport")
		return nil, errors.New("unexpected dispatch")
	})})
	for _, id := range []string{"", ".", "..", "../responses", "a/b", "a\\b", "a\r\nb"} {
		t.Run(id, func(t *testing.T) {
			response, err := client.Do(t.Context(), "key-a", transport.Request{Method: http.MethodDelete, Path: []string{"responses", id}, Accept: "application/json"})

			if response != nil || err == nil {
				t.Fatalf("Do = %v, %v; want path error", response, err)
			}
		})
	}
}
