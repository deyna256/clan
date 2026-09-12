package wire

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type customTool struct {
	Name           string                        `json:"name"`
	Description    generation.Optional[string]   `json:"description,omitzero"`
	Format         json.RawMessage               `json:"format,omitempty"`
	Async          generation.Optional[bool]     `json:"async,omitzero"`
	DeferLoading   generation.Optional[bool]     `json:"defer_loading,omitzero"`
	AllowedCallers generation.Optional[[]string] `json:"allowed_callers,omitzero"`
}

func encodeCustomTool(tool generation.CustomTool) (json.RawMessage, error) {
	if err := requiredString("name", tool.Name); err != nil {
		return nil, err
	}
	if tool.Description.IsNull() {
		return nil, invalid("description", "must be a string")
	}
	if tool.OpenAI.Async.IsNull() {
		return nil, invalid("async", "must be a boolean")
	}
	if tool.OpenAI.DeferLoading.IsNull() {
		return nil, invalid("defer_loading", "must be a boolean")
	}
	if description, ok := tool.Description.Value(); ok && !utf8.ValidString(description) {
		return nil, invalid("description", "must be valid UTF-8")
	}
	if callers, ok := tool.OpenAI.AllowedCallers.Value(); ok {
		if err := allowedCallers(callers); err != nil {
			return nil, err
		}
	}
	format, err := encodeCustomFormat(tool.Format)
	if err != nil {
		return nil, at("format", err)
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		customTool
	}{"custom", customTool{tool.Name, tool.Description, format, tool.OpenAI.Async, tool.OpenAI.DeferLoading, tool.OpenAI.AllowedCallers}})
}

func encodeCustomFormat(format generation.CustomInputFormat) (json.RawMessage, error) {
	switch value := format.(type) {
	case nil:
		return nil, nil
	case generation.CustomTextFormat:
		return json.RawMessage(`{"type":"text"}`), nil
	case generation.CustomGrammarFormat:
		if value.Syntax != "lark" && value.Syntax != "regex" {
			return nil, invalid("syntax", "expected lark or regex")
		}
		if !utf8.ValidString(value.Definition) {
			return nil, invalid("definition", "must be valid UTF-8")
		}
		return json.Marshal(struct {
			Type       string `json:"type"`
			Syntax     string `json:"syntax"`
			Definition string `json:"definition"`
		}{"grammar", value.Syntax, value.Definition})
	default:
		return nil, invalid("", "expected a supported custom format value")
	}
}

type toolCaller struct {
	Type     string `json:"type"`
	CallerID string `json:"caller_id,omitempty"`
}

func encodeCaller(caller generation.Optional[generation.OpenAIToolCaller]) (json.RawMessage, error) {
	if caller.IsZero() {
		return nil, nil
	}
	if caller.IsNull() {
		return json.RawMessage("null"), nil
	}
	value, _ := caller.Value()
	if !validCaller(value) {
		return nil, invalid("caller", "expected direct or a program caller ID")
	}
	return json.Marshal(toolCaller{value.Type, value.CallerID})
}

func validCaller(caller generation.OpenAIToolCaller) bool {
	switch caller.Type {
	case "direct":
		return caller.CallerID == ""
	case "program":
		return requiredString("caller_id", caller.CallerID) == nil
	default:
		return false
	}
}

func decodeCaller(raw json.RawMessage) (generation.Optional[generation.OpenAIToolCaller], error) {
	if len(raw) == 0 {
		return generation.Optional[generation.OpenAIToolCaller]{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return generation.Null[generation.OpenAIToolCaller](), nil
	}
	var value toolCaller
	if json.Unmarshal(raw, &value) != nil {
		return generation.Optional[generation.OpenAIToolCaller]{}, failure(generation.ProtocolError)
	}
	caller := generation.OpenAIToolCaller{Type: value.Type, CallerID: value.CallerID}
	if !validCaller(caller) {
		return generation.Optional[generation.OpenAIToolCaller]{}, failure(generation.ProtocolError)
	}
	return generation.Some(caller), nil
}

func encodeCustomCall(call generation.CustomToolCall) (json.RawMessage, error) {
	if err := itemMetadata(call.ID, call.Status); err != nil {
		return nil, err
	}
	if err := requiredString("call_id", call.CallID); err != nil {
		return nil, err
	}
	if err := requiredString("name", call.Name); err != nil {
		return nil, err
	}
	if !utf8.ValidString(call.Input) {
		return nil, invalid("input", "must be valid UTF-8")
	}
	if err := toolCallData(call.OpenAI); err != nil {
		return nil, err
	}
	caller, err := encodeCaller(call.OpenAI.Caller)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type      string                      `json:"type"`
		ID        string                      `json:"id,omitempty"`
		CallID    string                      `json:"call_id"`
		Name      string                      `json:"name"`
		Input     string                      `json:"input"`
		Async     generation.Optional[bool]   `json:"async,omitzero"`
		Namespace generation.Optional[string] `json:"namespace,omitzero"`
		Caller    json.RawMessage             `json:"caller,omitempty"`
	}{"custom_tool_call", call.ID, call.CallID, call.Name, call.Input, call.OpenAI.Async, call.OpenAI.Namespace, caller})
}

func decodeCustomCall(raw json.RawMessage) (generation.Item, error) {
	var value struct {
		ID        generation.Optional[string] `json:"id"`
		CallID    generation.Optional[string] `json:"call_id"`
		Name      generation.Optional[string] `json:"name"`
		Input     *string                     `json:"input"`
		Status    generation.ItemStatus       `json:"status"`
		Async     generation.Optional[bool]   `json:"async"`
		Namespace generation.Optional[string] `json:"namespace"`
		Caller    json.RawMessage             `json:"caller"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Input == nil || value.ID.IsNull() || value.CallID.IsNull() || value.Name.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	id, _ := value.ID.Value()
	callID, _ := value.CallID.Value()
	name, _ := value.Name.Value()
	metadata := generation.OpenAIToolCallData{Async: value.Async, Namespace: value.Namespace}
	if itemMetadata(id, value.Status) != nil || toolCallData(metadata) != nil {
		return nil, failure(generation.ProtocolError)
	}
	caller, err := decodeCaller(value.Caller)
	if err != nil {
		return nil, err
	}
	metadata.Caller = caller
	return generation.CustomToolCall{ID: id, CallID: callID, Name: name, Input: *value.Input, Status: value.Status, OpenAI: metadata}, nil
}

func encodeCustomResult(result generation.CustomToolResult) (json.RawMessage, error) {
	if err := itemMetadata(result.ID, result.Status); err != nil {
		return nil, err
	}
	if err := requiredString("call_id", result.CallID); err != nil {
		return nil, err
	}
	output, err := encodeToolOutput(result.Output)
	if err != nil {
		return nil, at("output", err)
	}
	caller, err := encodeCaller(result.Caller)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type   string          `json:"type"`
		ID     string          `json:"id,omitempty"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
		Caller json.RawMessage `json:"caller,omitempty"`
	}{"custom_tool_call_output", result.ID, result.CallID, output, caller})
}

func decodeCustomResult(raw json.RawMessage) (generation.Item, error) {
	var value struct {
		ID        string                      `json:"id"`
		CallID    string                      `json:"call_id"`
		Output    json.RawMessage             `json:"output"`
		Status    generation.ItemStatus       `json:"status"`
		Caller    json.RawMessage             `json:"caller"`
		CreatedBy generation.Optional[string] `json:"created_by"`
	}
	if json.Unmarshal(raw, &value) != nil || value.CreatedBy.IsNull() || value.ID == "" || itemMetadata(value.ID, value.Status) != nil || requiredString("call_id", value.CallID) != nil {
		return nil, failure(generation.ProtocolError)
	}
	output, err := decodeToolOutput(value.Output)
	if err != nil {
		return nil, err
	}
	caller, err := decodeCaller(value.Caller)
	if err != nil {
		return nil, err
	}
	return generation.CustomToolResult{ID: value.ID, CallID: value.CallID, Output: output, Status: value.Status, Caller: caller, CreatedBy: value.CreatedBy}, nil
}

func decodeToolOutput(raw json.RawMessage) (generation.ToolOutput, error) {
	return decodeToolOutputParts(raw, decodeInputPart)
}

func decodeToolOutputParts(raw json.RawMessage, decodePart func(json.RawMessage) (generation.Part, error)) (generation.ToolOutput, error) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return nil, failure(generation.ProtocolError)
	}
	if data[0] == '"' {
		var value string
		if json.Unmarshal(data, &value) != nil {
			return nil, failure(generation.ProtocolError)
		}
		return generation.ToolTextOutput(value), nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(data, &parts) != nil || parts == nil {
		return nil, failure(generation.ProtocolError)
	}
	output := make(generation.ToolPartsOutput, 0, len(parts))
	for _, raw := range parts {
		part, err := decodePart(raw)
		if err != nil {
			return nil, err
		}
		output = append(output, part)
	}
	return output, nil
}

func decodeInputPart(raw json.RawMessage) (generation.Part, error) {
	var value struct {
		Type       string                      `json:"type"`
		Text       *string                     `json:"text"`
		ImageURL   string                      `json:"image_url"`
		FileID     string                      `json:"file_id"`
		FileURL    string                      `json:"file_url"`
		FileData   string                      `json:"file_data"`
		Filename   generation.Optional[string] `json:"filename"`
		Detail     string                      `json:"detail"`
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
	var part generation.Part
	switch value.Type {
	case "input_text":
		if value.Text == nil {
			return nil, failure(generation.ProtocolError)
		}
		part = generation.Text{Text: *value.Text, OpenAI: generation.OpenAITextData{PromptCacheBreakpoint: hasBreakpoint}}
	case "input_image":
		if (value.ImageURL == "") == (value.FileID == "") {
			return nil, failure(generation.ProtocolError)
		}
		part = generation.ImageURL{URL: value.ImageURL, Detail: value.Detail, OpenAI: generation.OpenAIImageOptions{PromptCacheBreakpoint: hasBreakpoint}}
		if value.FileID != "" {
			part = generation.ImageFile{FileID: value.FileID, Detail: value.Detail, OpenAI: generation.OpenAIImageOptions{PromptCacheBreakpoint: hasBreakpoint}}
		}
	case "input_file":
		options := generation.FileOptions{Filename: value.Filename, Detail: value.Detail, OpenAI: generation.OpenAIFileOptions{PromptCacheBreakpoint: hasBreakpoint}}
		sources := 0
		if value.FileID != "" {
			sources++
			part = generation.FileID{ID: value.FileID, Options: options}
		}
		if value.FileURL != "" {
			sources++
			part = generation.FileURL{URL: value.FileURL, Options: options}
		}
		if value.FileData != "" {
			sources++
			part = generation.FileData{Data: value.FileData, Options: options}
		}
		if sources != 1 {
			return nil, failure(generation.ProtocolError)
		}
	default:
		return nil, failure(generation.Unsupported)
	}
	if _, err := encodePart(part, false); err != nil {
		return nil, failure(generation.ProtocolError)
	}
	return part, nil
}
