package wire_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestStoredMessageContent(t *testing.T) {
	raw := []byte(`{"type":"message","id":"msg_1","role":"user","content":[
		{"type":"input_text","text":"Question","prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"text","text":"Stored text"},
		{"type":"input_image","detail":"original","file_id":null,"prompt_cache_breakpoint":{"mode":"explicit"}},
		{"type":"computer_screenshot","detail":"auto","file_id":null,"image_url":null},
		{"type":"input_file","file_id":null,"filename":"report.pdf","file_url":"https://example.com/report.pdf","detail":"high"}
	]}`)
	want := generation.Message{ID: "msg_1", Role: generation.User, Parts: []generation.Part{
		generation.Text{Text: "Question", OpenAI: generation.OpenAITextData{PromptCacheBreakpoint: true}},
		generation.OpenAIStoredText{Text: "Stored text"},
		generation.OpenAIStoredImage{Kind: "input_image", Detail: "original", FileID: generation.Null[string](), PromptCacheBreakpoint: true},
		generation.OpenAIStoredImage{Kind: "computer_screenshot", Detail: "auto", FileID: generation.Null[string](), ImageURL: generation.Null[string]()},
		generation.OpenAIStoredFile{FileID: generation.Null[string](), Filename: generation.Some("report.pdf"), FileURL: generation.Some("https://example.com/report.pdf"), Detail: generation.Some("high")},
	}}

	item, err := wire.DecodeStoredItem(raw, nil)

	if err != nil || !reflect.DeepEqual(item, want) {
		t.Fatalf("stored item = %#v, %v; want %#v", item, err, want)
	}
}

func TestStoredMessageRolesAndOutput(t *testing.T) {
	for _, role := range []generation.Role{"unknown", "user", "assistant", "system", "critic", "discriminator", "developer", "tool"} {
		t.Run(string(role), func(t *testing.T) {
			raw := []byte(`{"type":"message","id":"msg_1","role":"` + string(role) + `","status":"completed","content":[{"type":"output_text","text":"Done","annotations":[],"logprobs":[]}],"phase":null}`)

			item, err := wire.DecodeStoredItem(raw, nil)

			if err != nil {
				t.Fatal(err)
			}
			want := generation.Message{
				ID: "msg_1", Role: role, Status: generation.ItemCompleted,
				OpenAI: generation.OpenAIMessageData{Phase: generation.Null[string]()},
				Parts:  []generation.Part{generation.Text{Text: "Done"}},
			}
			if !reflect.DeepEqual(item, want) {
				t.Fatalf("stored message = %#v; want %#v", item, want)
			}
		})
	}
}

func TestStoredCallProvenanceAndReplay(t *testing.T) {
	item, err := wire.DecodeStoredItem([]byte(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{","status":"in_progress","created_by":"creator_1"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := generation.ToolCall{ID: "fc_1", CallID: "call_1", Name: "lookup", Arguments: "{", Status: generation.ItemInProgress, OpenAI: generation.OpenAIToolCallData{CreatedBy: generation.Some("creator_1")}}
	if !reflect.DeepEqual(item, want) {
		t.Fatalf("stored call = %#v; want %#v", item, want)
	}

	body, err := wire.EncodeRequest(requestWith(item), false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{","status":"in_progress"}]}`)
}

func TestRejectMalformedStoredItems(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		kind      generation.FailureKind
	}{
		{name: "unknown", raw: `{"type":"future","id":"x"}`, kind: generation.Unsupported},
		{name: "missing ID", raw: `{"type":"message","role":"user","content":[]}`, kind: generation.ProtocolError},
		{name: "null status", raw: `{"type":"message","id":"m","role":"user","status":null,"content":[]}`, kind: generation.ProtocolError},
		{name: "invalid role", raw: `{"type":"message","id":"m","role":"future","status":"completed","content":[]}`, kind: generation.ProtocolError},
		{name: "null content", raw: `{"type":"message","id":"m","role":"user","content":null}`, kind: generation.ProtocolError},
		{name: "unknown content", raw: `{"type":"message","id":"m","role":"user","content":[{"type":"future"}]}`, kind: generation.Unsupported},
		{name: "null text", raw: `{"type":"message","id":"m","role":"user","content":[{"type":"input_text","text":null}]}`, kind: generation.ProtocolError},
		{name: "null breakpoint", raw: `{"type":"message","id":"m","role":"user","content":[{"type":"input_text","text":"x","prompt_cache_breakpoint":null}]}`, kind: generation.ProtocolError},
		{name: "missing image detail", raw: `{"type":"message","id":"m","role":"user","content":[{"type":"input_image"}]}`, kind: generation.ProtocolError},
		{name: "screenshot missing reference", raw: `{"type":"message","id":"m","role":"user","content":[{"type":"computer_screenshot","detail":"auto","file_id":null}]}`, kind: generation.ProtocolError},
		{name: "null file URL", raw: `{"type":"message","id":"m","role":"user","content":[{"type":"input_file","file_url":null}]}`, kind: generation.ProtocolError},
		{name: "call missing status", raw: `{"type":"function_call","id":"f","call_id":"c","name":"f","arguments":"{}"}`, kind: generation.ProtocolError},
		{name: "call null creator", raw: `{"type":"function_call","id":"f","call_id":"c","name":"f","status":"completed","arguments":"{}","created_by":null}`, kind: generation.ProtocolError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wire.DecodeStoredItem([]byte(tc.raw), nil)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != tc.kind {
				t.Fatalf("decode error = %v; want %s", err, tc.kind)
			}
		})
	}
}

func TestStoredCustomAndPatchProvenance(t *testing.T) {
	creator := generation.Some("creator_1")
	for _, tc := range []struct {
		name, raw string
		want      generation.Item
	}{
		{
			name: "custom output",
			raw:  `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","status":"completed","output":"done","created_by":"creator_1"}`,
			want: generation.CustomToolResult{ID: "out_1", CallID: "call_1", Status: generation.ItemCompleted, Output: generation.ToolTextOutput("done"), CreatedBy: creator},
		},
		{
			name: "patch call",
			raw:  `{"type":"apply_patch_call","id":"patch_1","call_id":"call_1","status":"completed","operation":{"type":"delete_file","path":"a.txt"},"created_by":"creator_1"}`,
			want: generation.OpenAIApplyPatchCall{ID: "patch_1", CallID: "call_1", Status: "completed", Operation: generation.PatchDeleteFile{Path: "a.txt"}, CreatedBy: creator},
		},
		{
			name: "patch output",
			raw:  `{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"completed","created_by":"creator_1"}`,
			want: generation.OpenAIApplyPatchResult{ID: "out_1", CallID: "call_1", Status: "completed", CreatedBy: creator},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item, err := wire.DecodeStoredItem([]byte(tc.raw), nil)

			if err != nil || !reflect.DeepEqual(item, tc.want) {
				t.Fatalf("stored item = %#v, %v; want %#v", item, err, tc.want)
			}
		})
	}
}
