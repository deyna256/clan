package openai_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
)

func TestGenerateInterpreterResults(t *testing.T) {
	for _, tc := range []struct {
		name, code, outputs string
		wantCode            generation.Optional[string]
		wantOutputs         generation.Optional[[]generation.InterpreterOutput]
	}{
		{"logs and image", `"print(1)"`, `[{"type":"logs","logs":""},{"type":"image","url":"https://example.com/plot.png"}]`, generation.Some("print(1)"), generation.Some([]generation.InterpreterOutput{generation.InterpreterLogs{}, generation.InterpreterImage{URL: "https://example.com/plot.png"}})},
		{"unavailable", `null`, `null`, generation.Null[string](), generation.Null[[]generation.InterpreterOutput]()},
		{"empty", `""`, `[]`, generation.Some(""), generation.Some([]generation.InterpreterOutput{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, staticJSON(responseWithItems(interpreterCallJSON(tc.code, tc.outputs))), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			if err != nil {
				t.Fatal(err)
			}
			want := []generation.Item{generation.OpenAICodeInterpreterCall{ID: "ci_1", ContainerID: "cntr_1", Status: "completed", Code: tc.wantCode, Outputs: tc.wantOutputs}}
			if !reflect.DeepEqual(result.Response.Output, want) || result.Response.Finish.Reason != "stop" {
				t.Fatalf("response = %#v; want output %#v and stop", result.Response, want)
			}
		})
	}
}

func TestStreamInterpreterInterleavesAndRecoversCode(t *testing.T) {
	var logs bytes.Buffer
	call := interpreterCallJSON(`"print(1)"`, `[{"type":"logs","logs":"1"}]`)
	function := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"save","arguments":"{}"}`
	client := streamClient(t, &logs, created(), interpreterAdded(),
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"save","arguments":""}}`,
		`{"type":"response.code_interpreter_call_code.delta","item_id":"ci_1","output_index":0,"delta":"print("}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{}"}`,
		`{"type":"response.code_interpreter_call_code.done","item_id":"ci_1","output_index":0,"code":"print(1)"}`,
		`{"type":"response.code_interpreter_call.interpreting","item_id":"ci_1","output_index":0}`,
		`{"type":"response.code_interpreter_call.completed","item_id":"ci_1","output_index":0}`,
		`{"type":"response.output_item.done","output_index":0,"item":`+call+`}`,
		`{"type":"response.completed","response":`+responseWithItems(call+","+function)+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.OpenAICodeInterpreterCall{ID: "ci_1", ContainerID: "cntr_1", Status: "in_progress"}},
		generation.ItemStarted{Index: 1, Item: generation.ToolCall{ID: "fc_1", CallID: "call_1", Name: "save"}},
		generation.CodeDelta{ItemIndex: 0, Fragment: "print("},
		generation.ArgumentsDelta{ItemIndex: 1, Fragment: "{}"},
		generation.CodeDelta{ItemIndex: 0, Fragment: "1)"},
		generation.ItemEnded{Index: 0, Item: generation.OpenAICodeInterpreterCall{ID: "ci_1", ContainerID: "cntr_1", Status: "completed", Code: generation.Some("print(1)"), Outputs: generation.Some([]generation.InterpreterOutput{generation.InterpreterLogs{Logs: "1"}})}},
		generation.ItemEnded{Index: 1, Item: generation.ToolCall{ID: "fc_1", CallID: "call_1", Name: "save", Arguments: "{}"}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "tool_calls"}},
	}
	if !reflect.DeepEqual(events, want) || logs.Len() != 0 {
		t.Fatalf("events = %#v; want %#v; logs = %s", events, want, logs.String())
	}
}

func TestStreamInterpreterFinalOnly(t *testing.T) {
	client := streamClient(t, io.Discard, `{"type":"response.completed","response":`+responseWithItems(interpreterCallJSON(`"pass"`, `[]`))+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.OpenAICodeInterpreterCall{ID: "ci_1", ContainerID: "cntr_1", Status: "completed"}},
		generation.CodeDelta{ItemIndex: 0, Fragment: "pass"},
		generation.ItemEnded{Index: 0, Item: generation.OpenAICodeInterpreterCall{ID: "ci_1", ContainerID: "cntr_1", Status: "completed", Code: generation.Some("pass"), Outputs: generation.Some([]generation.InterpreterOutput{})}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "stop"}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v; want %#v", events, want)
	}
}

func TestStreamInterpreterRetainsKnownCode(t *testing.T) {
	for _, call := range []string{
		interpreterCallJSON(`null`, `null`),
		`{"id":"ci_1","type":"code_interpreter_call","container_id":"cntr_1","status":"completed","outputs":null}`,
	} {
		t.Run(call, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), interpreterAdded(),
				`{"type":"response.code_interpreter_call_code.delta","item_id":"ci_1","output_index":0,"delta":"pass"}`,
				`{"type":"response.completed","response":`+responseWithItems(call)+`}`,
			)

			events, err := readStream(t, client)

			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			end := events[len(events)-2].(generation.ItemEnded).Item.(generation.OpenAICodeInterpreterCall)
			if code, known := end.Code.Value(); !known || code != "pass" {
				t.Fatalf("final code = %q, known=%v", code, known)
			}
		})
	}
}

func TestGenerateRequiresTerminalInterpreterContainer(t *testing.T) {
	for _, status := range []string{"completed", "incomplete"} {
		t.Run(status, func(t *testing.T) {
			body := `{"id":"resp_1","model":"test-model","status":"` + status + `","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"ci_1","type":"code_interpreter_call","status":"incomplete","code":null,"outputs":null}],"usage":{"output_tokens":8}}`
			client := testClient(t, staticJSON(body), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want protocol error", err)
			}
			if result.Usage.Output != count(8) {
				t.Fatalf("usage = %#v; want 8 output tokens", result.Usage)
			}
		})
	}
}

func TestStreamInterpreterAcceptsEmptyCodeDone(t *testing.T) {
	client := streamClient(t, io.Discard, created(), interpreterAdded(),
		`{"type":"response.code_interpreter_call_code.done","item_id":"ci_1","output_index":0,"code":""}`,
		`{"type":"response.completed","response":`+responseWithItems(interpreterCallJSON(`null`, `null`))+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	end := events[len(events)-2].(generation.ItemEnded).Item.(generation.OpenAICodeInterpreterCall)
	if code, known := end.Code.Value(); !known || code != "" {
		t.Fatalf("final code = %q, known=%v", code, known)
	}
	for _, event := range events {
		if _, ok := event.(generation.CodeDelta); ok {
			t.Fatalf("empty code emitted delta: %#v", event)
		}
	}
}

func TestStreamInterpreterConflictPreservesUsage(t *testing.T) {
	for _, tc := range []struct{ name, call string }{
		{"code", interpreterCallJSON(`"different"`, `null`)},
		{"container", strings.Replace(interpreterCallJSON(`"pass"`, `null`), "cntr_1", "cntr_other", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), interpreterAdded(),
				`{"type":"response.code_interpreter_call_code.delta","item_id":"ci_1","output_index":0,"delta":"pass"}`,
				`{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":[`+tc.call+`],"usage":{"output_tokens":8}}}`,
			)

			events, err := readStream(t, client)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want protocol error", err)
			}
			last, ok := events[len(events)-1].(generation.UsageUpdated)
			if !ok || last.Usage.Output != count(8) {
				t.Fatalf("last = %#v; want known usage", events[len(events)-1])
			}
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamInterpreterOutputSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        generation.Optional[[]generation.InterpreterOutput]
	}{
		{"omitted", "", generation.Some([]generation.InterpreterOutput{generation.InterpreterLogs{Logs: "1"}, generation.InterpreterImage{URL: "https://example.com/plot.png"}})},
		{"unavailable", `,"outputs":null`, generation.Null[[]generation.InterpreterOutput]()},
		{"empty", `,"outputs":[]`, generation.Some([]generation.InterpreterOutput{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := interpreterCallJSON(`"pass"`, `[{"type":"logs","logs":"1"},{"type":"image","url":"https://example.com/plot.png"}]`)
			final := `{"id":"ci_1","type":"code_interpreter_call","container_id":"cntr_1","status":"completed","code":"pass"` + tc.field + `}`
			client := streamClient(t, io.Discard, created(),
				`{"type":"response.output_item.done","output_index":0,"item":`+call+`}`,
				`{"type":"response.completed","response":`+responseWithItems(final)+`}`,
			)

			events, err := readStream(t, client)

			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			end := events[len(events)-2].(generation.ItemEnded).Item.(generation.OpenAICodeInterpreterCall)
			if !reflect.DeepEqual(end.Outputs, tc.want) {
				t.Fatalf("outputs = %#v; want %#v", end.Outputs, tc.want)
			}
		})
	}
}

func TestStreamInterpreterRejectsMalformedCodeEvents(t *testing.T) {
	for _, event := range []string{
		`{"type":"response.code_interpreter_call_code.done","output_index":0}`,
		`{"type":"response.code_interpreter_call_code.done","output_index":0,"code":null}`,
		`{"type":"response.code_interpreter_call_code.delta","output_index":0,"delta":null}`,
		`{"type":"response.code_interpreter_call_code.delta","output_index":0,"item_id":"other","delta":"pass"}`,
		`{"type":"response.code_interpreter_call_code.delta","output_index":1,"delta":"pass"}`,
	} {
		t.Run(event, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), interpreterAdded(), event)

			events, err := readStream(t, client)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want protocol error", err)
			}
			assertNoResponseEnd(t, events)
		})
	}
}

func TestGenerateSkipsMalformedOptionalInterpreterOutput(t *testing.T) {
	var logs bytes.Buffer
	call := interpreterCallJSON(`"pass"`, `[null,{"type":"future","secret":"private"},{"type":"logs","logs":null},{"type":"logs","logs":"valid"}]`)
	client := testClient(t, staticJSON(responseWithItems(call)), &logs)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	if err != nil {
		t.Fatal(err)
	}
	outputs, _ := result.Response.Output[0].(generation.OpenAICodeInterpreterCall).Outputs.Value()
	want := []generation.InterpreterOutput{generation.InterpreterLogs{Logs: "valid"}}
	if !reflect.DeepEqual(outputs, want) {
		t.Fatalf("outputs = %#v; want %#v", outputs, want)
	}
	if !strings.Contains(logs.String(), "invalid_interpreter_outputs") || strings.Contains(logs.String(), "private") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestStreamInterpreterBoundsRetainedCodeAndOutputs(t *testing.T) {
	large := strings.Repeat("x", 600_000)
	for _, tc := range []struct{ name, second string }{
		{"code", fmt.Sprintf(`{"type":"response.code_interpreter_call_code.delta","output_index":0,"delta":%q}`, large)},
		{"output", `{"type":"response.output_item.done","output_index":0,"item":` + interpreterCallJSON(`null`, fmt.Sprintf(`[{"type":"logs","logs":%q}]`, large)) + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), interpreterAdded(),
				fmt.Sprintf(`{"type":"response.code_interpreter_call_code.delta","output_index":0,"delta":%q}`, large), tc.second)

			events, err := readStream(t, client)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want cumulative size rejection", err)
			}
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamErrorCodeStillClassifiesProviderFailure(t *testing.T) {
	client := streamClient(t, io.Discard, created(), `{"type":"error","code":"rate_limit_exceeded"}`)

	_, err := readStream(t, client)

	var failure *generation.Failure
	if !errors.As(err, &failure) || failure.Kind != generation.RateLimited {
		t.Fatalf("error = %v; want rate limited", err)
	}
}

func interpreterAdded() string {
	return `{"type":"response.output_item.added","output_index":0,"item":{"id":"ci_1","type":"code_interpreter_call","container_id":"cntr_1","status":"in_progress","code":null,"outputs":null}}`
}
func interpreterCallJSON(code, outputs string) string {
	return `{"id":"ci_1","type":"code_interpreter_call","container_id":"cntr_1","status":"completed","code":` + code + `,"outputs":` + outputs + `}`
}
