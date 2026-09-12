package openai_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
)

func TestGenerateToolSearchExecution(t *testing.T) {
	for _, tc := range []struct{ execution, reason string }{
		{execution: "server", reason: "stop"},
		{execution: "client", reason: "tool_calls"},
	} {
		t.Run(tc.execution, func(t *testing.T) {
			call := toolSearchCallJSON(tc.execution, `{"limit":9007199254740993}`, "")
			client := testClient(t, staticJSON(responseWithItems(call)), io.Discard)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			if err != nil {
				t.Fatal(err)
			}
			got := result.Response.Output[0].(generation.OpenAIToolSearchCall)
			if string(got.Arguments) != `{"limit":9007199254740993}` || result.Response.Finish.Reason != tc.reason {
				t.Fatalf("call = %#v; finish = %#v; want exact arguments and %s", got, result.Response.Finish, tc.reason)
			}
		})
	}
}

func TestStreamToolSearchMergesAtomicSnapshots(t *testing.T) {
	var logs bytes.Buffer
	catalog := `[{"type":"namespace","name":"crm","description":"Customers","tools":[{"type":"function","name":"lookup","parameters":{"const":9007199254740993}}]}]`
	client := streamClient(t, &logs, created(),
		outputItemAdded(0, toolSearchCallJSON("server", `null`, `,"created_by":"actor_1"`)),
		outputItemAdded(1, toolSearchOutputJSON(`[{"type":"function","name":"obsolete","parameters":{}}]`, `,"created_by":"actor_1"`)),
		outputItemAdded(0, toolSearchCallJSON("server", `{"paths":["obsolete"]}`, "")),
		outputItemAdded(1, toolSearchOutputJSON(catalog, "")),
		`{"type":"response.completed","response":`+responseWithItems(toolSearchCallJSON("server", `{"paths":["crm"]}`, "")+","+toolSearchOutputJSON(catalog, ""))+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var ended []generation.Item
	for _, event := range events {
		switch value := event.(type) {
		case generation.ItemStarted:
			switch item := value.Item.(type) {
			case generation.OpenAIToolSearchCall:
				if item.Arguments != nil {
					t.Fatal("start exposes nonfinal search arguments")
				}
			case generation.OpenAIToolSearchOutput:
				if item.Tools != nil {
					t.Fatal("start exposes nonfinal catalog")
				}
			}
		case generation.ItemEnded:
			ended = append(ended, value.Item)
		}
	}
	want := []generation.Item{
		generation.OpenAIToolSearchCall{ID: generation.Some("search_1"), CallID: generation.Null[string](), Execution: generation.Some("server"), Status: generation.Some(generation.ItemCompleted), Arguments: json.RawMessage(`{"paths":["crm"]}`), CreatedBy: generation.Some("actor_1")},
		generation.OpenAIToolSearchOutput{ID: generation.Some("loaded_1"), CallID: generation.Null[string](), Execution: generation.Some("server"), Status: generation.Some(generation.ItemCompleted), CreatedBy: generation.Some("actor_1"), Tools: []generation.Tool{
			generation.OpenAINamespaceTool{Name: "crm", Description: "Customers", Tools: []generation.Tool{generation.FunctionTool{Name: "lookup", Parameters: json.RawMessage(`{"const":9007199254740993}`)}}},
		}},
	}
	if !reflect.DeepEqual(ended, want) || logs.Len() != 0 {
		t.Fatalf("ended = %#v; want %#v; logs = %s", ended, want, logs.String())
	}
}

func TestStreamToolSearchRejectsIdentityChanges(t *testing.T) {
	before := toolSearchCallJSON("client", `{}`, `,"created_by":"actor_1"`)
	for _, tc := range []struct{ name, after string }{
		{name: "execution", after: strings.Replace(before, `"client"`, `"server"`, 1)},
		{name: "call ID", after: strings.Replace(before, `"call_1"`, `"call_2"`, 1)},
		{name: "creator", after: strings.Replace(before, `"actor_1"`, `"actor_2"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, before), outputItemAdded(0, tc.after))

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamToolSearchRejectsUnsupportedCatalogAndKeepsUsage(t *testing.T) {
	catalog := `[{"type":"function","name":"lookup","parameters":{}},{"type":"future_tool"}]`
	body := responseWithItems(toolSearchOutputJSON(catalog, ""))
	body = strings.Replace(body, `"output":`, `"usage":{"output_tokens":8},"output":`, 1)
	client := streamClient(t, io.Discard, created(), `{"type":"response.completed","response":`+body+`}`)

	events, err := readStream(t, client)

	var failure *generation.Failure
	if !errors.As(err, &failure) || failure.Kind != generation.Unsupported {
		t.Fatalf("error = %v; want unsupported", err)
	}
	assertNoResponseEnd(t, events)
	last, ok := events[len(events)-1].(generation.UsageUpdated)
	if !ok || last.Usage.Output != count(8) {
		t.Fatalf("last event = %#v; want usage 8", events[len(events)-1])
	}
}

func TestStreamToolSearchBoundsRetainedCatalogs(t *testing.T) {
	catalog := `[{"type":"function","name":"lookup","parameters":{"description":"` + strings.Repeat("x", 600_000) + `"}}]`
	first := toolSearchOutputJSON(catalog, "")
	second := strings.Replace(first, `"loaded_1"`, `"loaded_2"`, 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, first), outputItemAdded(1, second))

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamToolSearchNeedsTerminalResponse(t *testing.T) {
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, toolSearchOutputJSON(`[]`, "")))

	events, err := readStream(t, client)

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v; want unexpected EOF", err)
	}
	assertNoResponseEnd(t, events)
}

func TestStreamAdditionalToolsPreservesRoleAndCatalog(t *testing.T) {
	item := `{"type":"additional_tools","id":"added_1","role":"tool","tools":[{"type":"custom","name":"query"}]}`
	client := streamClient(t, io.Discard, `{"type":"response.completed","response":`+responseWithItems(item)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := generation.OpenAIAdditionalTools{ID: generation.Some("added_1"), Role: "tool", Tools: []generation.Tool{generation.CustomTool{Name: "query"}}}
	ended := events[len(events)-2].(generation.ItemEnded).Item
	if !reflect.DeepEqual(ended, want) {
		t.Fatalf("ended = %#v; want %#v", ended, want)
	}
}

func TestStreamClientToolSearchNeedsActionableCallID(t *testing.T) {
	call := toolSearchCallJSON("client", `{}`, "")
	call = strings.Replace(call, `"call_1"`, `null`, 1)
	body := responseWithItems(call)
	body = strings.Replace(body, `"output":`, `"usage":{"output_tokens":8},"output":`, 1)
	client := streamClient(t, io.Discard, created(), `{"type":"response.completed","response":`+body+`}`)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
	last, ok := events[len(events)-1].(generation.UsageUpdated)
	if !ok || last.Usage.Output != count(8) {
		t.Fatalf("last event = %#v; want usage 8", events[len(events)-1])
	}
}

func TestGenerateIncompleteToolSearchKeepsUnknownCallID(t *testing.T) {
	call := toolSearchCallJSON("client", `null`, "")
	call = strings.Replace(call, `"call_1"`, `null`, 1)
	body := `{"id":"resp_1","model":"test-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + call + `]}`
	client := testClient(t, staticJSON(body), io.Discard)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	if err != nil {
		t.Fatal(err)
	}
	got := result.Response.Output[0].(generation.OpenAIToolSearchCall)
	if !got.CallID.IsNull() || string(got.Arguments) != "null" || result.Response.Finish.Reason != "max_output_tokens" {
		t.Fatalf("call = %#v; finish = %#v; want incomplete search with null ID and arguments", got, result.Response.Finish)
	}
}

func TestStreamToolSearchBoundsRetainedArguments(t *testing.T) {
	arguments, _ := json.Marshal(strings.Repeat("x", 600_000))
	first := toolSearchCallJSON("server", string(arguments), "")
	second := strings.Replace(first, `"search_1"`, `"search_2"`, 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, first), outputItemAdded(1, second))

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func toolSearchCallJSON(execution, arguments, extra string) string {
	callID := `null`
	if execution == "client" {
		callID = `"call_1"`
	}
	return `{"type":"tool_search_call","id":"search_1","execution":"` + execution + `","status":"completed","call_id":` + callID + `,"arguments":` + arguments + extra + `}`
}

func toolSearchOutputJSON(tools, extra string) string {
	return `{"type":"tool_search_output","id":"loaded_1","execution":"server","status":"completed","call_id":null,"tools":` + tools + extra + `}`
}
