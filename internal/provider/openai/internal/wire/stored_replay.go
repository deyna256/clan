package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func explicitBreakpoint(enabled bool) json.RawMessage {
	if enabled {
		return json.RawMessage(`{"mode":"explicit"}`)
	}
	return nil
}

func encodeStoredImage(image generation.OpenAIStoredImage, output bool) (json.RawMessage, error) {
	if output {
		return nil, invalid("", "input images cannot be assistant output parts")
	}
	if image.Kind != "input_image" && image.Kind != "computer_screenshot" {
		return nil, invalid("type", "unsupported stored image type")
	}
	switch image.Detail {
	case "auto", "low", "high", "original":
	default:
		return nil, invalid("detail", "unsupported image detail")
	}
	id, hasID := image.FileID.Value()
	if hasID {
		if err := requiredString("file_id", id); err != nil {
			return nil, err
		}
	}
	url, hasURL := image.ImageURL.Value()
	if hasURL {
		if err := imageURL(url); err != nil {
			return nil, err
		}
	}
	if !hasID && !hasURL {
		return nil, invalid("", "stored image requires a file ID or image URL for replay")
	}
	// Screenshots returned as message content use the input_image input shape.
	return json.Marshal(struct {
		Type       string                      `json:"type"`
		Detail     string                      `json:"detail"`
		FileID     generation.Optional[string] `json:"file_id,omitzero"`
		ImageURL   generation.Optional[string] `json:"image_url,omitzero"`
		Breakpoint json.RawMessage             `json:"prompt_cache_breakpoint,omitempty"`
	}{"input_image", image.Detail, image.FileID, image.ImageURL, explicitBreakpoint(image.PromptCacheBreakpoint)})
}

func encodeStoredFile(file generation.OpenAIStoredFile, output bool) (json.RawMessage, error) {
	if output {
		return nil, invalid("", "files cannot be assistant output parts")
	}
	id, hasID := file.FileID.Value()
	if hasID {
		if err := requiredString("file_id", id); err != nil {
			return nil, err
		}
	}
	url, hasURL := file.FileURL.Value()
	if file.FileURL.IsNull() || (hasURL && !httpURL(url)) {
		return nil, invalid("file_url", "expected an HTTP(S) URL")
	}
	data, hasData := file.FileData.Value()
	if file.FileData.IsNull() || (hasData && !fileData(data)) {
		return nil, invalid("file_data", "expected base64 file data or a base64 data URL")
	}
	if !hasID && !hasURL && !hasData {
		return nil, invalid("", "stored file requires a file ID, URL, or data for replay")
	}
	if filename, present := file.Filename.Value(); file.Filename.IsNull() || (present && !utf8.ValidString(filename)) {
		return nil, invalid("filename", "must be a UTF-8 string")
	}
	if err := optionalEnum("detail", file.Detail, false, "auto", "low", "high"); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type       string                      `json:"type"`
		FileID     generation.Optional[string] `json:"file_id,omitzero"`
		FileURL    generation.Optional[string] `json:"file_url,omitzero"`
		FileData   generation.Optional[string] `json:"file_data,omitzero"`
		Filename   generation.Optional[string] `json:"filename,omitzero"`
		Detail     generation.Optional[string] `json:"detail,omitzero"`
		Breakpoint json.RawMessage             `json:"prompt_cache_breakpoint,omitempty"`
	}{"input_file", file.FileID, file.FileURL, file.FileData, file.Filename, file.Detail, explicitBreakpoint(file.PromptCacheBreakpoint)})
}
