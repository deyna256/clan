package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/codex"
)

func TestStreamEventIdleTimeout(t *testing.T) {
	for _, tt := range []struct{ name, progress string }{
		{name: "first event"},
		{name: "comments", progress: ": keepalive\n\n"},
		{name: "partial event", progress: "data: "},
		{name: "empty event", progress: "data:\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reader, writer := io.Pipe()
				defer writer.Close()
				client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: reader}, nil
				})}, "https://example.test")
				stream, err := client.Stream(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
				if err != nil {
					t.Fatal(err)
				}
				if tt.progress != "" {
					done := make(chan struct{})
					defer func() { <-done }()
					go func() {
						defer close(done)
						for {
							time.Sleep(time.Minute)
							if _, err := io.WriteString(writer, tt.progress); err != nil {
								return
							}
						}
					}()
				}
				defer stream.Close()
				started := time.Now()

				_, err = stream.Next()

				var failure *codex.Failure
				if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &failure) || failure.Category != codex.TransportFailure || failure.SafeToRetry {
					t.Fatalf("idle error = %v, want deadline without safe retry", err)
				}
				if time.Since(started) != 5*time.Minute {
					t.Errorf("idle timeout after %v, want5m", time.Since(started))
				}
			})
		})
	}
}

func TestStreamIdleIgnoresConsumerPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer writer.Close()
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: reader}, nil
		})}, "https://example.test")
		stream, err := client.Stream(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		go func() {
			io.WriteString(writer, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"usage\":{\"input_tokens\":3}}}\n\n")
		}()

		if _, err := stream.Next(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Minute)
		started := time.Now()
		_, err = stream.Next()

		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 5*time.Minute {
			t.Fatalf("next event error = %v after %v, want deadline after5m", err, time.Since(started))
		}
		if result := stream.Result(); !result.Usage.Input.Known || result.Usage.Input.Tokens != 3 {
			t.Errorf("usage lost on idle timeout: %+v", result.Usage)
		}
	})
}

func TestGenerateHasNoDefaultTotalTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer writer.Close()
		client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: reader}, nil
		})}, "https://example.test")
		go func() {
			for range 3 {
				time.Sleep(4 * time.Minute)
				if _, err := io.WriteString(writer, "data: {\"type\":\"response.in_progress\"}\n\n"); err != nil {
					return
				}
			}
			io.WriteString(writer, completeSSE)
		}()
		started := time.Now()

		result, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

		if err != nil || time.Since(started) != 12*time.Minute || len(result.Response) == 0 {
			t.Fatalf("Generate = %+v, %v after %v, want complete response after12m", result, err, time.Since(started))
		}
	})
}

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
	ordinary, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), r)

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

			result, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

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

func TestTerminalDoesNotHTMLEscapeContent(t *testing.T) {
	item := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"<tag>&value</tag>"}]}`
	want := `{"id":"r","status":"completed","output":[` + item + `]}`
	for _, tt := range []struct{ name, output string }{
		{name: "completed items", output: `[]`},
		{name: "terminal output", output: `[` + item + `]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire := `data: {"type":"response.output_item.done","output_index":0,"item":` + item + "}\n\n" +
				`data: {"type":"response.completed","response":{"id":"r","output":` + tt.output + "}}\n\n"
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return httpResponse(200, "text/event-stream", wire), nil
			})}, "https://example.test")
			stream, err := client.Stream(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if _, err := stream.Next(); err != nil {
				t.Fatal(err)
			}

			event, err := stream.Next()

			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, event, `{"type":"response.completed","response":`+want+`}`)
			assertJSON(t, stream.Result().Response, want)
			if !strings.Contains(string(event), "<tag>&value</tag>") || !strings.Contains(string(stream.Result().Response), "<tag>&value</tag>") {
				t.Errorf("terminal content was HTML escaped: %s", event)
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

func TestErrorEventOmitsInvalidSequenceNumbers(t *testing.T) {
	for _, tt := range []struct{ name, sequence, want string }{
		{name: "zero", sequence: `0`, want: `0`},
		{name: "large integer", sequence: `9223372036854775807`, want: `9223372036854775807`},
		{name: "null", sequence: `null`},
		{name: "negative", sequence: `-1`},
		{name: "fraction", sequence: `1.5`},
		{name: "overflow", sequence: `9223372036854775808`},
		{name: "string", sequence: `"secret"`},
		{name: "object", sequence: `{"message":"secret"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire := `data: {"type":"error","code":"rate_limit_exceeded","message":"secret","sequence_number":` + tt.sequence + "}\n\n"
			client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return httpResponse(200, "text/event-stream", wire), nil
			})}, "https://example.test")
			stream, err := client.Stream(t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()

			event, err := stream.Next()

			if err != nil {
				t.Fatal(err)
			}
			var fields struct {
				Sequence json.RawMessage `json:"sequence_number"`
			}
			if err := json.Unmarshal(event, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields.Sequence) != tt.want {
				t.Errorf("sequence_number = %s, want %q", fields.Sequence, tt.want)
			}
			want := `{"type":"error","code":"provider_limit","message":"codex: provider_limit","param":null`
			if tt.want != "" {
				want += `,"sequence_number":` + tt.want
			}
			assertJSON(t, event, want+`}`)
			_, err = stream.Next()
			var failure *codex.Failure
			if !errors.As(err, &failure) || failure.Category != codex.ProviderLimit || failure.SafeToRetry {
				t.Fatalf("terminal failure = %v, want provider limit without safe replay", err)
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

			result, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

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

	result, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

	if err == nil || result.Usage.Input.Known || !result.Usage.Output.Known || result.Usage.Total.Tokens != 9 {
		t.Errorf("usage after corruption: %+v, %v", result.Usage, err)
	}
}

func TestStreamBoundsAccumulatedItems(t *testing.T) {
	item := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + strings.Repeat("x", 9<<20) + `"}]}`
	wire := "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + item + "}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":" + item + "}\n\n" + completeSSE
	client := testClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return httpResponse(200, "text/event-stream", wire), nil })}, "https://example.test")

	_, err := consumeClientStream(client, t.Context(), testAccount(t, "one"), request(t, `{"model":"m","input":"hi"}`))

	var failure *codex.Failure
	if !errors.As(err, &failure) || failure.Category != codex.InvalidResponse {
		t.Fatalf("unbounded items accepted: %v", err)
	}
}
