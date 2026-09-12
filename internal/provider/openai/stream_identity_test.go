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

func TestStreamRejectsUsageFromConflictingResponse(t *testing.T) {
	for _, tc := range []struct{ name, event, status, id, model string }{
		{name: "created ID", event: "response.created", status: "in_progress", id: "resp_2", model: "test-model"},
		{name: "progress ID", event: "response.in_progress", status: "in_progress", id: "resp_2", model: "test-model"},
		{name: "completed ID", event: "response.completed", status: "completed", id: "resp_2", model: "test-model"},
		{name: "failed ID", event: "response.failed", status: "failed", id: "resp_2", model: "test-model"},
		{name: "failed model", event: "response.failed", status: "failed", id: "resp_1", model: "other-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard,
				`{"type":"response.created","response":{"id":"resp_1","model":"test-model","usage":{"input_tokens":3}}}`,
				`{"type":"`+tc.event+`","response":{"id":"`+tc.id+`","model":"`+tc.model+`","status":"`+tc.status+`","output":[],"error":{"code":"server_error"},"usage":{"input_tokens":70}}}`,
			)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			want := []generation.Event{
				generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
				generation.UsageUpdated{Usage: usage.Snapshot{Input: count(3)}},
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v; want only the original response and its usage", events)
			}
		})
	}
}

func TestRetrieveResponseStreamRejectsForeignFailureUsage(t *testing.T) {
	client := streamClient(t, io.Discard,
		`{"type":"response.failed","response":{"id":"resp_2","model":"test-model","status":"failed","error":{"code":"server_error"},"usage":{"input_tokens":70}}}`,
	)
	stream, err := client.RetrieveResponseStream(t.Context(), testAttempt(), "resp_1", openai.ResponseRetrieveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	event, err := stream.Next()

	assertProtocolError(t, err)
	if event != nil {
		t.Fatalf("event = %#v; foreign response must not supply usage", event)
	}
}

func TestStreamRetainsMatchingUsageWhenEnvelopeContentIsMalformed(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		`{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":"private","usage":{"input_tokens":70}}}`,
	)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.UsageUpdated{Usage: usage.Snapshot{Input: count(70)}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v; want the matching response's known usage", events)
	}
}

func TestStreamBoundsResponseIdentityAndRetainsUsage(t *testing.T) {
	for _, tc := range []struct{ name, id, model string }{
		{name: "ID", id: strings.Repeat("x", 300), model: "test-model"},
		{name: "model", id: "resp_1", model: strings.Repeat("x", 300)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := frame(`{"type":"response.completed","response":{"id":"` + tc.id + `","model":"` + tc.model + `","status":"completed","output":[],"usage":{"input_tokens":2}}}`)
			client := boundedStreamClient(t, body, 128)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			want := []generation.Event{generation.UsageUpdated{Usage: usage.Snapshot{Input: count(2)}}}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v; want usage without an oversized response", events)
			}
		})
	}
}

func TestStreamChargesResponseIdentityOnce(t *testing.T) {
	body := strings.Repeat(frame(created()), 5) + frame(`{"type":"response.completed","response":`+responseWithItems("")+`}`)
	client := boundedStreamClient(t, body, 64)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "stop"}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v; want one start and end", events)
	}
}

func boundedStreamClient(t *testing.T, body string, limit int64) *openai.Client {
	t.Helper()
	client, err := openai.New(openai.Config{BaseURL: "https://example.invalid/v1", MaxResponseBytes: limit, MaxEventBytes: 4096},
		&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return client
}
