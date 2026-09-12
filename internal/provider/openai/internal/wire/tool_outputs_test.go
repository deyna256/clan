package wire_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestToolOutputCacheBreakpointsAndReplay(t *testing.T) {
	breakpoint := generation.Some(generation.OpenAIPromptCacheBreakpoint{Mode: "explicit"})
	for _, tc := range []struct {
		kind         string
		want         generation.Item
		replayStatus string
	}{
		{
			kind: "function_call_output",
			want: generation.ToolResult{
				ID: generation.Some("out_1"), CallID: generation.Some("call_1"), Status: generation.Some(generation.ItemCompleted),
				Output: generation.ToolPartsOutput{
					generation.OpenAIFunctionText{Text: "prefix", PromptCacheBreakpoint: breakpoint},
					generation.OpenAIFunctionImage{ImageURL: generation.Some("https://example.com/image.png"), Detail: generation.Some("original"), PromptCacheBreakpoint: breakpoint},
					generation.OpenAIFunctionImage{FileID: generation.Some("file_image"), PromptCacheBreakpoint: breakpoint},
					generation.OpenAIFunctionFile{FileID: generation.Some("file_report"), PromptCacheBreakpoint: breakpoint},
				},
			},
			replayStatus: `,"status":"completed"`,
		},
		{
			kind: "custom_tool_call_output",
			want: generation.CustomToolResult{
				ID: "out_1", CallID: "call_1", Status: generation.ItemCompleted,
				Output: generation.ToolPartsOutput{
					generation.Text{Text: "prefix", OpenAI: generation.OpenAITextData{PromptCacheBreakpoint: true}},
					generation.ImageURL{URL: "https://example.com/image.png", Detail: "original", OpenAI: generation.OpenAIImageOptions{PromptCacheBreakpoint: true}},
					generation.ImageFile{FileID: "file_image", OpenAI: generation.OpenAIImageOptions{PromptCacheBreakpoint: true}},
					generation.FileID{ID: "file_report", Options: generation.FileOptions{OpenAI: generation.OpenAIFileOptions{PromptCacheBreakpoint: true}}},
				},
			},
		},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			content := `[
    {"type":"input_text","text":"prefix","prompt_cache_breakpoint":{"mode":"explicit"}},
    {"type":"input_image","image_url":"https://example.com/image.png","detail":"original","prompt_cache_breakpoint":{"mode":"explicit"}},
    {"type":"input_image","file_id":"file_image","prompt_cache_breakpoint":{"mode":"explicit"}},
    {"type":"input_file","file_id":"file_report","prompt_cache_breakpoint":{"mode":"explicit"}}
   ]`
			itemJSON := `{"type":"` + tc.kind + `","id":"out_1","call_id":"call_1","status":"completed","output":` + content + `}`
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[` + itemJSON + `]}`))
			if err != nil {
				t.Fatal(err)
			}

			result, err := envelope.Result(nil)

			if err != nil || !reflect.DeepEqual(result.Response.Output, []generation.Item{tc.want}) {
				t.Fatalf("output = %#v, %v; want %#v", result.Response.Output, err, tc.want)
			}

			body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)

			if err != nil {
				t.Fatal(err)
			}
			// Custom result status is returned metadata, absent from its input shape.
			expected := `{"type":"` + tc.kind + `","id":"out_1","call_id":"call_1"` + tc.replayStatus + `,"output":` + content + `}`
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[`+expected+`]}`)
		})
	}
}

func TestToolOutputRejectsMalformedCacheBreakpoints(t *testing.T) {
	invalid := []struct{ name, raw string }{
		{name: "missing mode", raw: `{}`},
		{name: "null mode", raw: `{"mode":null}`},
		{name: "implicit mode", raw: `{"mode":"implicit"}`},
		{name: "boolean breakpoint", raw: `false`},
	}
	for _, tc := range []struct {
		kind        string
		breakpoints []struct{ name, raw string }
	}{
		{kind: "function_call_output", breakpoints: invalid},
		{kind: "custom_tool_call_output", breakpoints: append([]struct{ name, raw string }{{name: "null breakpoint", raw: `null`}}, invalid...)},
	} {
		for _, part := range []struct{ name, raw string }{
			{name: "text", raw: `"type":"input_text","text":"prefix"`},
			{name: "image", raw: `"type":"input_image","file_id":"file_image"`},
			{name: "file", raw: `"type":"input_file","file_id":"file_report"`},
		} {
			for _, breakpoint := range tc.breakpoints {
				t.Run(tc.kind+"/"+part.name+"/"+breakpoint.name, func(t *testing.T) {
					item := `{"type":"` + tc.kind + `","id":"out_1","call_id":"call_1","status":"completed","output":[{` + part.raw + `,"prompt_cache_breakpoint":` + breakpoint.raw + `}]}`
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
