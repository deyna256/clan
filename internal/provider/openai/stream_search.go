package openai

import "github.com/deyna256/clan/internal/generation"

func retainSearchAction(previous, next generation.WebSearchAction) generation.WebSearchAction {
	if next == nil {
		return previous
	}
	switch value := next.(type) {
	case generation.WebSearch:
		if prior, ok := previous.(generation.WebSearch); ok {
			if value.Query.IsZero() {
				value.Query = prior.Query
			}
			if value.Queries == nil {
				value.Queries = prior.Queries
			}
			if value.Sources == nil {
				value.Sources = prior.Sources
			}
		}
		return value
	case generation.WebOpenPage:
		if prior, ok := previous.(generation.WebOpenPage); ok && value.URL.IsZero() {
			value.URL = prior.URL
		}
		return value
	default:
		return next
	}
}

func startItem(item generation.Item) generation.Item {
	switch value := item.(type) {
	case generation.OpenAIImageGenerationCall:
		return generation.OpenAIImageGenerationCall{ID: value.ID, Status: value.Status}
	case generation.OpenAIWebSearchCall:
		return generation.OpenAIWebSearchCall{ID: value.ID, Status: value.Status}
	case generation.OpenAIFileSearchCall:
		return generation.OpenAIFileSearchCall{ID: value.ID, Status: value.Status}
	default:
		return emptyItem(item)
	}
}

func webSearchBytes(call generation.OpenAIWebSearchCall) int {
	size := len(call.ID)
	switch action := call.Action.(type) {
	case generation.WebSearch:
		query, _ := action.Query.Value()
		size += len(query)
		for _, query := range action.Queries {
			size += 16 + len(query)
		}
		for _, source := range action.Sources {
			size += 16 + len(source)
		}
	case generation.WebOpenPage:
		url, _ := action.URL.Value()
		size += len(url)
	case generation.WebFind:
		size += len(action.URL) + len(action.Pattern)
	}
	return size
}

func fileSearchBytes(call generation.OpenAIFileSearchCall) int {
	size := len(call.ID)
	for _, query := range call.Queries {
		size += 16 + len(query)
	}
	if results, ok := call.Results.Value(); ok {
		for _, result := range results {
			id, _ := result.FileID.Value()
			filename, _ := result.Filename.Value()
			text, _ := result.Text.Value()
			size += 128 + len(id) + len(filename) + len(text)
			if attributes, ok := result.Attributes.Value(); ok {
				for key, value := range attributes {
					size += 64 + len(key) + len(value)
				}
			}
		}
	}
	return size
}
