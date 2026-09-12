package openai_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/stretchr/testify/require"
)

func TestStreamFunctionNamespacesAndLateMetadata(t *testing.T) {
	var logs bytes.Buffer
	first := functionCallJSON("fc_1", "call_1", "{}", `,"namespace":"crm"`)
	second := functionCallJSON("fc_2", "call_2", "{}", "")
	client := streamClient(t, &logs, created(),
		outputItemAdded(0, functionCallJSON("fc_1", "call_1", "", `,"namespace":"crm"`)),
		outputItemAdded(1, functionCallJSON("fc_2", "call_2", "", "")),
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_2","delta":"{"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{}"}`,
		outputItemAdded(1, functionCallJSON("fc_2", "call_2", "{}", `,"namespace":"billing","status":"completed","async":false,"caller":{"type":"program","caller_id":"program_1"}`)),
		`{"type":"response.completed","response":`+responseWithItems(first+","+second)+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var deltas []generation.ArgumentsDelta
	var calls []generation.ToolCall
	for _, event := range events {
		switch value := event.(type) {
		case generation.ArgumentsDelta:
			deltas = append(deltas, value)
		case generation.ItemEnded:
			calls = append(calls, value.Item.(generation.ToolCall))
		}
	}
	wantCalls := []generation.ToolCall{
		{ID: "fc_1", CallID: "call_1", Name: "lookup", Arguments: "{}", OpenAI: generation.OpenAIToolCallData{Namespace: generation.Some("crm")}},
		{ID: "fc_2", CallID: "call_2", Name: "lookup", Arguments: "{}", Status: generation.ItemCompleted, OpenAI: generation.OpenAIToolCallData{
			Namespace: generation.Some("billing"), Async: generation.Some(false), Caller: generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"}),
		}},
	}
	wantDeltas := []generation.ArgumentsDelta{{ItemIndex: 1, Fragment: "{"}, {ItemIndex: 0, Fragment: "{}"}, {ItemIndex: 1, Fragment: "}"}}
	require.Equal(t, wantCalls, calls)
	require.Equal(t, wantDeltas, deltas)
	require.Equal(t, 0, logs.Len())
}

func TestStreamFunctionRejectsChangedRoutingMetadata(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{name: "namespace", before: `,"namespace":"crm"`, after: `,"namespace":"billing"`},
		{name: "async", before: `,"async":false`, after: `,"async":true`},
		{name: "caller", before: `,"caller":{"type":"direct"}`, after: `,"caller":{"type":"program","caller_id":"program_1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final := responseWithItems(functionCallJSON("fc_1", "call_1", "{}", tc.after))
			final = strings.Replace(final, `"output":`, `"usage":{"output_tokens":8},"output":`, 1)
			client := streamClient(t, io.Discard, created(),
				outputItemAdded(0, functionCallJSON("fc_1", "call_1", "", tc.before)),
				`{"type":"response.completed","response":`+final+`}`,
			)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
			last := eventAt[generation.UsageUpdated](t, events, len(events)-1)
			if last.Usage.Output != count(8) {
				t.Fatalf("last event = %#v; want output usage 8", events[len(events)-1])
			}
		})
	}
}

func TestGenerateFunctionResultWithoutCallID(t *testing.T) {
	body := responseWithItems(functionResultJSON(`[{"type":"input_text","text":"found"},{"type":"input_image","file_id":"file_1","detail":"low"}]`, `,"name":"lookup","namespace":"crm","created_by":"actor_1","caller":{"type":"program","caller_id":"program_1"}`))
	client := testClient(t, staticJSON(body), io.Discard)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	require.NoError(t, err)
	want := generation.ToolResult{
		ID: generation.Some("out_1"), Status: generation.Some(generation.ItemCompleted),
		Output: generation.ToolPartsOutput{generation.OpenAIFunctionText{Text: "found"}, generation.OpenAIFunctionImage{FileID: generation.Some("file_1"), Detail: generation.Some("low")}},
		Caller: generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"}),
		OpenAI: generation.OpenAIToolResultData{Name: generation.Some("lookup"), Namespace: generation.Some("crm"), CreatedBy: generation.Some("actor_1")},
	}
	require.Equal(t, []generation.Item{want}, result.Response.Output)
	require.Equal(t, "stop", result.Response.Finish.Reason)
}

func TestStreamFunctionResultRetainsLateIdentity(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		outputItemAdded(0, functionResultJSON(`""`, "")),
		outputItemAdded(0, functionResultJSON(`"found"`, `,"call_id":"call_1","name":"lookup","namespace":"crm","created_by":"actor_1","caller":{"type":"direct"}`)),
		`{"type":"response.completed","response":`+responseWithItems(functionResultJSON(`"found"`, ""))+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.Event{
		generation.ResponseStarted{Identity: generation.Identity{ID: "resp_1", Model: "test-model"}},
		generation.ItemStarted{Index: 0, Item: generation.ToolResult{ID: generation.Some("out_1"), Status: generation.Some(generation.ItemCompleted)}},
		generation.ItemEnded{Index: 0, Item: generation.ToolResult{
			ID: generation.Some("out_1"), CallID: generation.Some("call_1"), Status: generation.Some(generation.ItemCompleted), Output: generation.ToolTextOutput("found"),
			Caller: generation.Some(generation.OpenAIToolCaller{Type: "direct"}),
			OpenAI: generation.OpenAIToolResultData{Name: generation.Some("lookup"), Namespace: generation.Some("crm"), CreatedBy: generation.Some("actor_1")},
		}},
		generation.ResponseEnded{Finish: generation.Finish{Status: "completed", Reason: "stop"}},
	}
	require.Equal(t, want, events)
}

func TestStreamFunctionResultRejectsChangedIdentity(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{name: "call ID", before: `,"call_id":"call_1"`, after: `,"call_id":"call_2"`},
		{name: "name", before: `,"name":"lookup"`, after: `,"name":"delete"`},
		{name: "namespace", before: `,"namespace":"crm"`, after: `,"namespace":"billing"`},
		{name: "creator", before: `,"created_by":"actor_1"`, after: `,"created_by":"actor_2"`},
		{name: "caller", before: `,"caller":{"type":"direct"}`, after: `,"caller":null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(),
				outputItemAdded(0, functionResultJSON(`"found"`, tc.before)),
				`{"type":"response.completed","response":`+responseWithItems(functionResultJSON(`"found"`, tc.after))+`}`,
			)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamFunctionBoundsLateMetadata(t *testing.T) {
	large := strings.Repeat("x", 600_000)
	client := streamClient(t, io.Discard, created(),
		outputItemAdded(0, functionCallJSON("fc_1", "call_1", "", `,"namespace":"`+large+`"`)),
		outputItemAdded(0, functionCallJSON("fc_1", "call_1", "", `,"caller":{"type":"program","caller_id":"`+large+`"}`)),
	)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamFunctionResultRejectsConflictingOutput(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{name: "text", before: `"first"`, after: `"other"`},
		{name: "part", before: `[{"type":"input_text","text":"first"}]`, after: `[{"type":"input_text","text":"other"}]`},
		{name: "kind", before: `"first"`, after: `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, functionResultJSON(tc.before, "")),
				`{"type":"response.completed","response":`+responseWithItems(functionResultJSON(tc.after, ""))+`}`)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamFunctionFinalWithoutID(t *testing.T) {
	final := `{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}`
	client := streamClient(t, io.Discard, created(),
		outputItemAdded(0, functionCallJSON("fc_1", "call_1", "", `,"namespace":"crm"`)),
		`{"type":"response.completed","response":`+responseWithItems(final)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := generation.ToolCall{ID: "fc_1", CallID: "call_1", Name: "lookup", Arguments: "{}", OpenAI: generation.OpenAIToolCallData{Namespace: generation.Some("crm")}}
	ended := eventAt[generation.ItemEnded](t, events, len(events)-2).Item
	require.Equal(t, want, ended)
}

func TestStreamFunctionPreservesJSONArgumentValues(t *testing.T) {
	for _, arguments := range []string{`null`, `[]`, `[9007199254740993]`, `true`, `"query"`} {
		t.Run(arguments, func(t *testing.T) {
			call := functionCallJSON("fc_1", "call_1", arguments, `,"namespace":"crm"`)
			client := streamClient(t, io.Discard, `{"type":"response.completed","response":`+responseWithItems(call)+`}`)

			events, err := readStream(t, client)

			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			delta := eventAt[generation.ArgumentsDelta](t, events, 2)
			ended := eventAt[generation.ItemEnded](t, events, len(events)-2).Item.(generation.ToolCall)
			if delta.Fragment != arguments || ended.Arguments != arguments {
				t.Fatalf("delta = %q; ended arguments = %q; want %q", delta.Fragment, ended.Arguments, arguments)
			}
		})
	}
}

func TestStreamFunctionBoundsRetainedResults(t *testing.T) {
	output, _ := json.Marshal(strings.Repeat("x", 600_000))
	first := functionResultJSON(string(output), "")
	second := strings.Replace(first, `"out_1"`, `"out_2"`, 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, first), outputItemAdded(1, second))

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamFunctionDeltaRequiresFragment(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		outputItemAdded(0, functionCallJSON("fc_1", "call_1", "", "")),
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","arguments":"{}"}`)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func functionCallJSON(id, callID, arguments, extra string) string {
	encoded, _ := json.Marshal(arguments)
	return `{"type":"function_call","id":"` + id + `","call_id":"` + callID + `","name":"lookup","arguments":` + string(encoded) + extra + `}`
}

func functionResultJSON(output, extra string) string {
	return `{"type":"function_call_output","id":"out_1","status":"completed","output":` + output + extra + `}`
}
