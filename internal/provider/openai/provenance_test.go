package openai_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/stretchr/testify/require"
)

func TestStreamRetainsToolProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      generation.Item
	}{
		{name: "patch call", raw: `{"type":"apply_patch_call","id":"ap_1","call_id":"call_1","status":"completed","operation":{"type":"delete_file","path":"old.go"}`, want: generation.OpenAIApplyPatchCall{ID: "ap_1", CallID: "call_1", Status: "completed", Operation: generation.PatchDeleteFile{Path: "old.go"}, CreatedBy: generation.Some("actor_1")}},
		{name: "patch result", raw: `{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"completed"`, want: generation.OpenAIApplyPatchResult{ID: "out_1", CallID: "call_1", Status: "completed", CreatedBy: generation.Some("actor_1")}},
		{name: "custom result", raw: `{"type":"custom_tool_call_output","id":"out_1","call_id":"call_1","status":"completed","output":"done"`, want: generation.CustomToolResult{ID: "out_1", CallID: "call_1", Status: "completed", Output: generation.ToolTextOutput("done"), CreatedBy: generation.Some("actor_1")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, last := range []struct {
				name, field string
				valid       bool
			}{
				{name: "retains omitted", valid: true},
				{name: "accepts same", field: `,"created_by":"actor_1"`, valid: true},
				{name: "rejects changed", field: `,"created_by":"actor_2"`},
			} {
				t.Run(last.name, func(t *testing.T) {
					client := streamClient(t, io.Discard, created(), outputItemAdded(0, tc.raw+`,"created_by":"actor_1"}`), `{"type":"response.completed","response":`+responseWithItems(tc.raw+last.field+`}`)+`}`)

					events, err := readStream(t, client)

					if !last.valid {
						assertProtocolError(t, err)
						assertNoResponseEnd(t, events)
						return
					}
					if !errors.Is(err, io.EOF) {
						t.Fatal(err)
					}
					var ended generation.Item
					for _, event := range events {
						if item, ok := event.(generation.ItemEnded); ok {
							ended = item.Item
						}
					}
					require.Equal(t, tc.want, ended)
				})
			}
			t.Run("bounds creator", func(t *testing.T) {
				initial := tc.raw + `,"created_by":"` + strings.Repeat("x", 600_000) + `"}`
				second := strings.Replace(initial, `"id":"`, `"id":"second_`, 1)
				client := streamClient(t, io.Discard, created(), outputItemAdded(0, initial), outputItemAdded(1, second))

				events, err := readStream(t, client)

				assertProtocolError(t, err)
				assertNoResponseEnd(t, events)
			})
		})
	}
}
