package openai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/deyna256/clan/internal/retry"
	"github.com/deyna256/clan/internal/usage"
)

func TestSessionInterleavesLanesAndResetsResponseState(t *testing.T) {
	client := sessionFrames(t, openai.Config{},
		laneEvent("a", created()),
		laneEvent("b", strings.ReplaceAll(created(), "resp_1", "resp_2")),
		laneEvent("a", messageAdded()),
		laneEvent("b", messageAdded()),
		laneEvent("a", `{"type":"response.completed","response":`+textResponse("Alpha", `{"input_tokens":10,"output_tokens":2}`)+`}`),
		laneEvent("b", strings.ReplaceAll(`{"type":"response.completed","response":`+textResponse("Beta", `{"input_tokens":3,"output_tokens":1}`)+`}`, "resp_1", "resp_2")),
		laneEvent("a", `{"type":"response.completed","response":{"id":"resp_3","model":"test-model","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0}}}`),
	)
	session := openTestSession(t, client)

	events, err := readSession(session)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	texts := map[string]string{}
	usages := map[string]usage.Snapshot{}
	ends := map[string]int{}
	for _, event := range events {
		if event.Failure != nil || (event.Lane != "a" && event.Lane != "b") {
			t.Fatalf("unexpected envelope: %#v", event)
		}
		if event.ResponseID == "resp_2" && event.Lane != "b" || event.ResponseID != "resp_2" && event.Lane != "a" {
			t.Fatalf("response on wrong lane: %#v", event)
		}
		switch value := event.Event.(type) {
		case generation.ItemEnded:
			texts[event.ResponseID] = value.Item.(generation.Message).Parts[0].(generation.Text).Text
		case generation.UsageUpdated:
			usages[event.ResponseID] = value.Usage
		case generation.ResponseEnded:
			ends[event.ResponseID]++
		}
	}
	if !reflect.DeepEqual(texts, map[string]string{"resp_1": "Alpha", "resp_2": "Beta"}) || !reflect.DeepEqual(ends, map[string]int{"resp_1": 1, "resp_2": 1, "resp_3": 1}) {
		t.Fatalf("text/end snapshots = %#v / %#v", texts, ends)
	}
	wantUsage := map[string]usage.Snapshot{
		"resp_1": {Input: count(10), Output: count(2)},
		"resp_2": {Input: count(3), Output: count(1)},
		"resp_3": {Input: count(1), Output: count(0)},
	}
	if !reflect.DeepEqual(usages, wantUsage) {
		t.Fatalf("usage = %#v; want %#v", usages, wantUsage)
	}
}

func TestSessionRequestErrorDoesNotClaimActiveResponse(t *testing.T) {
	client := sessionFrames(t, openai.Config{},
		laneEvent("a", `{"type":"response.created","response":{"id":"resp_1","model":"test-model","status":"in_progress","output":[],"usage":{"input_tokens":9}}}`),
		`{"type":"error","stream_id":"a","status":400,"error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"private","param":null,"headers":{"Retry-After":"Tue, 01 Jan 2030 00:00:00 GMT","X-Trace":42}}}`,
		laneEvent("a", `{"type":"response.completed","response":`+responseWithItems("")+`}`),
	)
	session := openTestSession(t, client)

	events, err := readSession(session)

	if !errors.Is(err, io.EOF) || len(events) != 4 {
		t.Fatalf("events = %#v, %v; want start, usage, request failure, end", events, err)
	}
	failed := events[2]
	var failure *generation.Failure
	if failed.Lane != "a" || failed.ResponseID != "" || failed.Event != nil || failed.Failure == nil || failed.Failure.Usage != (usage.Snapshot{}) || !errors.As(failed.Failure, &failure) || failure.Kind != generation.InvalidRequest || strings.Contains(failed.Failure.Error(), "private") {
		t.Fatalf("request failure = %#v; must not claim active response or its usage", failed)
	}
	var responseError *openai.HTTPError
	if !errors.As(failed.Failure, &responseError) || responseError.Cooldown != (retry.Cooldown{Kind: retry.RetryAt, Until: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}) {
		t.Fatalf("failure lost valid retry header: %v", failed.Failure)
	}
	if _, ok := events[3].Event.(generation.ResponseEnded); !ok || events[3].ResponseID != "resp_1" {
		t.Fatalf("active response lost after request error: %#v", events[3])
	}
}

func TestSessionResponseFailurePreservesUsageAndOtherLanes(t *testing.T) {
	client := sessionFrames(t, openai.Config{},
		laneEvent("a", created()),
		laneEvent("b", strings.ReplaceAll(created(), "resp_1", "resp_2")),
		laneEvent("a", `{"type":"response.failed","response":{"id":"resp_1","model":"test-model","status":"failed","output":[],"error":{"code":"server_error","message":"private"},"usage":{"input_tokens":7,"output_tokens":2}}}`),
		laneEvent("b", strings.ReplaceAll(`{"type":"response.completed","response":`+responseWithItems("")+`}`, "resp_1", "resp_2")),
	)
	session := openTestSession(t, client)

	events, err := readSession(session)

	if !errors.Is(err, io.EOF) || len(events) != 5 {
		t.Fatalf("events = %#v, %v", events, err)
	}
	failed := events[3]
	var failure *generation.Failure
	if failed.Lane != "a" || failed.ResponseID != "resp_1" || failed.Failure == nil || failed.Failure.Usage != (usage.Snapshot{Input: count(7), Output: count(2)}) || !errors.As(failed.Failure, &failure) || failure.Kind != generation.Unavailable {
		t.Fatalf("response failure = %#v", failed)
	}
	if _, ok := events[4].Event.(generation.ResponseEnded); !ok || events[4].Lane != "b" {
		t.Fatalf("other lane did not complete: %#v", events[4])
	}
}

func TestSessionRejectsUsageFromConflictingResponse(t *testing.T) {
	for _, tc := range []struct{ name, id, model string }{
		{name: "ID", id: "resp_2", model: "test-model"},
		{name: "model", id: "resp_1", model: "other-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := sessionFrames(t, openai.Config{},
				laneEvent("a", `{"type":"response.created","response":{"id":"resp_1","model":"test-model","usage":{"input_tokens":3}}}`),
				laneEvent("a", `{"type":"response.failed","response":{"id":"`+tc.id+`","model":"`+tc.model+`","status":"failed","error":{"code":"server_error"},"usage":{"input_tokens":70}}}`),
			)
			session := openTestSession(t, client)

			events, err := readSession(session)

			assertProtocolError(t, err)
			want := []openai.SessionEvent{
				{Lane: "a", ResponseID: "resp_1", Event: generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}}},
				{Lane: "a", ResponseID: "resp_1", Event: generation.UsageUpdated{Usage: usage.Snapshot{Input: count(3)}}},
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v; want only the original response and its usage", events)
			}
		})
	}
}

func TestSessionSendCreateKeepsSameLaneOrderAndWarmupID(t *testing.T) {
	requests := make(chan string, 2)
	client := sessionServer(t, openai.Config{}, io.Discard, func(conn *websocket.Conn) {
		for range 2 {
			_, body, err := conn.Read(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			requests <- string(body)
		}
		_ = conn.Write(t.Context(), websocket.MessageText, []byte(laneEvent("a", `{"type":"response.completed","response":{"id":"resp_warm","model":"test-model","status":"completed","output":[]}}`)))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
	session := openTestSession(t, client)
	request := generation.Request{Model: "test-model"}

	invalid := session.SendCreate(t.Context(), request, openai.CreateOptions{Lane: "bad lane"})
	first := session.SendCreate(t.Context(), request, openai.CreateOptions{Lane: "a", Generate: generation.Some(false)})
	request.Instructions = generation.Some("second")
	second := session.SendCreate(t.Context(), request, openai.CreateOptions{Lane: "a"})
	events, err := readSession(session)

	if first != nil || second != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("send/read = %v, %v, %v", first, second, err)
	}
	var input *openai.InputError
	if !errors.As(invalid, &input) || input.Field != "stream_id" {
		t.Fatalf("invalid lane = %v", invalid)
	}
	assertSessionJSON(t, <-requests, `{"type":"response.create","model":"test-model","input":[],"stream_id":"a","generate":false}`)
	assertSessionJSON(t, <-requests, `{"type":"response.create","model":"test-model","input":[],"stream_id":"a","instructions":"second"}`)
	if len(events) != 2 || events[0].ResponseID != "resp_warm" || events[1].ResponseID != "resp_warm" {
		t.Fatalf("warmup events = %#v", events)
	}
	if _, ok := events[1].Event.(generation.ResponseEnded); !ok {
		t.Fatalf("warmup did not complete: %#v", events[1])
	}
}

func TestSessionProtocolFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []string
	}{
		{name: "null lane", frames: []string{`{"type":"ping","stream_id":null}`}},
		{name: "empty named lane", frames: []string{`{"type":"ping","stream_id":""}`}},
		{name: "bad lane", frames: []string{`{"type":"ping","stream_id":"private lane"}`}},
		{name: "malformed JSON", frames: []string{`{"type":`}},
		{name: "item before response", frames: []string{messageAdded()}},
		{name: "delta before response", frames: []string{`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"private"}`}},
		{name: "overlapping lane", frames: []string{created(), strings.ReplaceAll(created(), "resp_1", "resp_2")}},
		{name: "changed sequence", frames: []string{`{"sequence_number":1,` + strings.TrimPrefix(created(), "{"), `{"sequence_number":1,"type":"response.in_progress","response":{"id":"resp_1","model":"test-model"}}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := openTestSession(t, sessionFrames(t, openai.Config{}, tc.frames...))

			_, err := readSession(session)

			assertProtocolError(t, err)
		})
	}
}

func TestSessionSkipsUnknownIdleEventsAndDuplicateTerminal(t *testing.T) {
	var logs bytes.Buffer
	completed := `{"sequence_number":1,"type":"response.completed","response":` + responseWithItems("") + `}`
	frames := []string{
		`{"type":"response.future","stream_id":"unused","delta":{}}`,
		created(),
		`{"type":"response.future","delta":{}}`,
		completed, completed,
		`{"type":"response.future"}`,
		strings.ReplaceAll(completed, "resp_1", "resp_2"),
	}
	client := sessionServer(t, openai.Config{}, &logs, func(conn *websocket.Conn) {
		for _, frame := range frames {
			_ = conn.Write(t.Context(), websocket.MessageText, []byte(frame))
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
	session := openTestSession(t, client)

	events, err := readSession(session)

	if !errors.Is(err, io.EOF) || len(events) != 4 {
		t.Fatalf("events = %#v, %v; want start/end for each distinct response", events, err)
	}
	if events[0].ResponseID != "resp_1" || events[2].ResponseID != "resp_2" || !strings.Contains(logs.String(), `"reason":"unknown_event"`) {
		t.Fatalf("response IDs/logs = %#v / %s", events, logs.String())
	}
}

func TestSessionAggregateOverflowDoesNotPublishCompletion(t *testing.T) {
	for _, tc := range []struct{ name, id, model string }{
		{name: "model", id: "resp_1", model: strings.Repeat("x", 200)},
		{name: "response ID", id: strings.Repeat("x", 200), model: "test-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completed := `{"type":"response.completed","response":{"id":"` + tc.id + `","model":"` + tc.model + `","status":"completed","output":[],"usage":{"input_tokens":2}}}`
			session := openTestSession(t, sessionFrames(t, openai.Config{MaxResponseBytes: 250}, completed))

			events, err := readSession(session)

			assertProtocolError(t, err)
			if len(events) != 1 {
				t.Fatalf("events = %#v; want usage only", events)
			}
			got, ok := events[0].Event.(generation.UsageUpdated)
			if !ok || got.Usage.Input != count(2) || events[0].ResponseID != tc.id {
				t.Fatalf("event = %#v; want attributed known usage", events[0])
			}
		})
	}
}

func TestSessionConcurrentSendsAndReads(t *testing.T) {
	client := sessionServer(t, openai.Config{}, io.Discard, func(conn *websocket.Conn) {
		for i := range 12 {
			if _, _, err := conn.Read(t.Context()); err != nil {
				return
			}
			frame := `{"type":"response.completed","response":{"id":"resp_` + strconv.Itoa(i) + `","model":"test-model","status":"completed","output":[]}}`
			if conn.Write(t.Context(), websocket.MessageText, []byte(frame)) != nil {
				return
			}
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
	session := openTestSession(t, client)
	sent := make(chan error, 12)
	var workers sync.WaitGroup

	for range 12 {
		workers.Go(func() { sent <- session.SendCreate(t.Context(), textRequest(), openai.CreateOptions{}) })
	}
	events, err := readSession(session)
	workers.Wait()
	close(sent)

	for sendErr := range sent {
		if sendErr != nil {
			t.Error(sendErr)
		}
	}
	if !errors.Is(err, io.EOF) || len(events) != 24 {
		t.Fatalf("events/read = %d, %v; want 12 complete responses", len(events), err)
	}
}

func TestSessionReleasesCompletedResponseMemory(t *testing.T) {
	completed := `{"type":"response.completed","response":` + textResponse(strings.Repeat("a", 700), `null`) + `}`
	client := sessionFrames(t, openai.Config{MaxResponseBytes: 1100}, completed, strings.ReplaceAll(completed, "resp_1", "resp_2"))
	session := openTestSession(t, client)

	events, err := readSession(session)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var ended []string
	for _, event := range events {
		if _, ok := event.Event.(generation.ResponseEnded); ok {
			ended = append(ended, event.ResponseID)
		}
	}
	if !reflect.DeepEqual(ended, []string{"resp_1", "resp_2"}) {
		t.Fatalf("ended = %v", ended)
	}
}

func TestSessionNormalCloseWithActiveResponseIsUnexpectedEOF(t *testing.T) {
	session := openTestSession(t, sessionFrames(t, openai.Config{}, created()))

	_, err := readSession(session)
	_, again := session.Next()

	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(again, err) {
		t.Fatalf("terminal errors = %v, %v; want stable unexpected EOF", err, again)
	}
}

func TestSessionUnscopedRequestErrorClosesAllLanes(t *testing.T) {
	session := openTestSession(t, sessionFrames(t, openai.Config{}, laneEvent("a", created()),
		`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"websocket_connection_limit_reached","message":"private","param":null}}`))

	events, err := readSession(session)

	var failure *generation.Failure
	if len(events) != 1 || !errors.As(err, &failure) || failure.Kind != generation.InvalidRequest {
		t.Fatalf("events/error = %#v, %v", events, err)
	}
	if err := session.SendCreate(t.Context(), textRequest(), openai.CreateOptions{Lane: "b"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("send after global error = %v", err)
	}
}

func TestSessionBoundsFramesAndAggregateLaneState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config openai.Config
		frames []string
	}{
		{name: "frame", config: openai.Config{MaxEventBytes: 64}, frames: []string{created()}},
		{name: "aggregate", config: openai.Config{MaxResponseBytes: 250}, frames: []string{laneEvent("a", created()), laneEvent("b", strings.ReplaceAll(created(), "resp_1", "resp_2"))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := openTestSession(t, sessionFrames(t, tc.config, tc.frames...))

			_, err := readSession(session)

			assertProtocolError(t, err)
		})
	}
}

func TestSessionCancellationInterruptsReadAndConcurrentClose(t *testing.T) {
	peerClosed := make(chan struct{})
	client := sessionServer(t, openai.Config{}, io.Discard, func(conn *websocket.Conn) {
		_, _, _ = conn.Read(t.Context())
		close(peerClosed)
	})
	ctx, cancel := context.WithCancel(t.Context())
	session, err := client.OpenSession(ctx, testAttempt())
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() { _, err := session.Next(); read <- err }()

	cancel()
	var workers sync.WaitGroup
	for range 3 {
		workers.Go(func() { _ = session.Close() })
	}
	workers.Wait()

	select {
	case err := <-read:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not interrupt Next")
	}
	select {
	case <-peerClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not close connection")
	}
	if err := session.SendCreate(t.Context(), textRequest(), openai.CreateOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("send on closed session = %v", err)
	}
}

func TestSessionHandshakePreservesRootAndClassifiesErrors(t *testing.T) {
	var requests int
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/responses" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer secret-key" || r.Header.Get("Upgrade") != "websocket" {
			t.Errorf("unexpected handshake: %s %s headers=%v", r.Method, r.URL, r.Header)
		}
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"private"},"usage":{"input_tokens":3}}`)
	}, io.Discard)

	session, err := client.OpenSession(t.Context(), testAttempt())

	var startup *openai.StartupError
	var failure *generation.Failure
	if session != nil || !errors.As(err, &startup) || !errors.As(err, &failure) || failure.Kind != generation.RateLimited || startup.Result.Usage.Input != count(3) || requests != 1 || strings.Contains(err.Error(), "private") {
		t.Fatalf("handshake = %v, %#v; requests=%d", session, err, requests)
	}
}

func sessionServer(t *testing.T, config openai.Config, logs io.Writer, serve func(*websocket.Conn)) *openai.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		serve(conn)
	}))
	t.Cleanup(server.Close)
	config.BaseURL = server.URL
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = 1 << 20
	}
	if config.MaxEventBytes == 0 {
		config.MaxEventBytes = 1 << 20
	}
	client, err := openai.New(config, server.Client(), slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func sessionFrames(t *testing.T, config openai.Config, frames ...string) *openai.Client {
	t.Helper()
	return sessionServer(t, config, io.Discard, func(conn *websocket.Conn) {
		for _, frame := range frames {
			if conn.Write(t.Context(), websocket.MessageText, []byte(frame)) != nil {
				return
			}
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
}

func openTestSession(t *testing.T, client *openai.Client) *openai.Session {
	t.Helper()
	session, err := client.OpenSession(t.Context(), testAttempt())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func readSession(session *openai.Session) ([]openai.SessionEvent, error) {
	var events []openai.SessionEvent
	for {
		event, err := session.Next()
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

func laneEvent(lane, event string) string {
	encoded, _ := json.Marshal(lane)
	return `{"stream_id":` + string(encoded) + `,` + strings.TrimPrefix(event, "{")
}

func assertSessionJSON(t *testing.T, got, want string) {
	t.Helper()
	var actual, expected any
	a := json.NewDecoder(bytes.NewBufferString(got))
	a.UseNumber()
	b := json.NewDecoder(bytes.NewBufferString(want))
	b.UseNumber()
	if a.Decode(&actual) != nil || b.Decode(&expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("JSON = %s; want %s", got, want)
	}
}
