package wire_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestProgramReplay(t *testing.T) {
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[
		{"type":"program","id":"prog_1","call_id":"call_1","code":"text('Привет');\n","fingerprint":"opaque\u0000replay"},
		{"type":"program_output","id":"out_1","call_id":"call_1","result":"9007199254740993\n","status":"incomplete"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []generation.Item{
		generation.OpenAIProgram{ID: "prog_1", CallID: "call_1", Code: "text('Привет');\n", Fingerprint: generation.Some("opaque\x00replay")},
		generation.OpenAIProgramOutput{ID: "out_1", CallID: "call_1", Result: "9007199254740993\n", Status: generation.ItemIncomplete},
	}

	result, err := envelope.Result(nil)

	if err != nil || !reflect.DeepEqual(result.Response.Output, want) || result.Response.Finish.Reason != "stop" {
		t.Fatalf("Result = %#v, %v; want %#v with stop", result, err, want)
	}
	request := requestWith(result.Response.Output...)
	request.Tools = []generation.Tool{generation.OpenAIProgrammaticToolCallingTool{}}
	request.ToolChoice = generation.OpenAIProgrammaticToolCallingChoice{}

	body, err := wire.EncodeRequest(request, false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"tools":[{"type":"programmatic_tool_calling"}],"tool_choice":{"type":"programmatic_tool_calling"},"input":[
		{"type":"program","id":"prog_1","call_id":"call_1","code":"text('Привет');\n","fingerprint":"opaque\u0000replay"},
		{"type":"program_output","id":"out_1","call_id":"call_1","result":"9007199254740993\n","status":"incomplete"}
	]}`)
}

func TestProgramEmptyPayloads(t *testing.T) {
	request := requestWith(
		generation.OpenAIProgram{ID: "prog_1", CallID: "call_1", Fingerprint: generation.Some("")},
		generation.OpenAIProgramOutput{ID: "out_1", CallID: "call_1", Status: generation.ItemCompleted},
	)

	body, err := wire.EncodeRequest(request, false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"program","id":"prog_1","call_id":"call_1","code":"","fingerprint":""},
		{"type":"program_output","id":"out_1","call_id":"call_1","result":"","status":"completed"}
	]}`)
}

func TestRejectInvalidProgramReplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		item generation.Item
	}{
		{name: "missing ID", item: generation.OpenAIProgram{CallID: "call_1", Fingerprint: generation.Some("fp")}},
		{name: "missing call ID", item: generation.OpenAIProgramOutput{ID: "out_1", Status: generation.ItemCompleted}},
		{name: "missing fingerprint", item: generation.OpenAIProgram{ID: "prog_1", CallID: "call_1"}},
		{name: "null fingerprint", item: generation.OpenAIProgram{ID: "prog_1", CallID: "call_1", Fingerprint: generation.Null[string]()}},
		{name: "invalid code", item: generation.OpenAIProgram{ID: "prog_1", CallID: "call_1", Code: "secret\xff", Fingerprint: generation.Some("fp")}},
		{name: "invalid fingerprint", item: generation.OpenAIProgram{ID: "prog_1", CallID: "call_1", Fingerprint: generation.Some("secret\xff")}},
		{name: "invalid result", item: generation.OpenAIProgramOutput{ID: "out_1", CallID: "call_1", Result: "secret\xff", Status: generation.ItemCompleted}},
		{name: "nonterminal output", item: generation.OpenAIProgramOutput{ID: "out_1", CallID: "call_1", Status: generation.ItemInProgress}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(tc.item)

			body, err := wire.EncodeRequest(request, false)

			assertCustomInputError(t, body, err)
		})
	}
}

func TestRejectMalformedProgramItems(t *testing.T) {
	for _, tc := range []struct{ name, item string }{
		{name: "missing code", item: `{"type":"program","id":"p","call_id":"c","fingerprint":"fp"}`},
		{name: "null code", item: `{"type":"program","id":"p","call_id":"c","code":null,"fingerprint":"fp"}`},
		{name: "missing fingerprint", item: `{"type":"program","id":"p","call_id":"c","code":""}`},
		{name: "null fingerprint", item: `{"type":"program","id":"p","call_id":"c","code":"","fingerprint":null}`},
		{name: "null call ID", item: `{"type":"program","id":"p","call_id":null,"code":"","fingerprint":"fp"}`},
		{name: "object result", item: `{"type":"program_output","id":"o","call_id":"c","result":{},"status":"completed"}`},
		{name: "null result", item: `{"type":"program_output","id":"o","call_id":"c","result":null,"status":"completed"}`},
		{name: "missing status", item: `{"type":"program_output","id":"o","call_id":"c","result":""}`},
		{name: "invalid status", item: `{"type":"program_output","id":"o","call_id":"c","result":"","status":"failed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[` + tc.item + `]}`))
			if err != nil {
				t.Fatal(err)
			}

			_, err = envelope.Result(nil)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v; want protocol error", err)
			}
		})
	}
}

func TestLoadedProgramTool(t *testing.T) {
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"tool_search_output","id":"s","call_id":null,"execution":"server","status":"completed","tools":[{"type":"programmatic_tool_calling"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}

	result, err := envelope.Result(nil)

	if err != nil {
		t.Fatal(err)
	}
	got := result.Response.Output[0].(generation.OpenAIToolSearchOutput).Tools
	want := []generation.Tool{generation.OpenAIProgrammaticToolCallingTool{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %#v; want %#v", got, want)
	}
}
