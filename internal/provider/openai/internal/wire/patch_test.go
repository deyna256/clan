package wire_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodePatchHistory(t *testing.T) {
	request := patchRequest()
	unchanged := patchRequest()

	body, err := wire.EncodeRequest(request, true)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":true,"tools":[{"type":"apply_patch","allowed_callers":["direct","programmatic"]}],"tool_choice":{"type":"apply_patch"},"input":[
		{"type":"apply_patch_call","id":"ap_1","call_id":"call_1","status":"completed","operation":{"type":"create_file","path":"../client/файл.go","diff":"+package main\n"},"caller":{"type":"program","caller_id":"program_1"}},
		{"type":"apply_patch_call","call_id":"call_2","status":"in_progress","operation":{"type":"update_file","path":"C:\\client\\main.go","diff":""}},
		{"type":"apply_patch_call","call_id":"call_3","status":"completed","operation":{"type":"delete_file","path":"/client/old.go"}},
		{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"completed","output":"created","caller":{"type":"program","caller_id":"program_1"}}
	]}`)
	require.Equal(t, unchanged, request)
}

func TestEncodePatchResultPresence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output generation.Optional[string]
		field  string
	}{
		{name: "omitted"},
		{name: "null", output: generation.Null[string](), field: `,"output":null`},
		{name: "empty", output: generation.Some(""), field: `,"output":""`},
		{name: "failure", output: generation.Some("not found"), field: `,"output":"not found"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(generation.OpenAIApplyPatchResult{CallID: "call_1", Status: "failed", Output: tc.output, Caller: generation.Null[generation.OpenAIToolCaller]()})

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"apply_patch_call_output","call_id":"call_1","status":"failed","caller":null`+tc.field+`}]}`)
		})
	}
}

func TestEncodePatchCallerSelection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		callers generation.Optional[[]string]
		field   string
	}{
		{name: "default"},
		{name: "empty", callers: generation.Some([]string{}), field: `,"allowed_callers":[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{generation.OpenAIApplyPatchTool{AllowedCallers: tc.callers}}

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[],"tools":[{"type":"apply_patch"`+tc.field+`}]}`)
		})
	}
}

func TestRejectInvalidPatchHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		item generation.Item
	}{
		{name: "call status", item: generation.OpenAIApplyPatchCall{CallID: "call_1", Status: "incomplete", Operation: generation.PatchDeleteFile{Path: "old.go"}}},
		{name: "call ID", item: generation.OpenAIApplyPatchCall{Status: "completed", Operation: generation.PatchDeleteFile{Path: "old.go"}}},
		{name: "missing operation", item: generation.OpenAIApplyPatchCall{CallID: "call_1", Status: "completed"}},
		{name: "empty path", item: generation.OpenAIApplyPatchCall{CallID: "call_1", Status: "completed", Operation: generation.PatchDeleteFile{}}},
		{name: "diff UTF-8", item: generation.OpenAIApplyPatchCall{CallID: "call_1", Status: "completed", Operation: generation.PatchUpdateFile{Path: "main.go", Diff: "private\xff"}}},
		{name: "path UTF-8", item: generation.OpenAIApplyPatchCall{CallID: "call_1", Status: "completed", Operation: generation.PatchDeleteFile{Path: "private\xff"}}},
		{name: "result status", item: generation.OpenAIApplyPatchResult{CallID: "call_1", Status: "in_progress"}},
		{name: "output UTF-8", item: generation.OpenAIApplyPatchResult{CallID: "call_1", Status: "failed", Output: generation.Some("private\xff")}},
		{name: "caller", item: generation.OpenAIApplyPatchResult{CallID: "call_1", Status: "failed", Caller: generation.Some(generation.OpenAIToolCaller{Type: "program"})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(tc.item)

			body, err := wire.EncodeRequest(request, false)

			var inputError *wire.InputError
			if body != nil || !errors.As(err, &inputError) {
				t.Fatalf("EncodeRequest = %s, %v; want input error", body, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("error leaked patch content")
			}
		})
	}
}

func patchRequest() generation.Request {
	caller := generation.Some(generation.OpenAIToolCaller{Type: "program", CallerID: "program_1"})
	request := requestWith(
		generation.OpenAIApplyPatchCall{ID: "ap_1", CallID: "call_1", Status: "completed", Operation: generation.PatchCreateFile{Path: "../client/файл.go", Diff: "+package main\n"}, Caller: caller},
		generation.OpenAIApplyPatchCall{CallID: "call_2", Status: "in_progress", Operation: generation.PatchUpdateFile{Path: `C:\client\main.go`}},
		generation.OpenAIApplyPatchCall{CallID: "call_3", Status: "completed", Operation: generation.PatchDeleteFile{Path: "/client/old.go"}},
		generation.OpenAIApplyPatchResult{ID: "out_1", CallID: "call_1", Status: "completed", Output: generation.Some("created"), Caller: caller},
	)
	request.Tools = []generation.Tool{generation.OpenAIApplyPatchTool{AllowedCallers: generation.Some([]string{"direct", "programmatic"})}}
	request.ToolChoice = generation.OpenAIApplyPatchChoice{}
	return request
}
