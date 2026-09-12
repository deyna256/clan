package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeInputOption(input generation.Optional[[]generation.Item]) (json.RawMessage, error) {
	return encodeOptional(input, func(items []generation.Item) (json.RawMessage, error) {
		encoded, err := EncodeInput(items)
		if err != nil {
			return nil, err
		}
		return json.Marshal(encoded)
	})
}

func EncodeCompact(request generation.CompactRequest) ([]byte, error) {
	if request.Model.IsZero() {
		return nil, invalid("model", "is required")
	}
	for _, field := range []struct {
		name  string
		value generation.Optional[string]
	}{
		{"model", request.Model}, {"instructions", request.Instructions}, {"previous_response_id", request.PreviousResponseID}, {"prompt_cache_key", request.PromptCacheKey},
	} {
		if value, present := field.value.Value(); present && !utf8.ValidString(value) {
			return nil, invalid(field.name, "must be valid UTF-8")
		}
	}
	if err := optionalEnum("prompt_cache_retention", request.PromptCacheRetention, true, "in_memory", "24h"); err != nil {
		return nil, err
	}
	if err := optionalEnum("service_tier", request.ServiceTier, true, "auto", "default", "fast", "flex", "priority"); err != nil {
		return nil, err
	}
	input, err := encodeInputOption(request.Input)
	if err != nil {
		return nil, at("input", err)
	}
	cache, err := encodeOptional(request.PromptCacheOptions, func(options generation.CompactCacheOptions) (json.RawMessage, error) {
		return encodePromptCacheOptions(generation.OpenAIPromptCacheOptions{Mode: options.Mode, TTL: options.TTL})
	})
	if err != nil {
		return nil, at("prompt_cache_options", err)
	}
	return json.Marshal(struct {
		Model                generation.Optional[string] `json:"model"`
		Input                json.RawMessage             `json:"input,omitempty"`
		Instructions         generation.Optional[string] `json:"instructions,omitzero"`
		PreviousResponseID   generation.Optional[string] `json:"previous_response_id,omitzero"`
		PromptCacheKey       generation.Optional[string] `json:"prompt_cache_key,omitzero"`
		PromptCacheRetention generation.Optional[string] `json:"prompt_cache_retention,omitzero"`
		PromptCacheOptions   json.RawMessage             `json:"prompt_cache_options,omitempty"`
		ServiceTier          generation.Optional[string] `json:"service_tier,omitzero"`
	}{request.Model, input, request.Instructions, request.PreviousResponseID, request.PromptCacheKey, request.PromptCacheRetention, cache, request.ServiceTier})
}

func EncodeInputTokenRequest(request generation.InputTokenRequest) ([]byte, error) {
	for _, field := range []struct {
		name  string
		value generation.Optional[string]
	}{
		{"model", request.Model}, {"instructions", request.Instructions}, {"previous_response_id", request.PreviousResponseID}, {"conversation", request.Conversation},
	} {
		if value, present := field.value.Value(); present && !utf8.ValidString(value) {
			return nil, invalid(field.name, "must be valid UTF-8")
		}
	}
	if _, present := request.Conversation.Value(); present {
		if _, prior := request.PreviousResponseID.Value(); prior {
			return nil, invalid("conversation", "cannot be combined with previous_response_id")
		}
	}
	if value, present := request.Personality.Value(); request.Personality.IsNull() || (present && (!utf8.ValidString(value) || utf8.RuneCountInString(value) > 64)) {
		return nil, invalid("personality", "must be a UTF-8 string of at most 64 characters")
	}
	if err := optionalEnum("truncation", request.Truncation, false, "auto", "disabled"); err != nil {
		return nil, err
	}
	input, err := encodeInputOption(request.Input)
	if err != nil {
		return nil, at("input", err)
	}
	reasoning, err := encodeOptional(request.Reasoning, encodeReasoningOptions)
	if err != nil {
		return nil, at("reasoning", err)
	}
	text, err := encodeOptional(request.Text, func(options generation.TextOptions) (json.RawMessage, error) {
		if err := optionalEnum("verbosity", options.Verbosity, true, "low", "medium", "high"); err != nil {
			return nil, err
		}
		format, err := encodeFormat(options.Format)
		if err != nil {
			return nil, at("format", err)
		}
		return json.Marshal(textOptions{Format: format, Verbosity: options.Verbosity})
	})
	if err != nil {
		return nil, at("text", err)
	}
	tools, err := encodeOptional(request.Tools, encodeToolCatalog)
	if err != nil {
		return nil, err
	}
	choice, err := encodeOptional(request.ToolChoice, func(choice generation.ToolChoice) (json.RawMessage, error) {
		if choice == nil {
			return nil, invalid("", "expected a tool choice or explicit null")
		}
		return encodeChoice(choice)
	})
	if err != nil {
		return nil, at("tool_choice", err)
	}
	return json.Marshal(struct {
		Model              generation.Optional[string] `json:"model,omitzero"`
		Conversation       generation.Optional[string] `json:"conversation,omitzero"`
		Instructions       generation.Optional[string] `json:"instructions,omitzero"`
		PreviousResponseID generation.Optional[string] `json:"previous_response_id,omitzero"`
		Input              json.RawMessage             `json:"input,omitempty"`
		Parallel           generation.Optional[bool]   `json:"parallel_tool_calls,omitzero"`
		Reasoning          json.RawMessage             `json:"reasoning,omitempty"`
		Text               json.RawMessage             `json:"text,omitempty"`
		Tools              json.RawMessage             `json:"tools,omitempty"`
		Choice             json.RawMessage             `json:"tool_choice,omitempty"`
		Personality        generation.Optional[string] `json:"personality,omitzero"`
		Truncation         generation.Optional[string] `json:"truncation,omitzero"`
	}{request.Model, request.Conversation, request.Instructions, request.PreviousResponseID, input, request.ParallelToolCalls, reasoning, text, tools, choice, request.Personality, request.Truncation})
}
