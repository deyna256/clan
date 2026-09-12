package wire_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestReturnedToolProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, raw, replay string
		want              generation.Item
	}{
		{
			name:   "patch call",
			raw:    `{"type":"apply_patch_call","id":"ap_1","call_id":"call_1","status":"completed","operation":{"type":"delete_file","path":"old.go"}`,
			replay: `{"type":"apply_patch_call","id":"ap_1","call_id":"call_1","status":"completed","operation":{"type":"delete_file","path":"old.go"}}`,
			want:   generation.OpenAIApplyPatchCall{ID: "ap_1", CallID: "call_1", Status: "completed", Operation: generation.PatchDeleteFile{Path: "old.go"}, CreatedBy: generation.Some("actor_1")},
		},
		{
			name:   "patch result",
			raw:    `{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"completed"`,
			replay: `{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"completed"}`,
			want:   generation.OpenAIApplyPatchResult{ID: "out_1", CallID: "call_1", Status: "completed", CreatedBy: generation.Some("actor_1")},
		},
		{
			name:   "custom result",
			raw:    `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","status":"completed","output":"done"`,
			replay: `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","output":"done"}`,
			want:   generation.CustomToolResult{ID: "out_1", CallID: "call_1", Status: "completed", Output: generation.ToolTextOutput("done"), CreatedBy: generation.Some("actor_1")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[` + tc.raw + `,"created_by":"actor_1"}]}`))
			if err != nil {
				t.Fatal(err)
			}

			result, err := envelope.Result(nil)

			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Response.Output, []generation.Item{tc.want}) {
				t.Fatalf("output=%#v", result.Response.Output)
			}

			body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)

			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, body, `{"model":"test-model","input":[`+tc.replay+`],"stream":false}`)
		})
	}
}

func TestRejectMalformedToolProvenance(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{name: "patch call", raw: `{"type":"apply_patch_call","id":"ap_1","call_id":"call_1","status":"completed","operation":{"type":"delete_file","path":"old.go"}`},
		{name: "patch result", raw: `{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"completed"`},
		{name: "custom result", raw: `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","status":"completed","output":"done"`},
	} {
		for _, invalid := range []struct{ name, raw string }{
			{name: "null creator", raw: `null`},
			{name: "boolean creator", raw: `false`},
		} {
			t.Run(tc.name+"/"+invalid.name, func(t *testing.T) {
				envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[` + tc.raw + `,"created_by":` + invalid.raw + `}]}`))
				if err != nil {
					t.Fatal(err)
				}

				result, err := envelope.Result(nil)

				var failure *generation.Failure
				if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError || len(result.Response.Output) != 0 {
					t.Fatalf("result = %#v, %v; want no output and protocol error", result, err)
				}
			})
		}
	}
}
