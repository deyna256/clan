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
	"github.com/stretchr/testify/require"
)

func TestGenerateMCPFailuresRemainToolData(t *testing.T) {
	for _, tc := range []struct {
		name, errorJSON string
		want            generation.MCPCallError
	}{
		{name: "protocol", errorJSON: `{"type":"mcp_protocol_error","code":-32603,"message":"private error"}`, want: generation.MCPProtocolError{Code: -32603, Message: "private error"}},
		{name: "HTTP", errorJSON: `{"type":"http_error","code":502,"message":"bad gateway"}`, want: generation.MCPHTTPError{Code: 502, Message: "bad gateway"}},
		{name: "execution", errorJSON: `{"type":"mcp_tool_execution_error","content":{"id":9007199254740993}}`, want: generation.MCPExecutionError{Content: json.RawMessage(`{"id":9007199254740993}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			call := mcpCallJSON("{", `,"status":"failed","error":`+tc.errorJSON)
			client := testClient(t, staticJSON(responseWithItems(call)), &logs)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			require.NoError(t, err)
			want := mcpCall("{")
			want.Status = generation.Some("failed")
			want.Error = generation.Some(tc.want)
			require.Equal(t, []generation.Item{want}, result.Response.Output)
			require.Equal(t, "stop", result.Response.Finish.Reason)
			require.Equal(t, 0, logs.Len())
		})
	}
}

func TestStreamMCPInterleavesArgumentsAndReportsToolFailure(t *testing.T) {
	var logs bytes.Buffer
	first := mcpCallJSON("", `,"status":"in_progress"`)
	second := strings.Replace(mcpCallJSON("", ""), "mcp_1", "mcp_2", 1)
	finalFirst := mcpCallJSON(`{"q":`, `,"status":"failed","error":{"type":"http_error","code":502,"message":"retry later"}`)
	finalSecond := strings.Replace(mcpCallJSON(`{"x":1}`, `,"output":"ok","status":"completed"`), "mcp_1", "mcp_2", 1)
	client := streamClient(t, &logs, created(), outputItemAdded(0, first), outputItemAdded(1, second),
		`{"type":"response.mcp_call.in_progress","output_index":0,"item_id":"mcp_1"}`,
		`{"type":"response.mcp_call_arguments.delta","output_index":0,"item_id":"mcp_1","delta":"{\"q\":"}`,
		`{"type":"response.mcp_call_arguments.delta","output_index":1,"item_id":"mcp_2","delta":"{\"x\":","sequence_number":4}`,
		`{"type":"response.mcp_call_arguments.delta","output_index":1,"item_id":"mcp_2","delta":"{\"x\":","sequence_number":4}`,
		`{"type":"response.mcp_call_arguments.done","output_index":1,"item_id":"mcp_2","arguments":"{\"x\":1}"}`,
		`{"type":"response.mcp_call.failed","output_index":0,"item_id":"mcp_1"}`,
		`{"type":"response.mcp_call.completed","output_index":1,"item_id":"mcp_2"}`,
		`{"type":"response.completed","response":`+responseWithItems(finalFirst+","+finalSecond)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var deltas []generation.ArgumentsDelta
	var progress []generation.MCPProgress
	var calls []generation.OpenAIMCPCall
	for _, event := range events {
		switch value := event.(type) {
		case generation.ArgumentsDelta:
			deltas = append(deltas, value)
		case generation.MCPProgress:
			progress = append(progress, value)
		case generation.ItemEnded:
			calls = append(calls, value.Item.(generation.OpenAIMCPCall))
		}
	}
	wantDeltas := []generation.ArgumentsDelta{{ItemIndex: 0, Fragment: `{"q":`}, {ItemIndex: 1, Fragment: `{"x":`}, {ItemIndex: 1, Fragment: `1}`}}
	wantProgress := []generation.MCPProgress{{ItemIndex: 0, Status: "in_progress"}, {ItemIndex: 0, Status: "failed"}, {ItemIndex: 1, Status: "completed"}}
	wantFirst, wantSecond := mcpCall(`{"q":`), mcpCall(`{"x":1}`)
	wantFirst.Status, wantFirst.Error = generation.Some("failed"), generation.Some[generation.MCPCallError](generation.MCPHTTPError{Code: 502, Message: "retry later"})
	wantSecond.ID, wantSecond.Status, wantSecond.Output = "mcp_2", generation.Some("completed"), generation.Some("ok")
	require.Equal(t, wantDeltas, deltas)
	require.Equal(t, wantProgress, progress)
	require.Equal(t, []generation.OpenAIMCPCall{wantFirst, wantSecond}, calls)
	if finish := eventAt[generation.ResponseEnded](t, events, len(events)-1).Finish; finish.Reason != "stop" || logs.Len() != 0 {
		t.Fatalf("finish = %#v; logs = %s; want stop without warnings", finish, logs.String())
	}
}

func TestStreamMCPListFailureDoesNotEndGeneration(t *testing.T) {
	initial := mcpListJSON("[]", "")
	final := mcpListJSON("[]", `,"error":"server unavailable"`)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial),
		`{"type":"response.mcp_list_tools.in_progress","output_index":0,"item_id":"list_1"}`,
		`{"type":"response.mcp_list_tools.failed","output_index":0,"item_id":"list_1"}`,
		`{"type":"response.completed","response":`+responseWithItems(final)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := generation.OpenAIMCPListTools{ID: "list_1", ServerLabel: "server", Tools: []generation.MCPListedTool{}, Error: generation.Some("server unavailable")}
	require.Equal(t, want, eventAt[generation.ItemEnded](t, events, len(events)-2).Item)
	if progress := eventAt[generation.MCPProgress](t, events, 3); progress.Status != "failed" || progress.ItemIndex != 0 {
		t.Fatalf("progress = %#v; want failed at item 0", progress)
	}
}

func TestStreamMCPUsesLatestCatalogAndRetainsOmittedMetadata(t *testing.T) {
	first := mcpListJSON(`[{"name":"lookup","input_schema":true,"description":"find it","annotations":{"priority":9007199254740993}}]`, "")
	last := mcpListJSON(`[{"name":"lookup","input_schema":{"type":"object"}},{"name":"new","input_schema":null,"annotations":null}]`, "")
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, first), `{"type":"response.completed","response":`+responseWithItems(last)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := []generation.MCPListedTool{
		{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`), Description: generation.Some("find it"), Annotations: generation.Some(json.RawMessage(`{"priority":9007199254740993}`))},
		{Name: "new", InputSchema: json.RawMessage(`null`), Annotations: generation.Null[json.RawMessage]()},
	}
	end := eventAt[generation.ItemEnded](t, events, len(events)-2).Item.(generation.OpenAIMCPListTools)
	require.Equal(t, want, end.Tools)
}

func TestStreamMCPApprovalRetainsExplicitDenial(t *testing.T) {
	request := `{"type":"mcp_approval_request","id":"approval_1","server_label":"server","name":"lookup","arguments":"{\"id\":1}"}`
	response := `{"type":"mcp_approval_response","id":"reply_1","approval_request_id":"approval_1","approve":false,"reason":"declined"}`
	finalResponse := strings.Replace(response, `,"reason":"declined"`, "", 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, request), outputItemAdded(1, response),
		`{"type":"response.completed","response":`+responseWithItems(request+","+finalResponse)+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	wantRequest := generation.OpenAIMCPApprovalRequest{ID: "approval_1", ServerLabel: "server", Name: "lookup", Arguments: `{"id":1}`}
	wantResponse := generation.OpenAIMCPApprovalResponse{ID: generation.Some("reply_1"), ApprovalRequestID: "approval_1", Approve: false, Reason: generation.Some("declined")}
	var ended []generation.Item
	for _, event := range events {
		if end, ok := event.(generation.ItemEnded); ok {
			ended = append(ended, end.Item)
		}
	}
	require.Equal(t, []generation.Item{wantRequest, wantResponse}, ended)
}

func TestStreamMCPRejectsConflictsAndPreservesUsage(t *testing.T) {
	initial := mcpCallJSON(`{"q":1}`, `,"approval_request_id":"approval_1"`)
	for _, tc := range []struct{ name, before, after string }{
		{name: "tool name", before: `"name":"lookup"`, after: `"name":"other"`},
		{name: "server", before: `"server_label":"server"`, after: `"server_label":"other"`},
		{name: "arguments", before: `"arguments":"{\"q\":1}"`, after: `"arguments":"private"`},
		{name: "approval link", before: `"approval_request_id":"approval_1"`, after: `"approval_request_id":"other"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final := strings.TrimSuffix(responseWithItems(strings.Replace(initial, tc.before, tc.after, 1)), "}") + `,"usage":{"output_tokens":8}}`
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+final+`}`)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
			last := eventAt[generation.UsageUpdated](t, events, len(events)-1)
			if last.Usage.Output != count(8) {
				t.Fatalf("events = %#v; want preserved usage", events)
			}
		})
	}
}

func TestStreamMCPRejectsInvalidEventTargets(t *testing.T) {
	for _, tc := range []struct{ name, event string }{
		{name: "missing item ID", event: `{"type":"response.mcp_call_arguments.delta","output_index":0,"delta":"{"}`},
		{name: "wrong item ID", event: `{"type":"response.mcp_call.failed","output_index":0,"item_id":"other"}`},
		{name: "wrong item kind", event: `{"type":"response.mcp_list_tools.failed","output_index":0,"item_id":"mcp_1"}`},
		{name: "missing arguments", event: `{"type":"response.mcp_call_arguments.done","output_index":0,"item_id":"mcp_1"}`},
		{name: "negative index", event: `{"type":"response.mcp_call.completed","output_index":-1,"item_id":"mcp_1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := streamClient(t, io.Discard, created(), outputItemAdded(0, mcpCallJSON("", "")), tc.event)

			events, err := readStream(t, client)

			assertProtocolError(t, err)
			assertNoResponseEnd(t, events)
		})
	}
}

func TestStreamMCPRetainedMetadataIsIndependentOfCaller(t *testing.T) {
	list := mcpListJSON(`[{"name":"lookup","input_schema":{"maximum":9007199254740993},"annotations":{"title":"Lookup"}}]`, "")
	call := mcpCallJSON("{}", `,"error":{"type":"mcp_tool_execution_error","content":{"data":"private"}}`)
	finalList := mcpListJSON(`[{"name":"lookup","input_schema":{"maximum":9007199254740993}}]`, "")
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, list), outputItemAdded(1, call), `{"type":"response.completed","response":`+responseWithItems(finalList+","+mcpCallJSON("{}", ""))+`}`)
	stream, err := client.GenerateStream(t.Context(), testAttempt(), textRequest())
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	var ends []generation.Item
	var mutatedList, mutatedCall bool
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		switch value := event.(type) {
		case generation.ItemStarted:
			switch item := value.Item.(type) {
			case generation.OpenAIMCPListTools:
				annotations, _ := item.Tools[0].Annotations.Value()
				annotations[0] = '!'
				mutatedList = true
			case generation.OpenAIMCPCall:
				err, _ := item.Error.Value()
				err.(generation.MCPExecutionError).Content[0] = '!'
				mutatedCall = true
			}
		case generation.ItemEnded:
			ends = append(ends, value.Item)
		}
	}

	if !mutatedList || !mutatedCall || len(ends) != 2 {
		t.Fatalf("mutated list/call = %v/%v; ended items = %d; want both starts mutated and two ends", mutatedList, mutatedCall, len(ends))
	}
	tool := ends[0].(generation.OpenAIMCPListTools).Tools[0]
	annotations, _ := tool.Annotations.Value()
	callError, _ := ends[1].(generation.OpenAIMCPCall).Error.Value()
	if string(tool.InputSchema) != `{"maximum":9007199254740993}` || string(annotations) != `{"title":"Lookup"}` || string(callError.(generation.MCPExecutionError).Content) != `{"data":"private"}` {
		t.Fatalf("snapshots changed through caller data: %#v", ends)
	}
}

func TestStreamMCPBoundsRetainedArguments(t *testing.T) {
	large, _ := json.Marshal(strings.Repeat("x", 600_000))
	frame := `{"type":"response.mcp_call_arguments.delta","output_index":0,"item_id":"mcp_1","delta":` + string(large) + `}`
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, mcpCallJSON("", "")), frame, frame)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamMCPNeedsTerminalResponseAfterToolCompletion(t *testing.T) {
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, mcpCallJSON("{}", "")), `{"type":"response.mcp_call.completed","output_index":0,"item_id":"mcp_1"}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v; want unexpected EOF", err)
	}
	assertNoResponseEnd(t, events)
}

func TestStreamMCPRejectsChangedApprovalDecision(t *testing.T) {
	initial := `{"type":"mcp_approval_response","id":"reply_1","approval_request_id":"approval_1","approve":false}`
	final := strings.Replace(initial, `"approve":false`, `"approve":true`, 1)
	client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), `{"type":"response.completed","response":`+responseWithItems(final)+`}`)

	events, err := readStream(t, client)

	assertProtocolError(t, err)
	assertNoResponseEnd(t, events)
}

func TestStreamMCPFinalOnlyIncomplete(t *testing.T) {
	final := `{"id":"resp_1","model":"test-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[` + mcpCallJSON(`{"q":`, `,"status":"incomplete"`) + `]}`
	client := streamClient(t, io.Discard, `{"type":"response.incomplete","response":`+final+`}`)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	want := mcpCall(`{"q":`)
	want.Status = generation.Some("incomplete")
	require.Equal(t, want, eventAt[generation.ItemEnded](t, events, len(events)-2).Item)
	if finish := eventAt[generation.ResponseEnded](t, events, len(events)-1).Finish; finish.Status != "incomplete" || finish.Reason != "max_output_tokens" {
		t.Fatalf("finish = %#v; want incomplete due to max_output_tokens", finish)
	}
}

func mcpCall(arguments string) generation.OpenAIMCPCall {
	return generation.OpenAIMCPCall{ID: "mcp_1", ServerLabel: "server", Name: "lookup", Arguments: arguments}
}

func mcpCallJSON(arguments, extra string) string {
	encoded, _ := json.Marshal(arguments)
	return fmt.Sprintf(`{"type":"mcp_call","id":"mcp_1","server_label":"server","name":"lookup","arguments":%s%s}`, encoded, extra)
}

func mcpListJSON(tools, extra string) string {
	return `{"type":"mcp_list_tools","id":"list_1","server_label":"server","tools":` + tools + extra + `}`
}
