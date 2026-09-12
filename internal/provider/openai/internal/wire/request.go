// Package wire converts generation values to the Responses wire protocol.
package wire

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

// InputError identifies an invalid or unsupported field without its contents.
type InputError struct {
	Field   string
	Problem string
}

func (e *InputError) Error() string { return "openai: " + e.Field + ": " + e.Problem }

func (*InputError) Unwrap() error { return &generation.Failure{Kind: generation.InvalidRequest} }

// EncodeRequest validates and encodes one request without changing its input.
// streaming is chosen by the calling operation, not by provider options.
// The caller owns resource authorization and account affinity before dispatch.
func EncodeRequest(request generation.Request, streaming bool) ([]byte, error) {
	body, err := encodeCreateRequest(request, streaming)
	if err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func encodeCreateRequest(request generation.Request, streaming bool) (createRequest, error) {
	if err := validateSettings(request); err != nil {
		return createRequest{}, err
	}
	input, err := EncodeInput(request.Input)
	if err != nil {
		return createRequest{}, err
	}
	tools, err := encodeTools(request.Tools)
	if err != nil {
		return createRequest{}, err
	}
	choice, err := encodeChoice(request.ToolChoice)
	if err != nil {
		return createRequest{}, at("tool_choice", err)
	}
	format, err := encodeFormat(request.OutputFormat)
	if err != nil {
		return createRequest{}, at("text.format", err)
	}
	options := request.OpenAI
	extra, err := encodeRequestOptions(options, streaming)
	if err != nil {
		return createRequest{}, err
	}
	body := createRequest{
		requestOptions: extra,
		Model:          request.Model, Input: input, Stream: streaming,
		Instructions: request.Instructions, MaxOutputTokens: request.MaxOutputTokens,
		Temperature: request.Temperature, TopP: request.TopP,
		ParallelToolCalls: request.ParallelToolCalls, Tools: tools, ToolChoice: choice,
		Background: options.Background, Store: options.Store,
		PreviousResponseID: options.PreviousResponseID,
		Text:               textOptions{Format: format, Verbosity: options.Verbosity},
		MaxToolCalls:       options.MaxToolCalls, ServiceTier: options.ServiceTier,
		TopLogprobs: options.TopLogprobs,
		Truncation:  options.Truncation, PromptCacheKey: options.PromptCacheKey,
		PromptCacheRetention: options.PromptCacheRetention, SafetyIdentifier: options.SafetyIdentifier,
	}
	return body, nil
}

// EncodeInput validates and encodes input history without changing its items.
func EncodeInput(items []generation.Item) ([]json.RawMessage, error) {
	input := make([]json.RawMessage, 0, len(items))
	for i, item := range items {
		if _, trigger := item.(generation.OpenAICompactionTrigger); trigger && i != len(items)-1 {
			return nil, invalid(fmt.Sprintf("input[%d]", i), "compaction trigger must be the final input item")
		}
		encoded, err := encodeItem(item)
		if err != nil {
			return nil, at(fmt.Sprintf("input[%d]", i), err)
		}
		input = append(input, encoded)
	}
	return input, nil
}

type createRequest struct {
	requestOptions
	Model                string                       `json:"model"`
	Input                []json.RawMessage            `json:"input"`
	Stream               bool                         `json:"stream"`
	Instructions         generation.Optional[string]  `json:"instructions,omitzero"`
	MaxOutputTokens      generation.Optional[int64]   `json:"max_output_tokens,omitzero"`
	Temperature          generation.Optional[float64] `json:"temperature,omitzero"`
	TopP                 generation.Optional[float64] `json:"top_p,omitzero"`
	ParallelToolCalls    generation.Optional[bool]    `json:"parallel_tool_calls,omitzero"`
	Tools                []json.RawMessage            `json:"tools,omitempty"`
	ToolChoice           json.RawMessage              `json:"tool_choice,omitempty"`
	Background           generation.Optional[bool]    `json:"background,omitzero"`
	Store                generation.Optional[bool]    `json:"store,omitzero"`
	PreviousResponseID   generation.Optional[string]  `json:"previous_response_id,omitzero"`
	Text                 textOptions                  `json:"text,omitzero"`
	MaxToolCalls         generation.Optional[int64]   `json:"max_tool_calls,omitzero"`
	TopLogprobs          generation.Optional[int64]   `json:"top_logprobs,omitzero"`
	ServiceTier          generation.Optional[string]  `json:"service_tier,omitzero"`
	Truncation           generation.Optional[string]  `json:"truncation,omitzero"`
	PromptCacheKey       generation.Optional[string]  `json:"prompt_cache_key,omitzero"`
	PromptCacheRetention generation.Optional[string]  `json:"prompt_cache_retention,omitzero"`
	SafetyIdentifier     generation.Optional[string]  `json:"safety_identifier,omitzero"`
}

type textOptions struct {
	Format    json.RawMessage             `json:"format,omitempty"`
	Verbosity generation.Optional[string] `json:"verbosity,omitzero"`
}

func validateSettings(r generation.Request) error {
	if err := requiredString("model", r.Model); err != nil {
		return err
	}
	if background, _ := r.OpenAI.Background.Value(); background {
		return invalid("background", "background generation is not supported")
	}
	if value, ok := r.OpenAI.TopLogprobs.Value(); ok && (value < 0 || value > 20) {
		return invalid("top_logprobs", "must be between 0 and 20")
	}
	for _, field := range []struct {
		name  string
		value generation.Optional[int64]
	}{
		{"max_output_tokens", r.MaxOutputTokens}, {"max_tool_calls", r.OpenAI.MaxToolCalls},
	} {
		if value, ok := field.value.Value(); ok && value <= 0 {
			return invalid(field.name, "must be positive")
		}
	}
	for _, field := range []struct {
		name  string
		value generation.Optional[float64]
		max   float64
	}{
		{"temperature", r.Temperature, 2}, {"top_p", r.TopP, 1},
	} {
		if value, ok := field.value.Value(); ok && (math.IsNaN(value) || value < 0 || value > field.max) {
			return invalid(field.name, "outside the supported range")
		}
	}
	for _, field := range []struct {
		name  string
		value generation.Optional[string]
	}{
		{"instructions", r.Instructions}, {"previous_response_id", r.OpenAI.PreviousResponseID},
		{"text.verbosity", r.OpenAI.Verbosity}, {"service_tier", r.OpenAI.ServiceTier},
		{"truncation", r.OpenAI.Truncation}, {"prompt_cache_key", r.OpenAI.PromptCacheKey},
		{"prompt_cache_retention", r.OpenAI.PromptCacheRetention}, {"safety_identifier", r.OpenAI.SafetyIdentifier},
	} {
		if value, ok := field.value.Value(); ok && !utf8.ValidString(value) {
			return invalid(field.name, "must be valid UTF-8")
		}
	}
	return nil
}

func invalid(field, problem string) error { return &InputError{Field: field, Problem: problem} }

func at(prefix string, err error) error {
	if input, ok := err.(*InputError); ok {
		field := prefix
		if input.Field != "" {
			if !strings.HasPrefix(input.Field, "[") {
				field += "."
			}
			field += input.Field
		}
		return invalid(field, input.Problem)
	}
	return invalid(prefix, "cannot encode value")
}

func requiredString(field, value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
		return invalid(field, "must be nonblank UTF-8")
	}
	return nil
}
