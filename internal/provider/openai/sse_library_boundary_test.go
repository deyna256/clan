package openai_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/deyna256/clan/internal/usage"
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

			if !errors.Is(err, tc.endErr) || !reflect.DeepEqual(events, tc.want) {
				t.Fatalf("stream = %#v, %v; want %#v, %v", events, err, tc.want, tc.endErr)
			}
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
	if err != nil {
		t.Fatal(err)
	}

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.UsageUpdated{Usage: usage.Snapshot{Input: count(2)}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v; want start and known usage only", events)
	}
}
