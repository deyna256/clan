package wire

import (
	"encoding/json"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
	"github.com/tidwall/gjson"
)

type searchLocation struct {
	Type     generation.Optional[string] `json:"type,omitzero"`
	City     generation.Optional[string] `json:"city,omitzero"`
	Country  generation.Optional[string] `json:"country,omitzero"`
	Region   generation.Optional[string] `json:"region,omitzero"`
	Timezone generation.Optional[string] `json:"timezone,omitzero"`
}

func encodeWebSearchTool(tool generation.OpenAIWebSearchTool) (json.RawMessage, error) {
	version := tool.Version
	if version == "" {
		version = "web_search"
	}
	preview := version == "web_search_preview" || version == "web_search_preview_2025_03_11"
	if !preview && version != "web_search" && version != "web_search_2025_08_26" {
		return nil, invalid("type", "unsupported web search version")
	}
	if tool.ContextSize != "" && tool.ContextSize != "low" && tool.ContextSize != "medium" && tool.ContextSize != "high" {
		return nil, invalid("search_context_size", "unsupported context size")
	}
	if preview && (!tool.Filters.IsZero() || !tool.ExternalWebAccess.IsZero()) {
		return nil, invalid("", "preview does not support filters or external access options")
	}
	if !preview && tool.SearchContentTypes != nil {
		return nil, invalid("search_content_types", "only preview supports content types")
	}
	if tool.ExternalWebAccess.IsNull() {
		return nil, invalid("external_web_access", "must not be null")
	}
	for _, content := range tool.SearchContentTypes {
		if content != "text" && content != "image" {
			return nil, invalid("search_content_types", "unsupported content type")
		}
	}
	type webFilters struct {
		Allowed generation.Optional[[]string] `json:"allowed_domains,omitzero"`
	}
	var filters generation.Optional[webFilters]
	if tool.Filters.IsNull() {
		filters = generation.Null[webFilters]()
	}
	if value, ok := tool.Filters.Value(); ok {
		domains, _ := value.AllowedDomains.Value()
		for _, domain := range domains {
			if strings.TrimSpace(domain) == "" || !utf8.ValidString(domain) || strings.ContainsAny(domain, "/:@?# \t\n\r") {
				return nil, invalid("filters.allowed_domains", "expected domain names")
			}
		}
		filters = generation.Some(webFilters{Allowed: value.AllowedDomains})
	}
	var location generation.Optional[searchLocation]
	if tool.Location.IsNull() {
		location = generation.Null[searchLocation]()
	}
	if value, ok := tool.Location.Value(); ok {
		for _, field := range []generation.Optional[string]{value.City, value.Country, value.Region, value.Timezone} {
			if text, ok := field.Value(); ok && !utf8.ValidString(text) {
				return nil, invalid("user_location", "must be valid UTF-8")
			}
		}
		kind, present := value.Type.Value()
		if value.Type.IsNull() || (present && kind != "approximate") {
			return nil, invalid("user_location.type", "expected approximate")
		}
		locationType := value.Type
		if preview && !present {
			locationType = generation.Some("approximate")
		}
		location = generation.Some(searchLocation{locationType, value.City, value.Country, value.Region, value.Timezone})
	}
	return json.Marshal(struct {
		Type     string                              `json:"type"`
		External generation.Optional[bool]           `json:"external_web_access,omitzero"`
		Filters  generation.Optional[webFilters]     `json:"filters,omitzero"`
		Location generation.Optional[searchLocation] `json:"user_location,omitzero"`
		Context  string                              `json:"search_context_size,omitempty"`
		Content  []string                            `json:"search_content_types,omitzero"`
	}{version, tool.ExternalWebAccess, filters, location, tool.ContextSize, tool.SearchContentTypes})
}

type searchHybrid struct {
	Embedding float64 `json:"embedding_weight"`
	Text      float64 `json:"text_weight"`
}
type searchRanking struct {
	Ranker string                            `json:"ranker,omitempty"`
	Score  generation.Optional[float64]      `json:"score_threshold,omitzero"`
	Hybrid generation.Optional[searchHybrid] `json:"hybrid_search,omitzero"`
}

func encodeFileSearchTool(tool generation.OpenAIFileSearchTool) (json.RawMessage, error) {
	if len(tool.VectorStoreIDs) == 0 {
		return nil, invalid("vector_store_ids", "at least one store is required")
	}
	for _, id := range tool.VectorStoreIDs {
		if err := requiredString("vector_store_ids", id); err != nil {
			return nil, err
		}
	}
	if tool.MaxResults.IsNull() {
		return nil, invalid("max_num_results", "must not be null")
	}
	if max, ok := tool.MaxResults.Value(); ok && (max < 1 || max > 50) {
		return nil, invalid("max_num_results", "must be between 1 and 50")
	}
	var filter json.RawMessage
	if tool.Filter.IsNull() {
		filter = json.RawMessage(`null`)
	}
	if value, ok := tool.Filter.Value(); ok {
		if value == nil {
			return nil, invalid("filters", "expected a filter value")
		}
		var err error
		filter, err = encodeSearchFilter(value, 0)
		if err != nil {
			return nil, at("filters", err)
		}
	}
	if tool.Ranking.IsNull() {
		return nil, invalid("ranking_options", "must not be null")
	}
	var ranking generation.Optional[searchRanking]
	if value, ok := tool.Ranking.Value(); ok {
		if value.Ranker != "" && value.Ranker != "auto" && value.Ranker != "default-2024-11-15" {
			return nil, invalid("ranking_options.ranker", "unsupported ranker")
		}
		if value.ScoreThreshold.IsNull() || value.Hybrid.IsNull() {
			return nil, invalid("ranking_options", "ranking fields must not be null")
		}
		if score, ok := value.ScoreThreshold.Value(); ok && (!finite(score) || score < 0 || score > 1) {
			return nil, invalid("ranking_options.score_threshold", "must be between 0 and 1")
		}
		var hybrid generation.Optional[searchHybrid]
		if weights, ok := value.Hybrid.Value(); ok {
			if !finite(weights.EmbeddingWeight) || !finite(weights.TextWeight) || weights.EmbeddingWeight < 0 || weights.TextWeight < 0 {
				return nil, invalid("ranking_options.hybrid_search", "weights must be finite and nonnegative")
			}
			hybrid = generation.Some(searchHybrid{weights.EmbeddingWeight, weights.TextWeight})
		}
		ranking = generation.Some(searchRanking{value.Ranker, value.ScoreThreshold, hybrid})
	}
	return json.Marshal(struct {
		Type    string                             `json:"type"`
		Stores  []string                           `json:"vector_store_ids"`
		Max     generation.Optional[int64]         `json:"max_num_results,omitzero"`
		Filter  json.RawMessage                    `json:"filters,omitempty"`
		Ranking generation.Optional[searchRanking] `json:"ranking_options,omitzero"`
	}{"file_search", tool.VectorStoreIDs, tool.MaxResults, filter, ranking})

}

// Bound nesting before following caller-owned slices, which may contain cycles.
func encodeSearchFilter(filter generation.SearchFilter, depth int) (json.RawMessage, error) {
	if depth >= 64 {
		return nil, invalid("", "filter nesting exceeds 64 levels")
	}
	switch value := filter.(type) {
	case nil:
		return nil, nil
	case generation.SearchComparison:
		if err := requiredString("key", value.Key); err != nil {
			return nil, err
		}
		switch value.Operator {
		case "eq", "ne", "gt", "gte", "lt", "lte", "in", "nin":
		default:
			return nil, invalid("type", "unsupported comparison operator")
		}
		if !filterValue(value.Value, value.Operator == "in" || value.Operator == "nin") {
			return nil, invalid("value", "expected a scalar or membership array")
		}
		return json.Marshal(struct {
			Type  string          `json:"type"`
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}{value.Operator, value.Key, value.Value})
	case generation.SearchCompound:
		if value.Operator != "and" && value.Operator != "or" {
			return nil, invalid("type", "expected and or or")
		}
		if len(value.Filters) == 0 {
			return nil, invalid("filters", "at least one filter is required")
		}
		var filters []json.RawMessage
		for _, filter := range value.Filters {
			if filter == nil {
				return nil, invalid("filters", "filter must not be null")
			}
			raw, err := encodeSearchFilter(filter, depth+1)
			if err != nil {
				return nil, err
			}
			filters = append(filters, raw)
		}
		return json.Marshal(struct {
			Type    string            `json:"type"`
			Filters []json.RawMessage `json:"filters"`
		}{value.Operator, filters})
	default:
		return nil, invalid("", "unsupported filter value")
	}
}

func filterValue(raw json.RawMessage, membership bool) bool {
	if !utf8.Valid(raw) || !gjson.ValidBytes(raw) {
		return false
	}
	value := gjson.ParseBytes(raw)
	if !membership {
		return value.Type == gjson.String || value.Type == gjson.Number || value.Type == gjson.True || value.Type == gjson.False
	}
	if !value.IsArray() {
		return false
	}
	valid := true
	value.ForEach(func(_, item gjson.Result) bool {
		valid = item.Type == gjson.String || item.Type == gjson.Number
		return valid
	})
	return valid
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
