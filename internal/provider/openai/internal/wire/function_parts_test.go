package wire_test

import (
	"reflect"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestFunctionContentPreservesNullableFieldsAndSources(t *testing.T) {
	const content = `[
		{"type":"input_text","text":"prefix","prompt_cache_breakpoint":null},
		{"type":"input_image","detail":null,"file_id":null,"image_url":"https://example.com/a.png","prompt_cache_breakpoint":null},
		{"type":"input_image","detail":"original","file_id":"file_image","image_url":"https://example.com/b.png","prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"input_file","file_id":"file_report","file_data":null,"file_url":null,"filename":null,"prompt_cache_breakpoint":null},
		{"type":"input_file","detail":"high","file_id":"file_report","file_data":"aGk=","file_url":"https://example.com/a.pdf","filename":"a.pdf"}
	]`
	item, err := wire.DecodeItem([]byte(`{"type":"function_call_output","id":"out_1","status":"completed","output":`+content+`}`), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := item.(generation.ToolResult).Output.(generation.ToolPartsOutput)
	want := generation.ToolPartsOutput{
		generation.OpenAIFunctionText{Text: "prefix", PromptCacheBreakpoint: generation.Null[generation.OpenAIPromptCacheBreakpoint]()},
		generation.OpenAIFunctionImage{Detail: generation.Null[string](), FileID: generation.Null[string](), ImageURL: generation.Some("https://example.com/a.png"), PromptCacheBreakpoint: generation.Null[generation.OpenAIPromptCacheBreakpoint]()},
		generation.OpenAIFunctionImage{Detail: generation.Some("original"), FileID: generation.Some("file_image"), ImageURL: generation.Some("https://example.com/b.png"), PromptCacheBreakpoint: generation.Some(generation.OpenAIPromptCacheBreakpoint{Mode: "explicit"})},
		generation.OpenAIFunctionFile{FileID: generation.Some("file_report"), FileData: generation.Null[string](), FileURL: generation.Null[string](), Filename: generation.Null[string](), PromptCacheBreakpoint: generation.Null[generation.OpenAIPromptCacheBreakpoint]()},
		generation.OpenAIFunctionFile{Detail: generation.Some("high"), FileID: generation.Some("file_report"), FileData: generation.Some("aGk="), FileURL: generation.Some("https://example.com/a.pdf"), Filename: generation.Some("a.pdf")},
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("decoded parts = %#v; want %#v", parts, want)
	}

	body, err := wire.EncodeRequest(requestWith(item), false)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"function_call_output","id":"out_1","status":"completed","output":`+content+`}]}`)
}

func TestFunctionContentRejectsInvalidFields(t *testing.T) {
	for _, content := range []string{
		`{"type":"input_text","text":null}`,
		`{"type":"input_image","file_id":"file_1","detail":"invalid"}`,
		`{"type":"input_file","file_id":"file_1","detail":null}`,
		`{"type":"input_file","file_id":"file_1","file_url":false}`,
		`{"type":"input_image","file_id":"file_1","image_url":"invalid"}`,
		`{"type":"input_file","file_id":"file_1","file_data":"invalid"}`,
	} {
		t.Run(content, func(t *testing.T) {
			_, err := wire.DecodeItem([]byte(`{"type":"function_call_output","id":"out_1","status":"completed","output":[`+content+`]}`), true, nil)
			if failure, ok := err.(*generation.Failure); !ok || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNullableFunctionPartsStayWithinFunctionResults(t *testing.T) {
	for _, part := range []generation.Part{
		generation.OpenAIFunctionText{Text: "x", PromptCacheBreakpoint: generation.Null[generation.OpenAIPromptCacheBreakpoint]()},
		generation.OpenAIFunctionImage{FileID: generation.Some("file_1"), Detail: generation.Null[string]()},
		generation.OpenAIFunctionFile{FileID: generation.Some("file_1"), Filename: generation.Null[string]()},
		generation.FileID{ID: "file_1", Options: generation.FileOptions{Filename: generation.Null[string]()}},
	} {
		for _, item := range []generation.Item{
			generation.Message{Role: generation.User, Parts: []generation.Part{part}},
			generation.CustomToolResult{CallID: "call_1", Output: generation.ToolPartsOutput{part}},
		} {
			body, err := wire.EncodeRequest(requestWith(item), false)
			assertCustomInputError(t, body, err)
		}
	}
	_, err := wire.DecodeItem([]byte(`{"type":"custom_tool_call_output","id":"out_1","call_id":"c","output":[{"type":"input_file","file_id":"f","filename":null}]}`), true, nil)
	if failure, ok := err.(*generation.Failure); !ok || failure.Kind != generation.ProtocolError {
		t.Fatalf("custom filename null error = %v", err)
	}
}

func TestFunctionContentRejectsInvalidReplayFields(t *testing.T) {
	for _, part := range []generation.Part{
		generation.OpenAIFunctionText{Text: "\xff"},
		generation.OpenAIFunctionText{Text: "x", PromptCacheBreakpoint: generation.Some(generation.OpenAIPromptCacheBreakpoint{Mode: "implicit"})},
		generation.OpenAIFunctionImage{FileID: generation.Some("file_1"), ImageURL: generation.Some("invalid")},
		generation.OpenAIFunctionFile{FileID: generation.Some("file_1"), FileData: generation.Some("invalid")},
		generation.OpenAIFunctionFile{FileID: generation.Some("file_1"), Filename: generation.Some("\xff")},
		generation.OpenAIFunctionFile{FileID: generation.Some("file_1"), Detail: generation.Null[string]()},
	} {
		body, err := wire.EncodeRequest(requestWith(generation.ToolResult{Output: generation.ToolPartsOutput{part}}), false)
		assertCustomInputError(t, body, err)
	}
}
