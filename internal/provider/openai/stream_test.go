package openai_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
)

func TestStreamRecoversTextAndReportsUnknownEventsOnce(t *testing.T) {
	var logs bytes.Buffer
	client := streamClient(t, &logs,
		created(), messageAdded(),
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"Hello"}`,
		`{"type":"response.new_progress","payload":"private-content"}`,
		`{"type":"response.new_progress"}`,
		`{"type":"response.completed","response":`+textResponse("Hello world", `{"input_tokens":4,"output_tokens":2}`)+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var fragments []string
	var ended generation.Message
	for _, event := range events {
		switch e := event.(type) {
		case generation.TextDelta:
			fragments = append(fragments, e.Text)
		case generation.ItemEnded:
			ended = e.Item.(generation.Message)
		}
	}
	if !reflect.DeepEqual(fragments, []string{"Hello", " world"}) || !reflect.DeepEqual(ended.Parts, []generation.Part{generation.Text{Text: "Hello world"}}) {
		t.Fatalf("fragments = %q; final parts = %#v", fragments, ended.Parts)
	}
	if _, ok := events[len(events)-1].(generation.ResponseEnded); !ok {
		t.Fatalf("last event = %#v", events[len(events)-1])
	}
	for _, want := range []string{`"reason":"unknown_event"`, `"count":2`, `"request_id":"request-1"`, `"attempt_id":"attempt-1"`, `"upstream_id":"upstream-1"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs do not contain %s: %s", want, logs.String())
		}
	}
	if strings.Count(logs.String(), `"level":"WARN"`) != 2 || strings.Contains(logs.String(), "private-content") || strings.Contains(logs.String(), "secret-key") {
		t.Fatalf("unexpected logs: %s", logs.String())
	}
}

func TestStreamKeepsDeliveredTextOnSnapshotConflict(t *testing.T) {
	var logs bytes.Buffer
	client := streamClient(t, &logs, created(), messageAdded(),
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Delivered"}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"Changed"}`,
		`{"type":"response.completed","response":`+textResponse("Changed", "null")+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var text string
	for _, event := range events {
		if delta, ok := event.(generation.TextDelta); ok {
			text += delta.Text
		}
		if end, ok := event.(generation.PartEnded); ok && end.Part.(generation.Text).Text != "Delivered" {
			t.Fatalf("end snapshot contradicted delivered text: %#v", end)
		}
	}
	if text != "Delivered" || !strings.Contains(logs.String(), `"reason":"text_snapshot_conflict"`) {
		t.Fatalf("text = %q; logs = %s", text, logs.String())
	}
}

func TestStreamInterleavedToolArguments(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"first","arguments":""}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"second","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"a\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_2","delta":"{\"b\":2}"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"1}"}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":[
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"first","arguments":"{\"a\":1}","status":"completed"},
		{"type":"function_call","id":"fc_2","call_id":"call_2","name":"second","arguments":"{\"b\":2}","status":"completed"}]}}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var deltas []generation.ArgumentsDelta
	var calls []generation.ToolCall
	for _, event := range events {
		switch e := event.(type) {
		case generation.ArgumentsDelta:
			deltas = append(deltas, e)
		case generation.ItemEnded:
			calls = append(calls, e.Item.(generation.ToolCall))
		}
	}
	want := []generation.ArgumentsDelta{{ItemIndex: 0, Fragment: `{"a":`}, {ItemIndex: 1, Fragment: `{"b":2}`}, {ItemIndex: 0, Fragment: `1}`}}
	if !reflect.DeepEqual(deltas, want) || len(calls) != 2 || calls[0].Arguments != `{"a":1}` || calls[1].Arguments != `{"b":2}` || calls[0].CallID != "call_1" || calls[1].CallID != "call_2" {
		t.Fatalf("deltas = %#v; calls = %#v", deltas, calls)
	}
}

func TestStreamPreservesFailureUsageWithoutSuccessfulEnd(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		`{"type":"response.failed","response":{"id":"resp_1","model":"test-model","status":"failed","error":{"code":"server_error"},"usage":{"input_tokens":7,"output_tokens":2}}}`,
	)

	events, err := readStream(t, client)

	var failure *generation.Failure
	if !errors.As(err, &failure) || failure.Kind != generation.Unavailable {
		t.Fatalf("error = %v", err)
	}
	last, ok := events[len(events)-1].(generation.UsageUpdated)
	if !ok || last.Usage.Input != count(7) || last.Usage.Output != count(2) {
		t.Fatalf("last event = %#v; want failure usage", events[len(events)-1])
	}
	assertNoResponseEnd(t, events)
}

func TestStreamWaitsForToolIdentity(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"city\":\"Paris\"}"}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Paris\"}"}]}}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.ToolCall{ID: "fc_1", CallID: "call_1", Name: "weather"}},
		generation.ArgumentsDelta{ItemIndex: 0, Fragment: `{"city":"Paris"}`},
		generation.ItemEnded{Index: 0, Item: generation.ToolCall{ID: "fc_1", CallID: "call_1", Name: "weather", Arguments: `{"city":"Paris"}`}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "tool_calls"}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v; want %#v", events, want)
	}
}

func TestStreamPreservesLateReasoningData(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"Considering"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Considering options"}],"encrypted_content":"opaque-secret"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Considering options"}]}]}}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := generation.Reasoning{ID: "rs_1", Parts: []generation.Part{generation.ReasoningSummary{Text: "Considering options"}}, OpenAI: generation.OpenAIReasoningData{EncryptedContent: generation.Some("opaque-secret")}}
	var got generation.Item
	var text string
	for _, event := range events {
		switch e := event.(type) {
		case generation.ItemEnded:
			got = e.Item
		case generation.TextDelta:
			text += e.Text
		}
	}
	if !reflect.DeepEqual(got, want) || text != "Considering options" {
		t.Fatalf("ended = %#v; text = %q", got, text)
	}
}

func TestStreamRejectsConflictingOrMissingCalls(t *testing.T) {
	for _, tc := range []struct{ name, final string }{
		{"arguments", `[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Rome\"}"}]`},
		{"identity", `[{"type":"function_call","id":"fc_1","call_id":"other","name":"weather","arguments":"{\"city\":\"Paris\"}"}]`},
		{"missing", `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(),
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"city\":\"Paris\"}"}`,
				`{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":`+tc.final+`,"usage":{"output_tokens":8}}}`,
			)

			events, err := readStream(t, client)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want protocol error", err)
			}
			assertNoResponseEnd(t, events)
			last, ok := events[len(events)-1].(generation.UsageUpdated)
			if !ok || last.Usage.Output != count(8) {
				t.Fatalf("last event = %#v; want known usage", events[len(events)-1])
			}
		})
	}
}

func TestStreamRejectsCompletedCallWithInvalidJSONArguments(t *testing.T) {
	for _, arguments := range []string{"{", "", "true false"} {
		t.Run(arguments, func(t *testing.T) {
			client := streamClient(t, io.Discard,
				`{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":"`+arguments+`"}]}}`,
			)

			events, err := readStream(t, client)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want protocol error", err)
			}
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamIgnoresRepeatedSequenceWithoutDuplicatingArguments(t *testing.T) {
	delta := `{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"delta":"{}"}`
	client := streamClient(t, io.Discard, created(),
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":""}}`,
		delta, delta,
		`{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":"{}"}]}}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var arguments string
	for _, event := range events {
		if delta, ok := event.(generation.ArgumentsDelta); ok {
			arguments += delta.Fragment
		}
	}
	if arguments != "{}" {
		t.Fatalf("arguments = %q; want {}", arguments)
	}
}

func TestStreamCancellationStopsBufferedContent(t *testing.T) {
	client := streamClient(t, io.Discard, created(), messageAdded(),
		`{"type":"response.completed","response":`+textResponse("unused", "null")+`}`,
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := client.GenerateStream(ctx, testAttempt(), textRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}

	cancel()
	event, err := stream.Next()

	if event != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Next after cancellation = %#v, %v", event, err)
	}
}

func TestStreamCancellationPreservesBufferedUsage(t *testing.T) {
	client := streamClient(t, io.Discard,
		`{"type":"response.created","response":{"id":"resp_1","model":"test-model","status":"in_progress","usage":{"input_tokens":7}}}`,
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := client.GenerateStream(ctx, testAttempt(), textRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}

	cancel()
	event, err := stream.Next()

	usage, ok := event.(generation.UsageUpdated)
	if err != nil || !ok || usage.Usage.Input != count(7) {
		t.Fatalf("Next = %#v, %v; want retained usage", event, err)
	}
	event, err = stream.Next()
	if event != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Next after usage = %#v, %v; want cancellation", event, err)
	}
}

func TestStreamCloseUnblocksBodyRead(t *testing.T) {
	body := &blockedBody{reading: make(chan struct{}), closed: make(chan struct{})}
	client := clientWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
	}))
	stream, err := client.GenerateStream(t.Context(), testAttempt(), textRequest())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	finished := make(chan error, 1)
	go func() { _, err := stream.Next(); finished <- err }()
	select {
	case <-body.reading:
	case <-time.After(5 * time.Second):
		t.Fatal("Next did not enter body read")
	}

	err = stream.Close()

	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Next = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not unblock body read")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamBoundsRetainedReasoningData(t *testing.T) {
	encrypted := strings.Repeat("a", 600_000)
	body := frame(created())
	for index, id := range []string{"rs_1", "rs_2"} {
		body += frame(fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":{"type":"reasoning","id":%q,"summary":[],"encrypted_content":%q}}`, index, id, encrypted))
	}
	client := clientWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))

	events, err := readStream(t, client)

	var failure *generation.Failure
	if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
		t.Fatalf("error = %v; want cumulative size rejection before EOF", err)
	}
	assertNoResponseEnd(t, events)
}

type blockedBody struct {
	reading, closed chan struct{}
	closeOnce       sync.Once
}

func (b *blockedBody) Read([]byte) (int, error) {
	close(b.reading)
	<-b.closed
	return 0, io.ErrClosedPipe
}
func (b *blockedBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestStreamRetainsIncompleteArguments(t *testing.T) {
	client := streamClient(t, io.Discard,
		`{"type":"response.incomplete","response":{"id":"resp_1","model":"test-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"weather","arguments":"{\"city\":","status":"incomplete"}]}}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	end, ok := events[len(events)-1].(generation.ResponseEnded)
	if !ok || end.Finish != (generation.Finish{Status: "incomplete", Reason: "max_output_tokens"}) {
		t.Fatalf("last event = %#v", events[len(events)-1])
	}
	call := events[len(events)-2].(generation.ItemEnded).Item.(generation.ToolCall)
	if call.Arguments != `{"city":` || call.Status != generation.ItemIncomplete {
		t.Fatalf("call = %#v", call)
	}
}

func TestStreamRequiresProtocolCompletion(t *testing.T) {
	for _, tail := range []string{"", "data: [DONE]\n\n"} {
		t.Run(tail, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, frame(created())+tail)
			}, io.Discard)

			events, err := readStream(t, client)

			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("error = %v; want unexpected EOF", err)
			}
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamCloseInterruptsRead(t *testing.T) {
	stopped := make(chan struct{})
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, frame(created()))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	}, io.Discard)
	stream, err := client.GenerateStream(t.Context(), testAttempt(), textRequest())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := stream.Next()
		finished <- err
	}()

	closeErr := stream.Close()
	againErr := stream.Close()

	if closeErr != nil || againErr != nil {
		t.Fatalf("Close = %v then %v", closeErr, againErr)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Next = %v; want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not interrupt Next")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("provider request was not cancelled")
	}
}

func streamClient(t *testing.T, logs io.Writer, events ...string) *openai.Client {
	t.Helper()
	return testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			_, _ = io.WriteString(w, frame(event))
		}
	}, logs)
}

func frame(event string) string { return "data: " + strings.ReplaceAll(event, "\n", "") + "\n\n" }
func created() string {
	return `{"type":"response.created","response":{"id":"resp_1","model":"test-model","status":"in_progress","output":[]}}`
}
func messageAdded() string {
	return `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[]}}`
}

func readStream(t *testing.T, client *openai.Client) ([]generation.Event, error) {
	t.Helper()
	stream, err := client.GenerateStream(t.Context(), testAttempt(), textRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var events []generation.Event
	for {
		event, err := stream.Next()
		if err != nil {
			if event != nil {
				t.Fatalf("Next returned event %#v together with error %v", event, err)
			}
			again, nextErr := stream.Next()
			if again != nil || !errors.Is(nextErr, err) {
				t.Fatalf("terminal result changed: %#v, %v then %#v, %v", event, err, again, nextErr)
			}
			return events, err
		}
		events = append(events, event)
	}
}

func assertNoResponseEnd(t *testing.T, events []generation.Event) {
	t.Helper()
	for _, event := range events {
		if _, ok := event.(generation.ResponseEnded); ok {
			t.Fatal("failed stream emitted ResponseEnded")
		}
	}
}

func outputItemAdded(index int, item string) string {
	return fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":%s}`, index, item)
}

func responseWithItems(items string) string {
	return `{"id":"resp_1","model":"test-model","status":"completed","output":[` + items + `]}`
}

func assertProtocolError(t *testing.T, err error) {
	t.Helper()
	var failure *generation.Failure
	if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
		t.Fatalf("error = %v; want protocol error", err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("error leaked input")
	}
}
