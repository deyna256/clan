package wire_test

import (
	"errors"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestTerminalOutputRejectsConflictingIdentities(t *testing.T) {
	const call = `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}`
	for _, tc := range []struct{ name, first, second string }{
		{name: "same call ID different function", first: call, second: `{"type":"function_call","id":"fc_2","call_id":"call_1","name":"delete","arguments":"{}"}`},
		{name: "same item ID different function", first: call, second: `{"type":"function_call","id":"fc_1","call_id":"call_2","name":"delete","arguments":"{}"}`},
		{name: "duplicate call snapshot", first: call, second: call},
		{name: "case-insensitive identity", first: call, second: `{"type":"function_call","ID":"fc_1","CALL_ID":"call_2","name":"delete","arguments":"{}"}`},
		{name: "last duplicate identity", first: call, second: `{"type":"function_call","id":"fc_2","call_id":"call_2","call_id":"call_1","name":"delete","arguments":"{}"}`},
		{name: "different call families", first: call, second: `{"type":"custom_tool_call","id":"custom_1","call_id":"call_1","name":"other","input":"raw"}`},
		{name: "mismatched result family", first: call, second: `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","output":"done"}`},
		{name: "mismatched call after result", first: `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","output":"done"}`, second: call},
		{name: "call and result reuse item ID", first: call, second: `{"type":"function_call_output","status":"completed","id":"fc_1","call_id":"call_1","output":"done"}`},
		{name: "two results", first: `{"type":"function_call_output","status":"completed","id":"out_1","call_id":"call_1","output":"one"}`, second: `{"type":"function_call_output","status":"completed","id":"out_2","call_id":"call_1","output":"two"}`},
	} {
		for _, status := range []string{"completed", "incomplete"} {
			t.Run(tc.name+"/"+status, func(t *testing.T) {
				envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"model","status":"` + status + `","incomplete_details":{"reason":"max_output_tokens"},"output":[` + tc.first + `,` + tc.second + `]}`))
				if err != nil {
					t.Fatal(err)
				}

				result, err := envelope.Result(func(string) {})

				var failure *generation.Failure
				if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError || result.Response.Output != nil {
					t.Fatalf("result = %#v, %v; want protocol failure without partial output", result, err)
				}
			})
		}
	}
}

func TestTerminalOutputPreservesCallResultPairs(t *testing.T) {
	for _, tc := range []struct{ name, first, second string }{
		{name: "unknown extra correlation", first: `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[],"call_id":{}}`, second: `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}`},
		{name: "function", first: `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{}"}`, second: `{"type":"function_call_output","status":"completed","id":"out_1","call_id":"call_1","output":"done"}`},
		{name: "custom result before call", first: `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","output":"done"}`, second: `{"type":"custom_tool_call","id":"custom_1","call_id":"call_1","name":"lookup","input":"raw"}`},
		{name: "program", first: `{"type":"program","id":"p_1","call_id":"call_1","code":"return 1","fingerprint":"opaque"}`, second: `{"type":"program_output","id":"out_1","call_id":"call_1","result":"1","status":"completed"}`},
		{name: "client tool search", first: `{"type":"tool_search_call","id":"search_1","call_id":"call_1","execution":"client","status":"completed","arguments":{}}`, second: `{"type":"tool_search_output","id":"out_1","call_id":"call_1","execution":"client","status":"completed","tools":[]}`},
		{name: "uncorrelated results without call IDs", first: `{"type":"function_call_output","status":"completed","id":"out_1","output":"one"}`, second: `{"type":"function_call_output","status":"completed","id":"out_2","output":"two"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"model","status":"completed","output":[` + tc.first + `,` + tc.second + `]}`))
			if err != nil {
				t.Fatal(err)
			}

			result, err := envelope.Result(func(string) {})

			if err != nil || len(result.Response.Output) != 2 {
				t.Fatalf("result = %#v, %v; want both independent items", result, err)
			}
		})
	}
}

func TestEnvelopeDoesNotExposeRepairedIdentityAliases(t *testing.T) {
	for _, tc := range []struct{ name, identity string }{
		{name: "uppercase", identity: `"ID":"` + "\xff" + `"`},
		{name: "last duplicate", identity: `"id":"valid","id":"` + "\xff" + `"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{` + tc.identity + `,"model":"model","status":"completed","output":[],"usage":{"input_tokens":12}}`)

			envelope, err := wire.DecodeEnvelope(body)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError || envelope.ID != "" || envelope.Model != "model" || string(envelope.Usage) != `{"input_tokens":12}` {
				t.Fatalf("envelope = %#v, %v; want usage with no repaired ID", envelope, err)
			}
		})
	}
}
