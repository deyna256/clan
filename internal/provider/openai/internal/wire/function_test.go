package wire_test

import (
	"reflect"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestFunctionCallMetadataAndResultReplay(t *testing.T) {
	raw := []byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed","async":false,"namespace":"crm","caller":{"type":"program","caller_id":"program_1"}},
		{"type":"function_call_output","id":"out_1","status":"completed","output":[{"type":"input_text","text":"Found"},{"type":"input_image","file_id":"file_1"}],"name":"lookup","namespace":"crm","caller":null,"created_by":"program_1"}
	]}`)
	caller := generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"})
	want := []generation.Item{
		generation.ToolCall{
			ID:        "fc_1",
			CallID:    "call_1",
			Name:      "lookup",
			Arguments: "{}",
			Status:    generation.ItemCompleted,
			OpenAI:    generation.OpenAIToolCallData{Async: generation.Some(false), Namespace: generation.Some("crm"), Caller: caller},
		},
		generation.ToolResult{
			ID:     generation.Some("out_1"),
			Status: generation.Some(generation.ItemCompleted),
			Output: generation.ToolPartsOutput{generation.OpenAIFunctionText{Text: "Found"}, generation.OpenAIFunctionImage{FileID: generation.Some("file_1")}},
			Caller: generation.Null[generation.OpenAIToolCaller](),
			OpenAI: generation.OpenAIToolResultData{Name: generation.Some("lookup"), Namespace: generation.Some("crm"), CreatedBy: generation.Some("program_1")},
		},
	}

	envelope, err := wire.DecodeEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := envelope.Result(nil)

	if err != nil || !reflect.DeepEqual(result.Response.Output, want) {
		t.Fatalf("Result = %#v, %v; want %#v", result.Response.Output, err, want)
	}

	body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}","status":"completed","async":false,"namespace":"crm","caller":{"type":"program","caller_id":"program_1"}},
		{"type":"function_call_output","id":"out_1","status":"completed","output":[{"type":"input_text","text":"Found"},{"type":"input_image","file_id":"file_1"}],"name":"lookup","namespace":"crm","caller":null}
	]}`)
}

func TestEncodeFunctionResultPresence(t *testing.T) {
	request := requestWith(
		generation.ToolResult{Output: generation.ToolTextOutput("")},
		generation.ToolResult{
			ID:     generation.Null[string](),
			CallID: generation.Null[string](),
			Status: generation.Null[generation.ItemStatus](),
			Output: generation.ToolPartsOutput{},
			Caller: generation.Null[generation.OpenAIToolCaller](),
			OpenAI: generation.OpenAIToolResultData{Name: generation.Null[string](), Namespace: generation.Null[string]()},
		},
		generation.ToolResult{CallID: generation.Some("call_1"), Output: generation.ToolTextOutput("done"), Caller: generation.Some(generation.OpenAIToolCaller{Type: "direct"})},
	)

	body, err := wire.EncodeRequest(request, false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"function_call_output","output":""},
		{"type":"function_call_output","id":null,"call_id":null,"status":null,"output":[],"caller":null,"name":null,"namespace":null},
		{"type":"function_call_output","call_id":"call_1","output":"done","caller":{"type":"direct"}}
	]}`)
}

func TestDecodeFunctionCallWithoutOptionalID(t *testing.T) {
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}","caller":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []generation.Item{generation.ToolCall{CallID: "call_1", Name: "lookup", Arguments: "{}", OpenAI: generation.OpenAIToolCallData{Caller: generation.Null[generation.OpenAIToolCaller]()}}}

	result, err := envelope.Result(nil)

	if err != nil || !reflect.DeepEqual(result.Response.Output, want) {
		t.Fatalf("Result = %#v, %v; want %#v", result.Response.Output, err, want)
	}
}

func TestRejectInvalidFunctionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		item generation.Item
	}{
		{name: "null async", item: generation.ToolCall{CallID: "c", Name: "f", OpenAI: generation.OpenAIToolCallData{Async: generation.Null[bool]()}}},
		{name: "null namespace", item: generation.ToolCall{CallID: "c", Name: "f", OpenAI: generation.OpenAIToolCallData{Namespace: generation.Null[string]()}}},
		{name: "caller missing ID", item: generation.ToolCall{CallID: "c", Name: "f", OpenAI: generation.OpenAIToolCallData{Caller: generation.Some(generation.OpenAIToolCaller{Type: "program"})}}},
		{name: "result caller", item: generation.ToolResult{Output: generation.ToolTextOutput(""), Caller: generation.Some(generation.OpenAIToolCaller{Type: "private"})}},
		{name: "result namespace", item: generation.ToolResult{Output: generation.ToolTextOutput(""), OpenAI: generation.OpenAIToolResultData{Namespace: generation.Some("private\xff")}}},
		{name: "result status", item: generation.ToolResult{Output: generation.ToolTextOutput(""), Status: generation.Some(generation.ItemStatus("private"))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(tc.item)

			body, err := wire.EncodeRequest(request, false)

			assertInputError(t, body, err)
		})
	}
}

func TestRejectMalformedToolReturnedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "custom null async", raw: `{"type":"custom_tool_call","call_id":"c","name":"f","input":"","async":null}`},
		{name: "custom null namespace", raw: `{"type":"custom_tool_call","call_id":"c","name":"f","input":"","namespace":null}`},
		{name: "custom null ID", raw: `{"type":"custom_tool_call","id":null,"call_id":"c","name":"f","input":""}`},
		{name: "custom null call ID", raw: `{"type":"custom_tool_call","call_id":null,"name":"f","input":""}`},
		{name: "custom null name", raw: `{"type":"custom_tool_call","call_id":"c","name":null,"input":""}`},
		{name: "call null async", raw: `{"type":"function_call","call_id":"c","name":"f","arguments":"{}","async":null}`},
		{name: "call null namespace", raw: `{"type":"function_call","call_id":"c","name":"f","arguments":"{}","namespace":null}`},
		{name: "call null ID", raw: `{"type":"function_call","id":null,"call_id":"c","name":"f","arguments":"{}"}`},
		{name: "call missing caller ID", raw: `{"type":"function_call","call_id":"c","name":"f","arguments":"{}","caller":{"type":"program"}}`},
		{name: "result missing ID", raw: `{"type":"function_call_output","status":"completed","output":""}`},
		{name: "result missing status", raw: `{"type":"function_call_output","id":"out_1","output":""}`},
		{name: "result null status", raw: `{"type":"function_call_output","id":"out_1","status":null,"output":""}`},
		{name: "result null call ID", raw: `{"type":"function_call_output","id":"out_1","status":"completed","call_id":null,"output":""}`},
		{name: "result null name", raw: `{"type":"function_call_output","id":"out_1","status":"completed","name":null,"output":""}`},
		{name: "result null created_by", raw: `{"type":"function_call_output","id":"out_1","status":"completed","created_by":null,"output":""}`},
		{name: "result missing output", raw: `{"type":"function_call_output","id":"out_1","status":"completed"}`},
		{name: "result malformed output", raw: `{"type":"function_call_output","id":"out_1","status":"completed","output":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[` + tc.raw + `]}`))
			if err != nil {
				t.Fatal(err)
			}

			_, err = envelope.Result(nil)

			if failure, ok := err.(*generation.Failure); !ok || failure.Kind != generation.ProtocolError {
				t.Fatalf("Result error = %v; want protocol error", err)
			}
		})
	}
}
