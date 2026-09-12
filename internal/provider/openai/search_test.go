package openai_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
)

func TestGeneratePreservesHostedSearchResults(t *testing.T) {
	client := testClient(t, staticJSON(searchResponse()), io.Discard)

	result, err := client.Generate(t.Context(), testAttempt(), textRequest())

	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Response.Output, expectedSearchOutput()) {
		t.Fatalf("output = %#v; want %#v", result.Response.Output, expectedSearchOutput())
	}
	if result.Response.Finish.Reason != "stop" {
		t.Fatalf("hosted search must not request client tool execution: %#v", result.Response.Finish)
	}
}

func TestStreamPreservesSearchResultsAndIgnoresProgress(t *testing.T) {
	var logs bytes.Buffer
	client := streamClient(t, &logs, created(),
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`,
		`{"type":"response.web_search_call.searching","output_index":0,"item_id":"ws_1"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fs_1","type":"file_search_call","status":"in_progress","queries":[],"results":null}}`,
		`{"type":"response.file_search_call.searching","output_index":1,"item_id":"fs_1"}`,
		`{"type":"response.completed","response":`+searchResponse()+`}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var items []generation.Item
	for _, event := range events {
		if ended, ok := event.(generation.ItemEnded); ok {
			items = append(items, ended.Item)
		}
	}
	if !reflect.DeepEqual(items, expectedSearchOutput()) || logs.Len() != 0 {
		t.Fatalf("items = %#v; logs = %s", items, logs.String())
	}
}

func TestGenerateKeepsAnswerWhenOptionalSearchMetadataIsMalformed(t *testing.T) {
	for _, results := range []string{`"invalid"`, `[{"score":2}]`, `[null]`} {
		t.Run(results, func(t *testing.T) {
			var logs bytes.Buffer
			body := `{"id":"resp_1","model":"test-model","status":"completed","output":[
				{"id":"fs_1","type":"file_search_call","status":"completed","queries":["q"],"results":` + results + `},
				{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","sources":[{"type":"new_source","payload":"secret"},{"type":"url","url":"https://example.com"}]}},
				{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Usable"}]}
			]}`
			client := testClient(t, staticJSON(body), &logs)

			result, err := client.Generate(t.Context(), testAttempt(), textRequest())

			if err != nil {
				t.Fatal(err)
			}
			text := result.Response.Output[2].(generation.Message).Parts[0].(generation.Text).Text
			sources := result.Response.Output[1].(generation.OpenAIWebSearchCall).Action.(generation.WebSearch).Sources
			if text != "Usable" || !reflect.DeepEqual(sources, []string{"https://example.com"}) {
				t.Fatalf("text = %q; sources = %q", text, sources)
			}
			if !strings.Contains(logs.String(), "invalid_search_results") || !strings.Contains(logs.String(), "invalid_search_sources") || strings.Contains(logs.String(), "secret") {
				t.Fatalf("logs = %s", logs.String())
			}
		})
	}
}

func TestStreamRetainsSearchSourcesOmittedFromFinalSnapshot(t *testing.T) {
	client := streamClient(t, io.Discard, created(),
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","queries":["q"],"sources":[{"type":"url","url":"https://example.com"}]}}}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"test-model","status":"completed","output":[{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","queries":["q"]}}]}}`,
	)

	events, err := readStream(t, client)

	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	var action generation.WebSearchAction
	for _, event := range events {
		if end, ok := event.(generation.ItemEnded); ok {
			action = end.Item.(generation.OpenAIWebSearchCall).Action
		}
	}
	want := generation.WebSearch{Queries: []string{"q"}, Sources: []string{"https://example.com"}}
	if !reflect.DeepEqual(action, want) {
		t.Fatalf("action = %#v; want %#v", action, want)
	}
}

func searchResponse() string {
	return `{"id":"resp_1","model":"test-model","status":"completed","output":[
		{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"find_in_page","url":"https://example.com","pattern":"result"}},
		{"id":"fs_1","type":"file_search_call","status":"completed","queries":["report"],"results":[{"file_id":"file_1","filename":null,"score":0,"attributes":{"revision":9007199254740993,"active":false}}]}
	]}`
}

func expectedSearchOutput() []generation.Item {
	return []generation.Item{
		generation.OpenAIWebSearchCall{ID: "ws_1", Status: "completed", Action: generation.WebFind{URL: "https://example.com", Pattern: "result"}},
		generation.OpenAIFileSearchCall{ID: "fs_1", Status: "completed", Queries: []string{"report"}, Results: generation.Some([]generation.FileSearchResult{{FileID: generation.Some("file_1"), Filename: generation.Null[string](), Score: generation.Some(0.0), Attributes: generation.Some(map[string]json.RawMessage{"revision": json.RawMessage(`9007199254740993`), "active": json.RawMessage(`false`)})}})},
	}
}
