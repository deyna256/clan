package wire

import (
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type requestOptions struct {
	Metadata          json.RawMessage             `json:"metadata,omitempty"`
	Conversation      generation.Optional[string] `json:"conversation,omitzero"`
	Prompt            json.RawMessage             `json:"prompt,omitempty"`
	Moderation        json.RawMessage             `json:"moderation,omitempty"`
	ContextManagement json.RawMessage             `json:"context_management,omitempty"`
	PromptCache       json.RawMessage             `json:"prompt_cache_options,omitempty"`
	StreamOptions     json.RawMessage             `json:"stream_options,omitempty"`
	Reasoning         json.RawMessage             `json:"reasoning,omitempty"`
	Include           json.RawMessage             `json:"include,omitempty"`
	User              generation.Optional[string] `json:"user,omitzero"`
}

func encodeRequestOptions(options generation.OpenAIOptions, streaming bool) (requestOptions, error) {
	result := requestOptions{Conversation: options.Conversation, User: options.User}
	if conversation, present := options.Conversation.Value(); present {
		if err := requiredString("conversation", conversation); err != nil {
			return result, err
		}
		if _, present := options.PreviousResponseID.Value(); present {
			return result, invalid("conversation", "cannot be combined with previous_response_id")
		}
	}
	if value, present := options.User.Value(); options.User.IsNull() || (present && !utf8.ValidString(value)) {
		return result, invalid("user", "must be a UTF-8 string")
	}
	if options.PromptCacheOptions.IsNull() {
		return result, invalid("prompt_cache_options", "must be an object")
	}
	if _, present := options.StreamOptions.Value(); present && !streaming {
		return result, invalid("stream_options", "requires streaming")
	}
	var err error
	if result.Metadata, err = encodeOptional(options.Metadata, EncodeMetadata); err != nil {
		return result, at("metadata", err)
	}
	if result.Prompt, err = encodeOptional(options.Prompt, encodePrompt); err != nil {
		return result, at("prompt", err)
	}
	if result.Moderation, err = encodeOptional(options.Moderation, encodeModeration); err != nil {
		return result, at("moderation", err)
	}
	if result.ContextManagement, err = encodeOptional(options.ContextManagement, encodeContextManagement); err != nil {
		return result, at("context_management", err)
	}
	if result.PromptCache, err = encodeOptional(options.PromptCacheOptions, encodePromptCacheOptions); err != nil {
		return result, at("prompt_cache_options", err)
	}
	if result.StreamOptions, err = encodeOptional(options.StreamOptions, encodeStreamOptions); err != nil {
		return result, at("stream_options", err)
	}
	if result.Reasoning, err = encodeOptional(options.Reasoning, encodeReasoningOptions); err != nil {
		return result, at("reasoning", err)
	}
	if result.Include, err = encodeOptional(options.Include, encodeInclude); err != nil {
		return result, at("include", err)
	}
	return result, nil
}

func encodeOptional[T any](option generation.Optional[T], encode func(T) (json.RawMessage, error)) (json.RawMessage, error) {
	if option.IsZero() {
		return nil, nil
	}
	value, present := option.Value()
	if !present {
		return json.RawMessage("null"), nil
	}
	return encode(value)
}

// EncodeMetadata validates metadata shared by Responses and resource requests.
func EncodeMetadata(metadata map[string]string) (json.RawMessage, error) {
	if len(metadata) > 16 {
		return nil, invalid("", "must have at most 16 entries")
	}
	for key, value := range metadata {
		if !utf8.ValidString(key) || utf8.RuneCountInString(key) > 64 {
			return nil, invalid("", "keys must be UTF-8 strings of at most 64 characters")
		}
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 512 {
			return nil, invalid("", "values must be UTF-8 strings of at most 512 characters")
		}
	}
	if metadata == nil {
		return json.RawMessage("{}"), nil
	}
	return json.Marshal(metadata)
}

func encodePrompt(prompt generation.OpenAIPrompt) (json.RawMessage, error) {
	if err := requiredString("id", prompt.ID); err != nil {
		return nil, err
	}
	if version, present := prompt.Version.Value(); present && !utf8.ValidString(version) {
		return nil, invalid("version", "must be valid UTF-8")
	}
	variables, err := encodeOptional(prompt.Variables, encodePromptVariables)
	if err != nil {
		return nil, at("variables", err)
	}
	return json.Marshal(struct {
		ID        string                      `json:"id"`
		Version   generation.Optional[string] `json:"version,omitzero"`
		Variables json.RawMessage             `json:"variables,omitempty"`
	}{prompt.ID, prompt.Version, variables})
}

func encodePromptVariables(variables map[string]generation.PromptVariable) (json.RawMessage, error) {
	encoded := make(map[string]json.RawMessage, len(variables))
	for name, variable := range variables {
		if !utf8.ValidString(name) {
			return nil, invalid("", "names must be valid UTF-8")
		}
		var value json.RawMessage
		var err error
		switch part := variable.(type) {
		case generation.PromptString:
			if !utf8.ValidString(string(part)) {
				return nil, invalid("", "strings must be valid UTF-8")
			}
			value, err = json.Marshal(string(part))
		case generation.Text, generation.ImageURL, generation.ImageFile, generation.FileID, generation.FileURL, generation.FileData:
			value, err = encodePart(part.(generation.Part), false)
		default:
			return nil, invalid("", "expected a string or input text, image, or file part")
		}
		if err != nil {
			return nil, err
		}
		encoded[name] = value
	}
	return json.Marshal(encoded)
}

func encodeModeration(options generation.OpenAIModerationOptions) (json.RawMessage, error) {
	if err := requiredString("model", options.Model); err != nil {
		return nil, err
	}
	policy, err := encodeOptional(options.Policy, encodeModerationPolicy)
	if err != nil {
		return nil, at("policy", err)
	}
	return json.Marshal(struct {
		Model  string          `json:"model"`
		Policy json.RawMessage `json:"policy,omitempty"`
	}{options.Model, policy})
}

func encodeModerationPolicy(policy generation.OpenAIModerationPolicy) (json.RawMessage, error) {
	input, err := encodeOptional(policy.Input, encodeModerationRule)
	if err != nil {
		return nil, at("input", err)
	}
	output, err := encodeOptional(policy.Output, encodeModerationRule)
	if err != nil {
		return nil, at("output", err)
	}
	return json.Marshal(struct {
		Input  json.RawMessage `json:"input,omitempty"`
		Output json.RawMessage `json:"output,omitempty"`
	}{input, output})
}

func encodeModerationRule(rule generation.OpenAIModerationRule) (json.RawMessage, error) {
	if rule.Mode != "score" && rule.Mode != "block" {
		return nil, invalid("mode", "expected score or block")
	}
	return json.Marshal(struct {
		Mode string `json:"mode"`
	}{rule.Mode})
}

func encodeContextManagement(entries []generation.OpenAIContextManagement) (json.RawMessage, error) {
	type entry struct {
		Type             string                     `json:"type"`
		CompactThreshold generation.Optional[int64] `json:"compact_threshold,omitzero"`
	}
	encoded := make([]entry, 0, len(entries))
	for i, value := range entries {
		if value.Type != "compaction" {
			return nil, invalid(fmt.Sprintf("[%d].type", i), "expected compaction")
		}
		encoded = append(encoded, entry{value.Type, value.CompactThreshold})
	}
	return json.Marshal(encoded)
}

func encodePromptCacheOptions(options generation.OpenAIPromptCacheOptions) (json.RawMessage, error) {
	if err := optionalEnum("mode", options.Mode, false, "implicit", "explicit"); err != nil {
		return nil, err
	}
	if err := optionalEnum("ttl", options.TTL, false, "30m"); err != nil {
		return nil, err
	}
	if id, present := options.ComparisonResponseID.Value(); present && !utf8.ValidString(id) {
		return nil, invalid("comparison_response_id", "must be valid UTF-8")
	}
	return json.Marshal(struct {
		ComparisonResponseID generation.Optional[string] `json:"comparison_response_id,omitzero"`
		Mode                 generation.Optional[string] `json:"mode,omitzero"`
		TTL                  generation.Optional[string] `json:"ttl,omitzero"`
	}{options.ComparisonResponseID, options.Mode, options.TTL})
}

func encodeStreamOptions(options generation.OpenAIStreamOptions) (json.RawMessage, error) {
	if options.IncludeObfuscation.IsNull() {
		return nil, invalid("include_obfuscation", "must be a boolean")
	}
	return json.Marshal(struct {
		IncludeObfuscation generation.Optional[bool] `json:"include_obfuscation,omitzero"`
	}{options.IncludeObfuscation})
}

func encodeReasoningOptions(options generation.ReasoningOptions) (json.RawMessage, error) {
	for _, field := range []struct {
		name   string
		value  generation.Optional[string]
		values []string
	}{
		{"effort", options.Effort, []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}},
		{"context", options.Context, []string{"auto", "current_turn", "all_turns"}},
		{"summary", options.Summary, []string{"auto", "concise", "detailed"}},
		{"generate_summary", options.GenerateSummary, []string{"auto", "concise", "detailed"}},
	} {
		if err := optionalEnum(field.name, field.value, true, field.values...); err != nil {
			return nil, err
		}
	}
	if mode, present := options.Mode.Value(); options.Mode.IsNull() || (present && !utf8.ValidString(mode)) {
		return nil, invalid("mode", "must be a UTF-8 string")
	}
	return json.Marshal(struct {
		Effort          generation.Optional[string] `json:"effort,omitzero"`
		Summary         generation.Optional[string] `json:"summary,omitzero"`
		Context         generation.Optional[string] `json:"context,omitzero"`
		GenerateSummary generation.Optional[string] `json:"generate_summary,omitzero"`
		Mode            generation.Optional[string] `json:"mode,omitzero"`
	}{options.Effort, options.Summary, options.Context, options.GenerateSummary, options.Mode})
}

func optionalEnum(field string, option generation.Optional[string], nullable bool, values ...string) error {
	value, present := option.Value()
	if (option.IsNull() && !nullable) || (present && !slices.Contains(values, value)) {
		return invalid(field, "unsupported value")
	}
	return nil
}

func encodeInclude(includes []string) (json.RawMessage, error) {
	for i, include := range includes {
		if err := requiredString(fmt.Sprintf("[%d]", i), include); err != nil {
			return nil, err
		}
	}
	if includes == nil {
		return json.RawMessage("[]"), nil
	}
	return json.Marshal(includes)
}
