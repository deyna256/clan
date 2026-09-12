package wire

import (
	"encoding/json"

	"github.com/deyna256/clan/internal/generation"
)

func decodeLoadedWebSearch(raw json.RawMessage, kind string) (generation.Tool, error) {
	var value struct {
		External generation.Optional[bool] `json:"external_web_access"`
		Filters  generation.Optional[struct {
			Allowed generation.Optional[[]string] `json:"allowed_domains"`
		}] `json:"filters"`
		Location generation.Optional[searchLocation] `json:"user_location"`
		Context  generation.Optional[string]         `json:"search_context_size"`
		Content  strictStrings                       `json:"search_content_types"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Context.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	context, hasContext := value.Context.Value()
	if hasContext && context == "" {
		return nil, failure(generation.ProtocolError)
	}
	tool := generation.OpenAIWebSearchTool{Version: kind, ExternalWebAccess: value.External, ContextSize: context, SearchContentTypes: []string(value.Content)}
	if value.Filters.IsNull() {
		tool.Filters = generation.Null[generation.WebSearchFilters]()
	}
	if filters, ok := value.Filters.Value(); ok {
		tool.Filters = generation.Some(generation.WebSearchFilters{AllowedDomains: filters.Allowed})
	}
	if value.Location.IsNull() {
		tool.Location = generation.Null[generation.SearchLocation]()
	}
	if location, ok := value.Location.Value(); ok {
		locationType, hasType := location.Type.Value()
		preview := kind == "web_search_preview" || kind == "web_search_preview_2025_03_11"
		if location.Type.IsNull() || (hasType && locationType != "approximate") || (preview && !hasType) {
			return nil, failure(generation.ProtocolError)
		}
		tool.Location = generation.Some(generation.SearchLocation{Type: location.Type, City: location.City, Country: location.Country, Region: location.Region, Timezone: location.Timezone})
	}
	return tool, nil
}

func decodeLoadedFileSearch(raw json.RawMessage) (generation.Tool, error) {
	var value struct {
		Stores  strictStrings              `json:"vector_store_ids"`
		Max     generation.Optional[int64] `json:"max_num_results"`
		Filter  json.RawMessage            `json:"filters"`
		Ranking generation.Optional[struct {
			Ranker generation.Optional[string]  `json:"ranker"`
			Score  generation.Optional[float64] `json:"score_threshold"`
			Hybrid generation.Optional[struct {
				Embedding *float64 `json:"embedding_weight"`
				Text      *float64 `json:"text_weight"`
			}] `json:"hybrid_search"`
		}] `json:"ranking_options"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Ranking.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	tool := generation.OpenAIFileSearchTool{VectorStoreIDs: []string(value.Stores), MaxResults: value.Max}
	if len(value.Filter) != 0 {
		if absent(value.Filter) {
			tool.Filter = generation.Null[generation.SearchFilter]()
		} else {
			filter, err := decodeLoadedSearchFilter(value.Filter, 0)
			if err != nil {
				return nil, err
			}
			tool.Filter = generation.Some(filter)
		}
	}
	if ranking, ok := value.Ranking.Value(); ok {
		if ranking.Ranker.IsNull() || ranking.Hybrid.IsNull() {
			return nil, failure(generation.ProtocolError)
		}
		ranker, hasRanker := ranking.Ranker.Value()
		if hasRanker && ranker == "" {
			return nil, failure(generation.ProtocolError)
		}
		decoded := generation.SearchRanking{Ranker: ranker, ScoreThreshold: ranking.Score}
		if hybrid, ok := ranking.Hybrid.Value(); ok {
			if hybrid.Embedding == nil || hybrid.Text == nil {
				return nil, failure(generation.ProtocolError)
			}
			decoded.Hybrid = generation.Some(generation.HybridSearch{EmbeddingWeight: *hybrid.Embedding, TextWeight: *hybrid.Text})
		}
		tool.Ranking = generation.Some(decoded)
	}
	return tool, nil
}

func decodeLoadedSearchFilter(raw json.RawMessage, depth int) (generation.SearchFilter, error) {
	if depth >= 64 {
		return nil, failure(generation.ProtocolError)
	}
	var value struct {
		Type    string            `json:"type"`
		Key     string            `json:"key"`
		Value   json.RawMessage   `json:"value"`
		Filters []json.RawMessage `json:"filters"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	if value.Type != "and" && value.Type != "or" {
		return generation.SearchComparison{Key: value.Key, Operator: value.Type, Value: value.Value}, nil
	}
	children := make([]generation.SearchFilter, 0, len(value.Filters))
	for _, child := range value.Filters {
		filter, err := decodeLoadedSearchFilter(child, depth+1)
		if err != nil {
			return nil, err
		}
		children = append(children, filter)
	}
	return generation.SearchCompound{Operator: value.Type, Filters: children}, nil
}
