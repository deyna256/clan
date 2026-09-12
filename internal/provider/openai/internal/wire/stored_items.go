package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

// DecodeStoredItem converts items returned by conversation and input-item resources.
func DecodeStoredItem(raw json.RawMessage, warn func(string)) (generation.Item, error) {
	var header struct {
		ID        string                                     `json:"id"`
		Type      string                                     `json:"type"`
		Status    generation.Optional[generation.ItemStatus] `json:"status"`
		CreatedBy generation.Optional[string]                `json:"created_by"`
	}
	if !utf8.Valid(raw) || json.Unmarshal(raw, &header) != nil || requiredString("id", header.ID) != nil {
		return nil, failure(generation.ProtocolError)
	}
	switch header.Type {
	case "message":
		return decodeStoredMessage(raw, warn)
	case "configuration_update":
		return DecodeConfigurationUpdate(raw)
	case "compaction":
		return decodeCompaction(raw, true)
	}
	var item generation.Item
	var err error
	if header.Type == "function_call" {
		status, present := header.Status.Value()
		if !present || status == "" || itemMetadata(header.ID, status) != nil {
			return nil, failure(generation.ProtocolError)
		}
		item, err = decodeCall(raw, status == generation.ItemCompleted)
	} else {
		item, err = DecodeItem(raw, true, warn)
	}
	if err != nil {
		return nil, err
	}
	switch value := item.(type) {
	case generation.ToolCall:
		if requiredString("call_id", value.CallID) != nil || requiredString("name", value.Name) != nil || header.CreatedBy.IsNull() {
			return nil, failure(generation.ProtocolError)
		}
		value.OpenAI.CreatedBy = header.CreatedBy
		item = value
	case generation.CustomToolCall:
		status, present := header.Status.Value()
		if !present || status == "" || header.CreatedBy.IsNull() || requiredString("call_id", value.CallID) != nil || requiredString("name", value.Name) != nil {
			return nil, failure(generation.ProtocolError)
		}
		value.OpenAI.CreatedBy = header.CreatedBy
		item = value
	case generation.CustomToolResult:
		status, present := header.Status.Value()
		if !present || status == "" || header.CreatedBy.IsNull() {
			return nil, failure(generation.ProtocolError)
		}
		value.CreatedBy = header.CreatedBy
		item = value
	}
	return item, nil
}

func decodeStoredMessage(raw json.RawMessage, warn func(string)) (generation.Item, error) {
	var value struct {
		ID      string                                     `json:"id"`
		Role    generation.Role                            `json:"role"`
		Status  generation.Optional[generation.ItemStatus] `json:"status"`
		Content []json.RawMessage                          `json:"content"`
		Phase   generation.Optional[string]                `json:"phase"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Content == nil || value.Status.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	status, hasStatus := value.Status.Value()
	if itemMetadata(value.ID, status) != nil || (hasStatus && status == "") || (!hasStatus && value.Role != "user" && value.Role != "system" && value.Role != "developer") {
		return nil, failure(generation.ProtocolError)
	}
	switch value.Role {
	case "unknown", "user", "assistant", "system", "critic", "discriminator", "developer", "tool":
	default:
		return nil, failure(generation.ProtocolError)
	}
	if phase, ok := value.Phase.Value(); ok && phase != "commentary" && phase != "final_answer" {
		return nil, failure(generation.ProtocolError)
	}
	message := generation.Message{ID: value.ID, Role: value.Role, Status: status, Parts: make([]generation.Part, 0, len(value.Content)), OpenAI: generation.OpenAIMessageData{Phase: value.Phase}}
	for _, raw := range value.Content {
		part, err := decodeStoredPart(raw, warn)
		if err != nil {
			return nil, err
		}
		message.Parts = append(message.Parts, part)
	}
	return message, nil
}

func decodeStoredPart(raw json.RawMessage, warn func(string)) (generation.Part, error) {
	var value struct {
		Type       string                      `json:"type"`
		Text       *string                     `json:"text"`
		Detail     generation.Optional[string] `json:"detail"`
		FileID     generation.Optional[string] `json:"file_id"`
		ImageURL   generation.Optional[string] `json:"image_url"`
		FileData   generation.Optional[string] `json:"file_data"`
		FileURL    generation.Optional[string] `json:"file_url"`
		Filename   generation.Optional[string] `json:"filename"`
		Breakpoint generation.Optional[struct {
			Mode string `json:"mode"`
		}] `json:"prompt_cache_breakpoint"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Breakpoint.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	breakpoint, hasBreakpoint := value.Breakpoint.Value()
	if hasBreakpoint && breakpoint.Mode != "explicit" {
		return nil, failure(generation.ProtocolError)
	}
	switch value.Type {
	case "text":
		if value.Text == nil {
			return nil, failure(generation.ProtocolError)
		}
		return generation.OpenAIStoredText{Text: *value.Text}, nil
	case "input_text":
		if value.Text == nil {
			return nil, failure(generation.ProtocolError)
		}
		return generation.Text{Text: *value.Text, OpenAI: generation.OpenAITextData{PromptCacheBreakpoint: hasBreakpoint}}, nil
	case "input_image", "computer_screenshot":
		detail, hasDetail := value.Detail.Value()
		if !hasDetail || (detail != "auto" && detail != "low" && detail != "high" && detail != "original") {
			return nil, failure(generation.ProtocolError)
		}
		if value.Type == "computer_screenshot" && (value.FileID.IsZero() || value.ImageURL.IsZero()) {
			return nil, failure(generation.ProtocolError)
		}
		return generation.OpenAIStoredImage{Kind: value.Type, Detail: detail, FileID: value.FileID, ImageURL: value.ImageURL, PromptCacheBreakpoint: hasBreakpoint}, nil
	case "input_file":
		if value.Detail.IsNull() || value.FileData.IsNull() || value.FileURL.IsNull() || value.Filename.IsNull() {
			return nil, failure(generation.ProtocolError)
		}
		if detail, ok := value.Detail.Value(); ok && detail != "auto" && detail != "low" && detail != "high" {
			return nil, failure(generation.ProtocolError)
		}
		return generation.OpenAIStoredFile{FileID: value.FileID, FileData: value.FileData, FileURL: value.FileURL, Filename: value.Filename, Detail: value.Detail, PromptCacheBreakpoint: hasBreakpoint}, nil
	case "output_text", "refusal", "summary_text", "reasoning_text":
		return DecodePart(raw, warn)
	default:
		return nil, failure(generation.Unsupported)
	}
}
