package wire_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestToolOutputCacheBreakpointsRoundTrip(t *testing.T) {
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		t.Run(kind, func(t *testing.T) {
			itemJSON := `{"type":"` + kind + `","id":"out_1","call_id":"call_1","status":"completed","output":[
				{"type":"input_text","text":"prefix","prompt_cache_breakpoint":{"mode":"explicit"}},
				{"type":"input_image","image_url":"https://example.com/image.png","detail":"original","prompt_cache_breakpoint":{"mode":"explicit"}},
				{"type":"input_image","file_id":"file_image","prompt_cache_breakpoint":{"mode":"explicit"}},
				{"type":"input_file","file_id":"file_report","prompt_cache_breakpoint":{"mode":"explicit"}}
			]}`
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[` + itemJSON + `]}`))
			if err != nil {
				t.Fatal(err)
			}
			result, err := envelope.Result(nil)
			if err != nil {
				t.Fatal(err)
			}
			body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)
			if err != nil {
				t.Fatal(err)
			}
			expected := itemJSON
			if kind == "custom_tool_call_output" {
				// Custom result status is returned metadata, absent from its input shape.
				expected = strings.Replace(expected, `,"status":"completed"`, "", 1)
			}
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[`+expected+`]}`)
		})
	}
}

func TestToolOutputRejectsMalformedCacheBreakpoints(t *testing.T) {
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		for _, part := range []string{`"type":"input_text","text":"prefix"`, `"type":"input_image","file_id":"file_image"`, `"type":"input_file","file_id":"file_report"`} {
			for _, breakpoint := range []string{`null`, `{}`, `{"mode":null}`, `{"mode":"implicit"}`, `false`} {
				if kind == "function_call_output" && breakpoint == "null" {
					continue // ResponseInput*Content permits null; custom content does not.
				}
				t.Run(kind+"/"+part+"/"+breakpoint, func(t *testing.T) {
					item := `{"type":"` + kind + `","id":"out_1","call_id":"call_1","status":"completed","output":[{` + part + `,"prompt_cache_breakpoint":` + breakpoint + `}]}`
					envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[` + item + `]}`))
					if err != nil {
						t.Fatal(err)
					}
					result, err := envelope.Result(nil)
					var failure *generation.Failure
					if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError || len(result.Response.Output) != 0 {
						t.Fatalf("result = %#v, %v", result, err)
					}
				})
			}
		}
	}
}
