package codex_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/codex"
)

func TestStreamPreservesOrderAndAssemblesCompletedItems(t *testing.T) {
	events := []string{
		`{"type":"response.created","response":{"id":"r","usage":{"input_tokens":2}}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_b","delta":"{\"b\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_a","delta":"{\"a\":1}"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_b","delta":"2}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_b","name":"b","call_id":"c_b","arguments":"{\"b\":-1}"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs","summary":[],"encrypted_content":"opaque"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_a","name":"a","call_id":"c_a","arguments":"{\"a\":1}"}}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_b","name":"b","call_id":"c_b","arguments":"{\"b\":2}"}}`,
		`{"type":"response.completed","sequence_number":7,"response":{"id":"r","status":"completed","output":[],"usage":{"output_tokens":0,"total_tokens":2}}}`,
	}
	wire := ": keepalive\r\n\r\n"
	for _, event := range events {
		wire += "data: " + event + "\r\n\r\n"
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		io.WriteString(w, wire)
	}))
	defer server.Close()
	client := testClient(t, server.Client(), server.URL)
	r := request(t, `{"model":"m","input":"hi","stream":true}`)
	stream, err := client.Stream(t.Context(), testAccount(t, "one"), r)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	for i := range len(events) - 1 {
		event, err := stream.Next()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if string(event) != events[i] {
			t.Errorf("event %d changed: %s", i, event)
		}
	}
	terminal, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("terminal end: %v", err)
	}
	result := stream.Result()
	ordinary, err := client.Generate(t.Context(), testAccount(t, "one"), r)

	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"r","status":"completed","output":[{"type":"reasoning","id":"rs","summary":[],"encrypted_content":"opaque"},{"type":"function_call","id":"fc_a","name":"a","call_id":"c_a","arguments":"{\"a\":1}"},{"type":"function_call","id":"fc_b","name":"b","call_id":"c_b","arguments":"{\"b\":2}"}],"usage":{"output_tokens":0,"total_tokens":2}}`
	assertJSON(t, result.Response, want)
	assertJSON(t, ordinary.Response, want)
	var final struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(terminal, &final); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, final.Response, want)
	if !result.Usage.Input.Known || result.Usage.Input.Tokens != 2 || !result.Usage.Output.Known || result.Usage.Output.Tokens != 0 || result.Usage.Total.Tokens != 2 {
		t.Errorf("usage not retained: %+v", result.Usage)
	}
	result.Response[0] = 'x'
	assertJSON(t, stream.Result().Response, want)
}

func TestTerminalOutputIsAuthoritativeAndIncompleteIsDistinct(t *testing.T) {
	for _, status := range []string{"completed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			wire := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"old\"}]}}\n\n"
			final := `{"id":"r","status":"` + status + `","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"cannot"}]}],"usage":null}`
			wire += "data: {\"type\":\"response." + status + "\",\"response\":" + final + "}\n\n"
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return httpResponse(200, "text/event-stream", wire), nil })}, "https://example.test")

			result, err := client.Generate(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

			if err != nil {
				t.Fatalf("terminal %s is not an attempt error: %v", status, err)
			}
			assertJSON(t, result.Response, final)
			if result.Usage.Input.Known || result.Usage.Output.Known || result.Usage.Total.Known {
				t.Errorf("unknown usage became known: %+v", result.Usage)
			}
		})
	}
}

func TestFailedTerminalKeepsUsageAndSanitizesError(t *testing.T) {
	for _, tt := range []struct {
		code     string
		category codex.Category
	}{
		{code: "rate_limit_exceeded", category: codex.ProviderLimit},
		{code: "context_length_exceeded", category: codex.InvalidRequest},
		{code: "usage_not_included", category: codex.AccountProblem},
		{code: "future_code", category: codex.ProviderFailure},
	} {
		t.Run(tt.code, func(t *testing.T) {
			wire := "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"r\",\"status\":\"failed\",\"error\":{\"code\":\"" + tt.code + "\",\"message\":\"secret provider text\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n"
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return httpResponse(200, "text/event-stream", wire), nil })}, "https://example.test")
			stream, err := client.Stream(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()

			event, err := stream.Next()
			if err != nil {
				t.Fatal(err)
			}
			_, err = stream.Next()
			result := stream.Result()

			var failure *codex.Failure
			if !errors.As(err, &failure) || failure.Category != tt.category || failure.HTTPStatus != 200 || failure.SafeToRetry {
				t.Fatalf("wrong failure facts: %v", err)
			}
			if strings.Contains(string(event), "secret") || strings.Contains(string(result.Response), "secret") || strings.Contains(err.Error(), "secret") {
				t.Error("provider error leaked")
			}
			if !result.Usage.Input.Known || result.Usage.Input.Tokens != 3 || !result.Usage.Output.Known || result.Usage.Total.Known {
				t.Errorf("failure usage: %+v", result.Usage)
			}
		})
	}
}

func TestErrorEventUsesSafeResponsesShape(t *testing.T) {
	for _, detail := range []string{
		`"code":"rate_limit_exceeded","message":"secret","param":"secret"`,
		`"error":{"code":"rate_limit_exceeded","message":"secret","param":"secret"}`,
	} {
		t.Run(detail, func(t *testing.T) {
			wire := "data: {\"type\":\"error\",\"sequence_number\":7," + detail + "}\n\n"
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return httpResponse(200, "text/event-stream", wire), nil })}, "https://example.test")
			stream, err := client.Stream(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()

			event, err := stream.Next()

			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, event, `{"type":"error","sequence_number":7,"code":"provider_limit","message":"codex: provider_limit","param":null}`)
			if _, err = stream.Next(); err == nil {
				t.Error("failed event ended successfully")
			}
		})
	}
}

func TestStreamRejectsCorruptionAndPrematureEOF(t *testing.T) {
	tests := []struct{ name, wire string }{
		{name: "empty"},
		{name: "truncated JSON", wire: "data: {\"type\":\"response.completed\"\n\n"},
		{name: "done without terminal", wire: "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\"}}\n\n"},
		{name: "done marker without terminal", wire: "data: [DONE]\n\n"},
		{name: "missing response", wire: "data: {\"type\":\"response.completed\"}\n\n"},
		{name: "wrong status", wire: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\"}}\n\n"},
		{name: "invalid output", wire: "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":{}}}\n\n"},
		{name: "event type mismatch", wire: "event: response.completed\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"},
		{name: "missing output index", wire: "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\"}}\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return httpResponse(200, "text/event-stream", tt.wire), nil
			})}, "https://example.test")

			result, err := client.Generate(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

			var failure *codex.Failure
			if !errors.As(err, &failure) || failure.Category != codex.InvalidResponse || failure.SafeToRetry {
				t.Errorf("corrupt stream returned %v", err)
			}
			if len(result.Response) != 0 {
				t.Error("corrupt stream became successful response")
			}
		})
	}
}

func TestMalformedUsageRetainsEveryValidCounter(t *testing.T) {
	wire := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"usage\":{\"input_tokens\":-1,\"output_tokens\":0,\"total_tokens\":9}}}\n\n"
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return httpResponse(200, "text/event-stream", wire), nil })}, "https://example.test")

	result, err := client.Generate(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

	if err == nil || result.Usage.Input.Known || !result.Usage.Output.Known || result.Usage.Total.Tokens != 9 {
		t.Errorf("usage after corruption: %+v, %v", result.Usage, err)
	}
}

func TestStreamBoundsAccumulatedItems(t *testing.T) {
	item := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + strings.Repeat("x", 9<<20) + `"}]}`
	wire := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + item + "}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":" + item + "}\n\n" + completeSSE
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return httpResponse(200, "text/event-stream", wire), nil })}, "https://example.test")

	_, err := client.Generate(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

	var failure *codex.Failure
	if !errors.As(err, &failure) || failure.Category != codex.InvalidResponse {
		t.Fatalf("unbounded items accepted: %v", err)
	}
}
