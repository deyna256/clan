package openai_test

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/deyna256/clan/internal/usage"
	"github.com/stretchr/testify/require"
)

func TestSSELibraryEOFStillRequiresTerminalResponse(t *testing.T) {
	completed := `{"type":"response.completed","response":` + responseWithItems("") + `}`
	started := generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}}
	for _, tc := range []struct {
		name   string
		body   string
		want   []generation.Event
		endErr error
	}{
		{
			name:   "terminal JSON with final newline",
			body:   "data: " + completed + "\n",
			want:   []generation.Event{started, generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "stop"}}},
			endErr: io.EOF,
		},
		{
			name: "nonterminal JSON with final newline",
			body: "data: " + created() + "\n",
			want: []generation.Event{started}, endErr: io.ErrUnexpectedEOF,
		},
		{
			name: "terminal JSON without final newline",
			body: "data: " + completed, endErr: io.ErrUnexpectedEOF,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := boundedStreamClient(t, tc.body, 4096)

			events, err := readStream(t, client)

			require.ErrorIs(t, err, tc.endErr)
			require.Equal(t, tc.want, events)
		})
	}
}

func TestSSEEventLimitIsProtocolErrorAndRetainsUsage(t *testing.T) {
	initial := frame(`{"type":"response.created","response":{"id":"resp_1","model":"test-model","usage":{"input_tokens":2}}}`)
	body := initial + ": private " + strings.Repeat("x", len(initial)) + "\n\n"
	client, err := openai.New(openai.Config{
		BaseURL: "https://example.invalid/v1", MaxResponseBytes: 4096, MaxEventBytes: len(initial),
	}, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.UsageUpdated{Usage: usage.Snapshot{Input: count(2)}},
	}
	require.Equal(t, want, events)
}
