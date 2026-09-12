package openai_test

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/deyna256/clan/internal/usage"
)

func TestStreamRetainsUsageOnInvalidUTF8(t *testing.T) {
	started := generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}}
	counted := generation.UsageUpdated{Usage: usage.Snapshot{Input: count(12), Output: count(3)}}
	for _, tc := range []struct {
		name    string
		foreign bool
		want    []generation.Event
	}{
		{name: "current response", want: []generation.Event{started, counted}},
		{name: "foreign response", foreign: true, want: []generation.Event{started}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), invalidUTF8Completion(tc.foreign))

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			if !reflect.DeepEqual(events, tc.want) {
				t.Fatalf("events = %#v; want %#v without content", events, tc.want)
			}
		})
	}
}

func TestSessionRetainsUsageOnInvalidUTF8(t *testing.T) {
	started := openai.SessionEvent{ResponseID: "resp_1", Event: generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}}}
	counted := openai.SessionEvent{ResponseID: "resp_1", Event: generation.UsageUpdated{Usage: usage.Snapshot{Input: count(12), Output: count(3)}}}
	for _, tc := range []struct {
		name    string
		foreign bool
		want    []openai.SessionEvent
	}{
		{name: "current response", want: []openai.SessionEvent{started, counted}},
		{name: "foreign response", foreign: true, want: []openai.SessionEvent{started}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := sessionFrames(t, openai.Config{}, created(), invalidUTF8Completion(tc.foreign))
			session := openTestSession(t, client)

			events, err := readSession(session)

			assertProtocolError(t, err)
			if !reflect.DeepEqual(events, tc.want) {
				t.Fatalf("events = %#v; want %#v without content", events, tc.want)
			}
		})
	}
}

func invalidUTF8Completion(foreign bool) string {
	body := textResponse("Hello", `{"input_tokens":12,"output_tokens":3}`)
	body = strings.Replace(body, "Hello", "\xff", 1)
	if foreign {
		body = strings.Replace(body, "resp_1", "resp_other", 1)
	}
	return `{"type":"response.completed","response":` + body + `}`
}

func TestGenerateRetainsUsageOnConflictingToolIdentity(t *testing.T) {
	const calls = `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"},
		{"type":"function_call","id":"fc_2","call_id":"call_1","name":"delete","arguments":"{}"}`
	body := strings.TrimSuffix(responseWithItems(calls), "}") + `,"usage":{"input_tokens":12,"output_tokens":3}}`
	client := testClient(t, staticJSON(body), io.Discard)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	assertProtocolError(t, err)
	want := usage.Snapshot{Input: count(12), Output: count(3)}
	if result.Usage != want || result.Response.Output != nil {
		t.Fatalf("result = %+v; want usage %+v without executable output", result, want)
	}
}

func TestSessionUsesConsistentResponseIdentity(t *testing.T) {
	for _, tc := range []struct{ name, identity string }{
		{name: "duplicate keys", identity: `"id":"foreign","id":"resp_1"`},
		{name: "case insensitive decoding", identity: `"ID":"resp_1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal := `{"type":"response.completed","response":{` + tc.identity + `,"model":"test-model","status":"completed","output":[],"usage":{"input_tokens":12}}}`
			session := openTestSession(t, sessionFrames(t, openai.Config{}, terminal))

			events, err := readSession(session)

			if !errors.Is(err, io.EOF) || len(events) != 3 {
				t.Fatalf("events = %#v, error = %v; want start, usage and completion", events, err)
			}
			for _, event := range events {
				if event.ResponseID != "resp_1" {
					t.Fatalf("response ID = %q; want resp_1", event.ResponseID)
				}
			}
			started, ok := events[0].Event.(generation.ResponseStarted)
			if !ok || started.Identity.ID != "resp_1" {
				t.Fatalf("start = %#v; want resp_1", events[0].Event)
			}
		})
	}
}

func TestMalformedStreamControlsCannotClaimUsage(t *testing.T) {
	t.Run("SSE event type", func(t *testing.T) {
		bad := strings.Replace(invalidUTF8Completion(false), "response.completed", "response.completed\xff", 1)
		client := streamClient(t, io.Discard, created(), bad)

		events, err := readStream(t, client)

		if !errors.Is(err, io.ErrUnexpectedEOF) || len(events) != 1 {
			t.Fatalf("events = %#v, error = %v; want start only and interrupted stream", events, err)
		}
		eventAt[generation.ResponseStarted](t, events, 0)
	})
	t.Run("WebSocket lane", func(t *testing.T) {
		bad := laneEvent("a", invalidUTF8Completion(false))
		bad = strings.Replace(bad, `"stream_id":"a"`, "\"stream_id\":\"a\xff\"", 1)
		session := openTestSession(t, sessionFrames(t, openai.Config{}, laneEvent("a", created()), bad))

		events, err := readSession(session)

		assertProtocolError(t, err)
		if len(events) != 1 || events[0].Lane != "a" || events[0].ResponseID != "resp_1" {
			t.Fatalf("events = %#v; malformed lane must not claim usage", events)
		}
		if _, ok := events[0].Event.(generation.ResponseStarted); !ok {
			t.Fatalf("event = %#v; want start only", events[0].Event)
		}
	})
}
