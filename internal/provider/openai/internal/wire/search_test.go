package wire_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestEncodeSearchTools(t *testing.T) {
	request := searchRequest()
	unchanged := searchRequest()

	body, err := wire.EncodeRequest(request, true)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":true,"top_logprobs":0,"tools":[
		{"type":"web_search","external_web_access":false,"filters":{"allowed_domains":["example.com"]},"user_location":{"type":"approximate","country":"GB","city":null},"search_context_size":"high"},
		{"type":"file_search","vector_store_ids":["vs_1"],"max_num_results":50,"filters":{"type":"and","filters":[{"type":"eq","key":"active","value":false},{"type":"in","key":"revision","value":[9007199254740993,"latest"]}]},"ranking_options":{"ranker":"auto","score_threshold":0,"hybrid_search":{"embedding_weight":0,"text_weight":1}}}
	],"tool_choice":{"type":"file_search"}}`)
	if !reflect.DeepEqual(request, unchanged) {
		t.Fatal("encoding changed search configuration")
	}
}

func TestEncodeSearchHistory(t *testing.T) {
	request := requestWith(
		generation.OpenAIWebSearchCall{ID: "ws_1", Status: "completed", Action: generation.WebSearch{Query: generation.Some("old"), Queries: []string{"new"}, Sources: []string{"https://example.com"}}},
		generation.OpenAIWebSearchCall{ID: "ws_2", Status: "completed", Action: generation.WebOpenPage{URL: generation.Null[string]()}},
		generation.OpenAIFileSearchCall{
			ID:      "fs_1",
			Status:  "completed",
			Queries: []string{"report"},
			Results: generation.Some([]generation.FileSearchResult{{
				FileID:     generation.Some("file_1"),
				Text:       generation.Null[string](),
				Score:      generation.Some(0.0),
				Attributes: generation.Some(map[string]json.RawMessage{"revision": json.RawMessage(`9007199254740993`), "active": json.RawMessage(`false`)}),
			}}),
		},
	)

	body, err := wire.EncodeRequest(request, false)

	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"old","queries":["new"],"sources":[{"type":"url","url":"https://example.com"}]}},
		{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"open_page","url":null}},
		{"type":"file_search_call","id":"fs_1","status":"completed","queries":["report"],"results":[{"file_id":"file_1","text":null,"score":0,"attributes":{"revision":9007199254740993,"active":false}}]}
	]}`)
}

func TestRejectInvalidAndCyclicFilters(t *testing.T) {
	cycle := make([]generation.SearchFilter, 1)
	cycle[0] = generation.SearchCompound{Operator: "and", Filters: cycle}
	for _, tc := range []struct {
		name   string
		filter generation.SearchFilter
	}{
		{name: "object value", filter: generation.SearchComparison{Operator: "eq", Key: "k", Value: json.RawMessage(`{"anything":1}`)}},
		{name: "invalid membership", filter: generation.SearchComparison{Operator: "in", Key: "k", Value: json.RawMessage(`[true]`)}},
		{name: "unknown operator", filter: generation.SearchComparison{Operator: "other", Key: "k", Value: json.RawMessage(`1`)}},
		{name: "empty compound", filter: generation.SearchCompound{Operator: "and"}},
		{name: "cycle", filter: cycle[0]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.Tools = []generation.Tool{generation.OpenAIFileSearchTool{VectorStoreIDs: []string{"vs_1"}, Filter: generation.Some(tc.filter)}}

			body, err := wire.EncodeRequest(request, false)

			if err == nil || body != nil {
				t.Fatalf("EncodeRequest = %s, %v", body, err)
			}
		})
	}
}

func searchRequest() generation.Request {
	request := requestWith()
	request.OpenAI.TopLogprobs = generation.Some(int64(0))
	request.Tools = []generation.Tool{
		generation.OpenAIWebSearchTool{
			ExternalWebAccess: generation.Some(false),
			Filters:           generation.Some(generation.WebSearchFilters{AllowedDomains: generation.Some([]string{"example.com"})}),
			ContextSize:       "high",
			Location:          generation.Some(generation.SearchLocation{Type: generation.Some("approximate"), Country: generation.Some("GB"), City: generation.Null[string]()}),
		},
		generation.OpenAIFileSearchTool{VectorStoreIDs: []string{"vs_1"}, MaxResults: generation.Some(int64(50)), Filter: generation.Some[generation.SearchFilter](generation.SearchCompound{Operator: "and", Filters: []generation.SearchFilter{
			generation.SearchComparison{Key: "active", Operator: "eq", Value: json.RawMessage(`false`)}, generation.SearchComparison{Key: "revision", Operator: "in", Value: json.RawMessage(`[9007199254740993,"latest"]`)},
		}}), Ranking: generation.Some(generation.SearchRanking{Ranker: "auto", ScoreThreshold: generation.Some(0.0), Hybrid: generation.Some(generation.HybridSearch{TextWeight: 1})})},
	}
	request.ToolChoice = generation.OpenAISearchChoice("file_search")
	return request
}
