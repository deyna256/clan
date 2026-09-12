package wire

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type functionBreakpoint struct {
	Mode string `json:"mode"`
}

type functionContent struct {
	Type       string                                  `json:"type"`
	Text       *string                                 `json:"text,omitempty"`
	Detail     generation.Optional[string]             `json:"detail,omitzero"`
	FileID     generation.Optional[string]             `json:"file_id,omitzero"`
	ImageURL   generation.Optional[string]             `json:"image_url,omitzero"`
	FileData   generation.Optional[string]             `json:"file_data,omitzero"`
	FileURL    generation.Optional[string]             `json:"file_url,omitzero"`
	Filename   generation.Optional[string]             `json:"filename,omitzero"`
	Breakpoint generation.Optional[functionBreakpoint] `json:"prompt_cache_breakpoint,omitzero"`
}

func encodeFunctionOutput(output generation.ToolOutput) (json.RawMessage, error) {
	parts, ok := output.(generation.ToolPartsOutput)
	if !ok {
		return encodeToolOutput(output)
	}
	encoded := make([]json.RawMessage, 0, len(parts))
	for i, part := range parts {
		body, err := encodeFunctionPart(part)
		if err != nil {
			return nil, at(fmt.Sprintf("[%d]", i), err)
		}
		encoded = append(encoded, body)
	}
	return json.Marshal(encoded)
}

func encodeFunctionPart(part generation.Part) (json.RawMessage, error) {
	var content functionContent
	var breakpoint generation.Optional[generation.OpenAIPromptCacheBreakpoint]
	switch value := part.(type) {
	case generation.OpenAIFunctionText:
		content.Type, content.Text = "input_text", &value.Text
		breakpoint = value.PromptCacheBreakpoint
	case generation.OpenAIFunctionImage:
		content.Type, content.Detail = "input_image", value.Detail
		content.FileID, content.ImageURL = value.FileID, value.ImageURL
		breakpoint = value.PromptCacheBreakpoint
	case generation.OpenAIFunctionFile:
		content.Type, content.Detail = "input_file", value.Detail
		content.FileID, content.FileData, content.FileURL, content.Filename = value.FileID, value.FileData, value.FileURL, value.Filename
		breakpoint = value.PromptCacheBreakpoint
	default:
		return encodePart(part, false)
	}
	if breakpoint.IsNull() {
		content.Breakpoint = generation.Null[functionBreakpoint]()
	} else if value, present := breakpoint.Value(); present {
		content.Breakpoint = generation.Some(functionBreakpoint{value.Mode})
	}
	if err := content.validate(true); err != nil {
		return nil, err
	}
	return json.Marshal(content)
}

func decodeFunctionPart(raw json.RawMessage) (generation.Part, error) {
	var content functionContent
	if json.Unmarshal(raw, &content) != nil {
		return nil, failure(generation.ProtocolError)
	}
	if content.Type != "input_text" && content.Type != "input_image" && content.Type != "input_file" {
		return nil, failure(generation.Unsupported)
	}
	if content.validate(false) != nil {
		return nil, failure(generation.ProtocolError)
	}
	var breakpoint generation.Optional[generation.OpenAIPromptCacheBreakpoint]
	if content.Breakpoint.IsNull() {
		breakpoint = generation.Null[generation.OpenAIPromptCacheBreakpoint]()
	} else if value, present := content.Breakpoint.Value(); present {
		breakpoint = generation.Some(generation.OpenAIPromptCacheBreakpoint{Mode: value.Mode})
	}
	switch content.Type {
	case "input_text":
		return generation.OpenAIFunctionText{Text: *content.Text, PromptCacheBreakpoint: breakpoint}, nil
	case "input_image":
		return generation.OpenAIFunctionImage{Detail: content.Detail, FileID: content.FileID, ImageURL: content.ImageURL, PromptCacheBreakpoint: breakpoint}, nil
	default:
		return generation.OpenAIFunctionFile{Detail: content.Detail, FileID: content.FileID, FileData: content.FileData, FileURL: content.FileURL, Filename: content.Filename, PromptCacheBreakpoint: breakpoint}, nil
	}
}

func (content functionContent) validate(sending bool) error {
	if breakpoint, present := content.Breakpoint.Value(); present && breakpoint.Mode != "explicit" {
		return invalid("prompt_cache_breakpoint", "expected explicit mode")
	}
	if content.Type == "input_text" {
		if content.Text == nil || !utf8.ValidString(*content.Text) {
			return invalid("text", "must be a UTF-8 string")
		}
		return nil
	}
	id, hasID := content.FileID.Value()
	if hasID {
		if err := requiredString("file_id", id); err != nil {
			return err
		}
	}
	if content.Type == "input_image" {
		if err := optionalEnum("detail", content.Detail, true, "auto", "low", "high", "original"); err != nil {
			return err
		}
		url, hasURL := content.ImageURL.Value()
		if hasURL {
			if err := imageURL(url); err != nil {
				return err
			}
		}
		if sending && !hasID && !hasURL {
			return invalid("", "function image requires a file ID or image URL")
		}
		return nil
	}
	if err := optionalEnum("detail", content.Detail, false, "auto", "low", "high"); err != nil {
		return err
	}
	url, hasURL := content.FileURL.Value()
	if hasURL && !httpURL(url) {
		return invalid("file_url", "expected an HTTP(S) URL")
	}
	data, hasData := content.FileData.Value()
	if hasData && !fileData(data) {
		return invalid("file_data", "expected base64 file data or a base64 data URL")
	}
	if filename, present := content.Filename.Value(); present && !utf8.ValidString(filename) {
		return invalid("filename", "must be a UTF-8 string")
	}
	if sending && !hasID && !hasURL && !hasData {
		return invalid("", "function file requires a file ID, URL, or data")
	}
	return nil
}
