package openai_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/usage"
	"github.com/stretchr/testify/require"
)

func TestGenerateCustomCall(t *testing.T) {
	for _, input := range []string{"", "print('Привет')\n", "\x00\x1b\a"} {
		t.Run(input, func(t *testing.T) {
			encoded, _ := json.Marshal(input)
			call := `{"type":"custom_tool_call","call_id":"call_1","name":"execute","input":` + string(encoded) + `,"async":false,"namespace":"sandbox","caller":{"type":"program","caller_id":"program_1"}}`
			client := testClient(t, staticJSON(responseWithItems(call)), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			require.NoError(t, err)
			want := []generation.Item{generation.CustomToolCall{CallID: "call_1", Name: "execute", Input: input, OpenAI: generation.OpenAIToolCallData{
				Async: generation.Some(false), Namespace: generation.Some("sandbox"), Caller: generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"}),
			}}}
			require.Equal(t, want, result.Response.Output)
			require.Equal(t, "tool_calls", result.Response.Finish.Reason)
		})
	}
}

func TestGenerateCustomOutputs(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         generation.ToolOutput
	}{
		{name: "text", output: `""`, want: generation.ToolTextOutput("")},
		{name: "empty parts", output: `[]`, want: generation.ToolPartsOutput{}},
		{name: "multimodal", output: `[
			{"type":"input_text","text":"result"},
			{"type":"input_image","image_url":"https://example.com/image.png","detail":"low"},
			{"type":"input_image","file_id":"file_image","detail":"high"},
			{"type":"input_file","file_id":"file_1"},
			{"type":"input_file","file_url":"https://example.com/result.txt","detail":"high"},
			{"type":"input_file","file_data":"YQ==","filename":"result.txt","prompt_cache_breakpoint":{"mode":"explicit"}}
		]`, want: generation.ToolPartsOutput{
			generation.Text{Text: "result"}, generation.ImageURL{URL: "https://example.com/image.png", Detail: "low"}, generation.ImageFile{FileID: "file_image", Detail: "high"},
			generation.FileID{ID: "file_1"}, generation.FileURL{URL: "https://example.com/result.txt", Options: generation.FileOptions{Detail: "high"}},
			generation.FileData{Data: "YQ==", Options: generation.FileOptions{Filename: generation.Some("result.txt"), OpenAI: generation.OpenAIFileOptions{PromptCacheBreakpoint: true}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := responseWithItems(`{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","status":"completed","caller":{"type":"direct"},"output":` + tc.output + `}`)
			client := testClient(t, staticJSON(body), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			require.NoError(t, err)
			want := []generation.Item{generation.CustomToolResult{ID: "out_1", CallID: "call_1", Status: "completed", Output: tc.want, Caller: generation.Some(generation.OpenAIToolCaller{Type: "direct"})}}
			require.Equal(t, want, result.Response.Output)
			require.Equal(t, "stop", result.Response.Finish.Reason)
		})
	}
}

func TestStreamCustomInterleavesAndRetainsLateMetadata(t *testing.T) {
	var logs bytes.Buffer
	finalCustom := customCallJSON("ct_1", "call_1", "execute", "print('Привет')\n")
	function := `{"type":"function_call","id":"fc_1","call_id":"call_2","name":"execute","arguments":"{}"}`
	client := streamClient(t, &logs, created(),
		outputItemAdded(0, customCallJSON("ct_1", "", "", "")),
		outputItemAdded(1, `{"type":"function_call","id":"fc_1","call_id":"call_2","name":"execute","arguments":""}`),
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ct_1","delta":"print("}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{}"}`,
		outputItemAdded(0, customCallJSON("ct_1", "call_1", "execute", "print(")),
		`{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"ct_1","input":"print('Привет')\n"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_1","name":"execute","input":"print('Привет')\n","namespace":"sandbox","status":"completed","async":false,"caller":{"type":"program","caller_id":"program_1"}}}`,
		`{"type":"response.completed","response":`+responseWithItems(finalCustom+","+function)+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 1, Item: generation.ToolCall{ID: "fc_1", CallID: "call_2", Name: "execute"}},
		generation.ArgumentsDelta{ItemIndex: 1, Fragment: "{}"},
		generation.ItemStarted{Index: 0, Item: generation.CustomToolCall{ID: "ct_1", CallID: "call_1", Name: "execute"}},
		generation.ToolInputDelta{ItemIndex: 0, Fragment: "print("},
		generation.ToolInputDelta{ItemIndex: 0, Fragment: "'Привет')\n"},
		generation.ItemEnded{Index: 0, Item: generation.CustomToolCall{ID: "ct_1", CallID: "call_1", Name: "execute", Input: "print('Привет')\n", Status: generation.ItemCompleted, OpenAI: generation.OpenAIToolCallData{
			Async: generation.Some(false), Namespace: generation.Some("sandbox"), Caller: generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"}),
		}}},
		generation.ItemEnded{Index: 1, Item: generation.ToolCall{ID: "fc_1", CallID: "call_2", Name: "execute", Arguments: "{}"}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "tool_calls"}},
	}
	require.Equal(t, want, events)
	require.Equal(t, 0, logs.Len())
}

func TestStreamCustomFinalWithoutID(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(fmt.Sprint(prior), func(t *testing.T) {
			frames := []string{created()}
			id := ""
			if prior {
				id = "ct_1"
				frames = append(frames, outputItemAdded(0, customCallJSON(id, "call_1", "execute", "")))
			}
			frames = append(frames, `{"type":"response.completed","response":`+responseWithItems(`{"type":"custom_tool_call","call_id":"call_1","name":"execute","input":"SELECT 1"}`)+`}`)
			client := streamClient(t, io.Discard, frames...)

			events, err := readStream(t, client)

			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			want := []generation.Event{
				generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
				generation.ItemStarted{Index: 0, Item: generation.CustomToolCall{ID: id, CallID: "call_1", Name: "execute"}},
				generation.ToolInputDelta{ItemIndex: 0, Fragment: "SELECT 1"},
				generation.ItemEnded{Index: 0, Item: generation.CustomToolCall{ID: id, CallID: "call_1", Name: "execute", Input: "SELECT 1"}},
				generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "tool_calls"}},
			}
			require.Equal(t, want, events)
		})
	}
}

func TestStreamCustomIncomplete(t *testing.T) {
	call := customCallJSON("ct_1", "call_1", "execute", "print(")
	body := `{"id":"resp_1","model":"test-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + call + `],"usage":{"output_tokens":3}}`
	client := streamClient(t, io.Discard, `{"type":"response.incomplete","response":`+body+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.UsageUpdated{Usage: usage.Snapshot{Output: count(3)}},
		generation.ItemStarted{Index: 0, Item: generation.CustomToolCall{ID: "ct_1", CallID: "call_1", Name: "execute"}},
		generation.ToolInputDelta{ItemIndex: 0, Fragment: "print("},
		generation.ItemEnded{Index: 0, Item: generation.CustomToolCall{ID: "ct_1", CallID: "call_1", Name: "execute", Input: "print("}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "incomplete", Reason: "max_output_tokens"}},
	}
	require.Equal(t, want, events)
}

func TestCustomProtocolErrorsPreserveUsage(t *testing.T) {
	for _, tc := range []struct{ name, item string }{
		{name: "missing input", item: `{"type":"custom_tool_call","call_id":"call_1","name":"execute"}`},
		{name: "null input", item: `{"type":"custom_tool_call","call_id":"call_1","name":"execute","input":null}`},
		{name: "missing name", item: `{"type":"custom_tool_call","call_id":"call_1","input":"private"}`},
		{name: "invalid caller", item: `{"type":"custom_tool_call","call_id":"call_1","name":"execute","input":"private","caller":{"type":"program"}}`},
		{name: "null output", item: `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","output":null}`},
		{name: "ambiguous file", item: `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","output":[{"type":"input_file","file_id":"file_1","file_url":"https://example.com/private"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id":"resp_1","model":"test-model","status":"completed","output":[` + tc.item + `],"usage":{"output_tokens":8}}`
			client := testClient(t, staticJSON(body), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			assertProtocolError(t, err)
			if result.Usage.Output != count(8) || len(result.Response.Output) != 0 {
				t.Fatalf("result = %#v; want usage without invalid output", result)
			}
		})
	}
}

func TestStreamCustomConflictsPreserveUsage(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{name: "input", before: `"input":"private"`, after: `"input":"different"`},
		{name: "call ID", before: `"call_id":"call_1"`, after: `"call_id":"other"`},
		{name: "name", before: `"name":"execute"`, after: `"name":"other"`},
		{name: "namespace", before: `"namespace":"sandbox"`, after: `"namespace":"other"`},
		{name: "caller", before: `"caller_id":"program_1"`, after: `"caller_id":"other"`},
		{name: "async", before: `"async":false`, after: `"async":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := `{"id":"ct_1","type":"custom_tool_call","call_id":"call_1","name":"execute","input":"private","namespace":"sandbox","async":false,"caller":{"type":"program","caller_id":"program_1"}}`
			conflict := strings.Replace(initial, tc.before, tc.after, 1)
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":[`+conflict+`],"usage":{"output_tokens":8}}}`)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
			last := eventAt[generation.UsageUpdated](t, events, len(events)-1)
			if last.Usage.Output != count(8) {
				t.Fatalf("events = %#v; want retained usage", events)
			}
		})
	}
}

func TestStreamCustomRejectsInvalidInputEvents(t *testing.T) {
	for _, tc := range []struct{ name, event string }{
		{name: "missing delta", event: `{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ct_1"}`},
		{name: "null done", event: `{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"ct_1","input":null}`},
		{name: "wrong item", event: `{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"other","delta":"private"}`},
		{name: "wrong index", event: `{"type":"response.custom_tool_call_input.delta","output_index":1,"item_id":"ct_1","delta":"private"}`},
		{name: "function event", event: `{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"ct_1","delta":"{}"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, customCallJSON("ct_1", "call_1", "execute", "")), tc.event)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamCustomTruncation(t *testing.T) {
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, customCallJSON("ct_1", "call_1", "execute", "")),
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ct_1","delta":"print("}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v; want unexpected EOF", err)
	}
	assertNoResponseEnd(t, events)
	if last := events[len(events)-1]; last != (generation.ToolInputDelta{ItemIndex: 0, Fragment: "print("}) {
		t.Fatalf("last event = %#v; want input fragment", last)
	}
}

func TestStreamCustomBoundsRetainedData(t *testing.T) {
	large := strings.Repeat("x", 600_000)
	for _, tc := range []struct{ name, second string }{
		{name: "input", second: fmt.Sprintf(`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ct_1","delta":%q}`, large)},
		{name: "result parts", second: outputItemAdded(1, fmt.Sprintf(`{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","output":[{"type":"input_file","file_url":"https://example.com/%s"}]}`, large))},
		{name: "metadata", second: outputItemAdded(1, fmt.Sprintf(`{"type":"custom_tool_call","id":"ct_2","call_id":"call_2","name":"execute","input":"","namespace":%q}`, large))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, customCallJSON("ct_1", "call_1", "execute", "")), fmt.Sprintf(`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ct_1","delta":%q}`, large), tc.second)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamCustomResultSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, initial, final string
		want                 generation.ToolOutput
	}{
		{name: "text", initial: `"one"`, final: `"one two"`, want: generation.ToolTextOutput("one two")},
		{name: "parts", initial: `[{"type":"input_text","text":"one"}]`, final: `[{"type":"input_text","text":"one"},{"type":"input_image","file_id":"file_1"}]`, want: generation.ToolPartsOutput{generation.Text{Text: "one"}, generation.ImageFile{FileID: "file_1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final := customResultJSON(tc.final, "")
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, customResultJSON(tc.initial, `,"caller":{"type":"program","caller_id":"program_1"}`)),
				`{"type":"response.output_item.done","output_index":0,"item":`+final+`}`,
				`{"type":"response.completed","response":`+responseWithItems(final)+`}`)

			events, err := readStream(t, client)

			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			caller := generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"})
			want := []generation.Event{
				generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
				generation.ItemStarted{Index: 0, Item: generation.CustomToolResult{ID: "out_1", CallID: "call_1", Caller: caller}},
				generation.ItemEnded{Index: 0, Item: generation.CustomToolResult{ID: "out_1", CallID: "call_1", Output: tc.want, Caller: caller}},
				generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "stop"}},
			}
			require.Equal(t, want, events)
		})
	}
}

func TestStreamCustomResultConflicts(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{name: "text", before: `"private"`, after: `"changed"`},
		{name: "parts", before: `[{"type":"input_text","text":"private"}]`, after: `[{"type":"input_text","text":"changed"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, customResultJSON(tc.before, "")),
				`{"type":"response.completed","response":`+responseWithItems(customResultJSON(tc.after, ""))+`}`)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamCustomEmptyInput(t *testing.T) {
	call := customCallJSON("ct_1", "call_1", "execute", "")
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, call),
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ct_1","delta":""}`,
		`{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"ct_1","input":""}`,
		`{"type":"response.completed","response":`+responseWithItems(call)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.CustomToolCall{ID: "ct_1", CallID: "call_1", Name: "execute"}},
		generation.ItemEnded{Index: 0, Item: generation.CustomToolCall{ID: "ct_1", CallID: "call_1", Name: "execute"}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "tool_calls"}},
	}
	require.Equal(t, want, events)
}

func customResultJSON(output, extra string) string {
	return `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","output":` + output + extra + `}`
}

func customCallJSON(id, callID, name, input string) string {
	body, _ := json.Marshal(struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		CallID string `json:"call_id"`
		Name   string `json:"name"`
		Input  string `json:"input"`
	}{"custom_tool_call", id, callID, name, input})
	return string(body)
}
