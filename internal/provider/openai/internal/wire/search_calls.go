package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type webAction struct {
	Type    string                      `json:"type"`
	Query   generation.Optional[string] `json:"query,omitzero"`
	Queries []string                    `json:"queries,omitempty"`
	Sources []webSource                 `json:"sources,omitempty"`
	URL     generation.Optional[string] `json:"url,omitzero"`
	Pattern generation.Optional[string] `json:"pattern,omitzero"`
}
type webSource struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}
type webSearchCall struct {
	Type   string     `json:"type"`
	ID     string     `json:"id"`
	Status string     `json:"status"`
	Action *webAction `json:"action,omitempty"`
}

func decodeWebSearchCall(raw json.RawMessage, warn func(string)) (generation.Item, error) {
	var call struct {
		ID     string          `json:"id"`
		Status string          `json:"status"`
		Action json.RawMessage `json:"action"`
	}
	if json.Unmarshal(raw, &call) != nil || call.ID == "" || !searchStatus(call.Status) {
		return nil, failure(generation.ProtocolError)
	}
	result := generation.OpenAIWebSearchCall{ID: call.ID, Status: call.Status}
	if absent(call.Action) {
		if call.Status == "completed" {
			return nil, failure(generation.ProtocolError)
		}
		return result, nil
	}
	var err error
	result.Action, err = decodeWebAction(call.Action, warn)
	return result, err
}

func decodeWebAction(raw json.RawMessage, warn func(string)) (generation.WebSearchAction, error) {
	var action struct {
		webAction
		Sources json.RawMessage `json:"sources"`
	}
	if json.Unmarshal(raw, &action) != nil {
		return nil, failure(generation.ProtocolError)
	}
	switch action.Type {
	case "search":
		var sources []string
		var records []json.RawMessage
		if !absent(action.Sources) && json.Unmarshal(action.Sources, &records) != nil {
			warn("invalid_search_sources")
		}
		if records != nil {
			sources = make([]string, 0, len(records))
		}
		for _, raw := range records {
			var source webSource
			if json.Unmarshal(raw, &source) != nil || source.Type != "url" || source.URL == "" {
				warn("invalid_search_sources")
				continue
			}
			sources = append(sources, source.URL)
		}
		return generation.WebSearch{Query: action.Query, Queries: action.Queries, Sources: sources}, nil
	case "open_page":
		return generation.WebOpenPage{URL: action.URL}, nil
	case "find_in_page":
		url, hasURL := action.URL.Value()
		pattern, hasPattern := action.Pattern.Value()
		if !hasURL || !hasPattern {
			return nil, failure(generation.ProtocolError)
		}
		return generation.WebFind{URL: url, Pattern: pattern}, nil
	default:
		return nil, failure(generation.Unsupported)
	}
}

func encodeWebSearchCall(call generation.OpenAIWebSearchCall) (json.RawMessage, error) {
	if err := requiredString("id", call.ID); err != nil {
		return nil, err
	}
	if !searchStatus(call.Status) {
		return nil, invalid("status", "unsupported search status")
	}
	action, err := encodeWebAction(call.Action)
	if err != nil {
		return nil, err
	}
	if action == nil && call.Status == "completed" {
		return nil, invalid("action", "completed search requires an action")
	}
	return json.Marshal(webSearchCall{Type: "web_search_call", ID: call.ID, Status: call.Status, Action: action})
}

func encodeWebAction(value generation.WebSearchAction) (*webAction, error) {
	var action webAction
	switch value := value.(type) {
	case nil:
		return nil, nil
	case generation.WebSearch:
		action.Type, action.Query, action.Queries = "search", value.Query, value.Queries
		for _, source := range value.Sources {
			action.Sources = append(action.Sources, webSource{"url", source})
		}
	case generation.WebOpenPage:
		action.Type, action.URL = "open_page", value.URL
	case generation.WebFind:
		action.Type, action.URL, action.Pattern = "find_in_page", generation.Some(value.URL), generation.Some(value.Pattern)
	default:
		return nil, invalid("action", "unsupported web action")
	}
	for _, text := range []generation.Optional[string]{action.Query, action.URL, action.Pattern} {
		if v, ok := text.Value(); ok && !utf8.ValidString(v) {
			return nil, invalid("action", "must be valid UTF-8")
		}
	}
	for _, query := range action.Queries {
		if !utf8.ValidString(query) {
			return nil, invalid("action.queries", "must be valid UTF-8")
		}
	}
	for _, source := range action.Sources {
		if !utf8.ValidString(source.URL) {
			return nil, invalid("action.sources", "must be valid UTF-8")
		}
	}
	return &action, nil
}

type fileSearchResult struct {
	ID         generation.Optional[string]                     `json:"file_id,omitzero"`
	Filename   generation.Optional[string]                     `json:"filename,omitzero"`
	Text       generation.Optional[string]                     `json:"text,omitzero"`
	Score      generation.Optional[float64]                    `json:"score,omitzero"`
	Attributes generation.Optional[map[string]json.RawMessage] `json:"attributes,omitzero"`
}
type fileSearchCall struct {
	Type    string                                  `json:"type"`
	ID      string                                  `json:"id"`
	Status  string                                  `json:"status"`
	Queries []string                                `json:"queries"`
	Results generation.Optional[[]fileSearchResult] `json:"results,omitzero"`
}

func decodeFileSearchCall(raw json.RawMessage, warn func(string)) (generation.Item, error) {
	var call struct {
		ID      string          `json:"id"`
		Status  string          `json:"status"`
		Queries []string        `json:"queries"`
		Results json.RawMessage `json:"results"`
	}
	if json.Unmarshal(raw, &call) != nil || call.ID == "" || !searchStatus(call.Status) {
		return nil, failure(generation.ProtocolError)
	}
	result := generation.OpenAIFileSearchCall{ID: call.ID, Status: call.Status, Queries: call.Queries}
	if len(call.Results) != 0 && absent(call.Results) {
		result.Results = generation.Null[[]generation.FileSearchResult]()
	}
	if !absent(call.Results) {
		var records []json.RawMessage
		if json.Unmarshal(call.Results, &records) != nil {
			warn("invalid_search_results")
			return result, nil
		}
		results := make([]generation.FileSearchResult, 0, len(records))
		for _, raw := range records {
			var record fileSearchResult
			if absent(raw) || json.Unmarshal(raw, &record) != nil || record.validate() != nil {
				warn("invalid_search_results")
				continue
			}
			results = append(results, generation.FileSearchResult{FileID: record.ID, Filename: record.Filename, Text: record.Text, Score: record.Score, Attributes: record.Attributes})
		}
		result.Results = generation.Some(results)
	}
	return result, nil
}

func encodeFileSearchCall(call generation.OpenAIFileSearchCall) (json.RawMessage, error) {
	if err := requiredString("id", call.ID); err != nil {
		return nil, err
	}
	if !searchStatus(call.Status) {
		return nil, invalid("status", "unsupported search status")
	}
	queries := call.Queries
	if queries == nil {
		queries = []string{}
	}
	for _, query := range queries {
		if !utf8.ValidString(query) {
			return nil, invalid("queries", "must be valid UTF-8")
		}
	}
	body := fileSearchCall{Type: "file_search_call", ID: call.ID, Status: call.Status, Queries: queries}
	if call.Results.IsNull() {
		body.Results = generation.Null[[]fileSearchResult]()
	}
	if records, ok := call.Results.Value(); ok {
		results := make([]fileSearchResult, 0, len(records))
		for _, record := range records {
			value := fileSearchResult{record.FileID, record.Filename, record.Text, record.Score, record.Attributes}
			if err := value.validate(); err != nil {
				return nil, err
			}
			results = append(results, value)
		}
		body.Results = generation.Some(results)
	}
	return json.Marshal(body)
}

func (r fileSearchResult) validate() error {
	for _, value := range []generation.Optional[string]{r.ID, r.Filename, r.Text} {
		if text, ok := value.Value(); ok && !utf8.ValidString(text) {
			return invalid("results", "must be valid UTF-8")
		}
	}
	if score, ok := r.Score.Value(); ok && (!finite(score) || score < 0 || score > 1) {
		return invalid("results.score", "must be between 0 and 1")
	}
	if attributes, ok := r.Attributes.Value(); ok {
		for key, value := range attributes {
			if !utf8.ValidString(key) || !filterValue(value, false) {
				return invalid("results.attributes", "expected UTF-8 keys and scalar values")
			}
		}
	}
	return nil
}

func searchStatus(status string) bool {
	switch status {
	case "in_progress", "searching", "completed", "incomplete", "failed":
		return true
	}
	return false
}
