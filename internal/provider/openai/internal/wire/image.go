package wire

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
	"github.com/tidwall/gjson"
)

func encodeImageTool(tool generation.OpenAIImageGenerationTool) (json.RawMessage, error) {
	fidelity, hasFidelity := tool.InputFidelity.Value()
	if hasFidelity && fidelity != "high" && fidelity != "low" {
		return nil, invalid("input_fidelity", "unsupported value")
	}
	if tool.OutputCompression.IsNull() || tool.PartialImages.IsNull() {
		return nil, invalid("", "image numeric options must not be null")
	}
	for _, field := range []struct {
		name, value string
		allowed     []string
	}{
		{"action", tool.Action, []string{"", "auto", "generate", "edit"}},
		{"background", tool.Background, []string{"", "auto", "opaque", "transparent"}},
		{"output_format", tool.OutputFormat, []string{"", "png", "jpeg", "webp"}},
		{"quality", tool.Quality, []string{"", "auto", "low", "medium", "high", "xhigh", "max"}},
		{"input_fidelity", fidelity, []string{"", "low", "high"}},
		{"moderation", tool.Moderation, []string{"", "auto", "low"}},
	} {
		if !slices.Contains(field.allowed, field.value) {
			return nil, invalid(field.name, "unsupported value")
		}
	}
	model, _ := tool.Model.Value()
	if tool.Model.IsNull() || !utf8.ValidString(model) {
		return nil, invalid("model", "must be valid UTF-8")
	}
	if !imageSize(tool.Size) {
		return nil, invalid("size", "expected auto or positive WIDTHxHEIGHT")
	}
	if n, ok := tool.OutputCompression.Value(); ok && (n < 0 || n > 100) {
		return nil, invalid("output_compression", "must be between 0 and 100")
	}
	if n, ok := tool.PartialImages.Value(); ok && (n < 0 || n > 3) {
		return nil, invalid("partial_images", "must be between 0 and 3")
	}
	if tool.Background == "transparent" && tool.OutputFormat == "jpeg" {
		return nil, invalid("output_format", "transparent images require png or webp")
	}
	mask, err := encodeImageMask(tool.Mask)
	if err != nil {
		return nil, at("input_image_mask", err)
	}
	return json.Marshal(struct {
		Type        string                      `json:"type"`
		Model       generation.Optional[string] `json:"model,omitzero"`
		Action      string                      `json:"action,omitempty"`
		Background  string                      `json:"background,omitempty"`
		Format      string                      `json:"output_format,omitempty"`
		Quality     string                      `json:"quality,omitempty"`
		Size        string                      `json:"size,omitempty"`
		Fidelity    generation.Optional[string] `json:"input_fidelity,omitzero"`
		Moderation  string                      `json:"moderation,omitempty"`
		Compression generation.Optional[int64]  `json:"output_compression,omitzero"`
		Partials    generation.Optional[int64]  `json:"partial_images,omitzero"`
		Mask        json.RawMessage             `json:"input_image_mask,omitempty"`
	}{"image_generation", tool.Model, tool.Action, tool.Background, tool.OutputFormat, tool.Quality, tool.Size, tool.InputFidelity, tool.Moderation, tool.OutputCompression, tool.PartialImages, mask})
}

func imageSize(size string) bool {
	if size == "" || size == "auto" {
		return true
	}
	width, height, ok := strings.Cut(size, "x")
	if !ok {
		return false
	}
	w, errW := strconv.ParseUint(width, 10, 32)
	h, errH := strconv.ParseUint(height, 10, 32)
	return errW == nil && errH == nil && w > 0 && h > 0
}

func encodeImageMask(mask generation.Optional[generation.ImageGenerationMask]) (json.RawMessage, error) {
	if mask.IsZero() {
		return nil, nil
	}
	if mask.IsNull() {
		return nil, invalid("", "mask must not be null")
	}
	value, _ := mask.Value()
	if value.FileID.IsNull() || value.ImageURL.IsNull() {
		return nil, invalid("", "mask references must not be null")
	}
	if id, ok := value.FileID.Value(); ok {
		if err := requiredString("file_id", id); err != nil {
			return nil, err
		}
	}
	if url, ok := value.ImageURL.Value(); ok {
		if err := imageURL(url); err != nil {
			return nil, err
		}
		if strings.HasPrefix(url, "data:") && !fileData(url) {
			return nil, invalid("image_url", "invalid base64 mask")
		}
	}
	return json.Marshal(struct {
		ID  generation.Optional[string] `json:"file_id,omitzero"`
		URL generation.Optional[string] `json:"image_url,omitzero"`
	}{value.FileID, value.ImageURL})
}

type imageMetadata struct {
	Action     generation.Optional[string] `json:"action,omitzero"`
	Background generation.Optional[string] `json:"background,omitzero"`
	Format     generation.Optional[string] `json:"output_format,omitzero"`
	Quality    generation.Optional[string] `json:"quality,omitzero"`
	Size       generation.Optional[string] `json:"size,omitzero"`
	Prompt     generation.Optional[string] `json:"revised_prompt,omitzero"`
}

func encodeImageCall(call generation.OpenAIImageGenerationCall) (json.RawMessage, error) {
	if err := requiredString("id", call.ID); err != nil {
		return nil, err
	}
	if !imageStatus(call.Status) {
		return nil, invalid("status", "unsupported image status")
	}
	if result, ok := call.Result.Value(); ok && !ValidBase64Data(result) {
		return nil, invalid("result", "expected base64 image data")
	}
	if call.Status == "completed" && (call.Result.IsZero() || call.Result.IsNull()) {
		return nil, invalid("result", "completed image requires a result")
	}
	m := call.Metadata
	for _, field := range []generation.Optional[string]{m.Action, m.Background, m.OutputFormat, m.Quality, m.Size, m.RevisedPrompt} {
		if value, ok := field.Value(); ok && !utf8.ValidString(value) {
			return nil, invalid("metadata", "must be valid UTF-8")
		}
	}
	return json.Marshal(struct {
		Type   string                      `json:"type"`
		ID     string                      `json:"id"`
		Status string                      `json:"status"`
		Result generation.Optional[string] `json:"result"`
		imageMetadata
	}{"image_generation_call", call.ID, call.Status, call.Result, imageMetadata{m.Action, m.Background, m.OutputFormat, m.Quality, m.Size, m.RevisedPrompt}})
}

func imageStatus(status string) bool {
	return status == "in_progress" || status == "generating" || status == "completed" || status == "failed"
}

func decodeImageCall(data []byte, warn func(string)) (generation.Item, error) {
	var call struct {
		ID     string                      `json:"id"`
		Status string                      `json:"status"`
		Result generation.Optional[string] `json:"result"`
	}
	if json.Unmarshal(data, &call) != nil || requiredString("id", call.ID) != nil || !imageStatus(call.Status) {
		return nil, failure(generation.ProtocolError)
	}
	if result, ok := call.Result.Value(); ok && !ValidBase64Data(result) {
		return nil, failure(generation.ProtocolError)
	}
	return generation.OpenAIImageGenerationCall{ID: call.ID, Status: call.Status, Result: call.Result, Metadata: DecodeImageMetadata(data, warn)}, nil
}

// DecodeImageMetadata tolerates malformed optional fields independently.
func DecodeImageMetadata(data []byte, warn func(string)) generation.ImageGenerationMetadata {
	raw := gjson.ParseBytes(data)
	if !gjson.ValidBytes(data) || (!raw.IsObject() && raw.Type != gjson.Null) {
		warn("invalid_image_metadata")
		return generation.ImageGenerationMetadata{}
	}
	var m generation.ImageGenerationMetadata
	for _, field := range []struct {
		name   string
		target *generation.Optional[string]
	}{
		{name: "action", target: &m.Action}, {name: "background", target: &m.Background}, {name: "output_format", target: &m.OutputFormat},
		{name: "quality", target: &m.Quality}, {name: "size", target: &m.Size}, {name: "revised_prompt", target: &m.RevisedPrompt},
	} {
		value := raw.Get(field.name)
		if !value.Exists() {
			continue
		}
		if json.Unmarshal([]byte(value.Raw), field.target) != nil {
			warn("invalid_image_metadata")
		}
	}
	return m
}
