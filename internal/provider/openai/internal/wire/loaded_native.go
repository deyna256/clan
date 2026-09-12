package wire

import (
	"encoding/json"

	"github.com/deyna256/clan/internal/generation"
)

func decodeLoadedNative(raw json.RawMessage, kind string) (generation.Tool, error) {
	var tool generation.Tool
	var err error
	switch kind {
	case "computer":
		tool = generation.OpenAIComputerTool{}
	case "local_shell":
		tool = generation.OpenAILocalShellTool{}
	case "computer_use_preview":
		var value struct {
			Width       int64  `json:"display_width"`
			Height      int64  `json:"display_height"`
			Environment string `json:"environment"`
		}
		if json.Unmarshal(raw, &value) != nil {
			return nil, failure(generation.ProtocolError)
		}
		tool = generation.OpenAIComputerPreviewTool{DisplayWidth: value.Width, DisplayHeight: value.Height, Environment: value.Environment}
	case "apply_patch":
		var value struct {
			AllowedCallers generation.Optional[[]string] `json:"allowed_callers"`
		}
		if json.Unmarshal(raw, &value) != nil {
			return nil, failure(generation.ProtocolError)
		}
		tool = generation.OpenAIApplyPatchTool{AllowedCallers: value.AllowedCallers}
	case "shell":
		tool, err = decodeLoadedShell(raw)
	case "code_interpreter":
		tool, err = decodeLoadedInterpreter(raw)
	case "image_generation":
		tool, err = decodeLoadedImage(raw)
	case "web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11":
		tool, err = decodeLoadedWebSearch(raw, kind)
	case "file_search":
		tool, err = decodeLoadedFileSearch(raw)
	default:
		return nil, failure(generation.Unsupported)
	}
	if err != nil {
		return nil, err
	}
	if _, err := encodeTool(tool); err != nil {
		return nil, failure(generation.ProtocolError)
	}
	return tool, nil
}

func decodeLoadedInterpreter(raw json.RawMessage) (generation.Tool, error) {
	var value struct {
		Container      json.RawMessage               `json:"container"`
		AllowedCallers generation.Optional[[]string] `json:"allowed_callers"`
	}
	if json.Unmarshal(raw, &value) != nil || absent(value.Container) {
		return nil, failure(generation.ProtocolError)
	}
	tool := generation.OpenAICodeInterpreterTool{AllowedCallers: value.AllowedCallers}
	var id string
	if json.Unmarshal(value.Container, &id) == nil {
		tool.Container = generation.InterpreterContainerID(id)
		return tool, nil
	}
	var container struct {
		Type          string                      `json:"type"`
		FileIDs       strictStrings               `json:"file_ids"`
		MemoryLimit   generation.Optional[string] `json:"memory_limit"`
		NetworkPolicy json.RawMessage             `json:"network_policy"`
	}
	if json.Unmarshal(value.Container, &container) != nil || container.Type != "auto" {
		return nil, failure(generation.ProtocolError)
	}
	network, err := decodeLoadedNetwork(container.NetworkPolicy)
	if err != nil {
		return nil, err
	}
	tool.Container = generation.InterpreterAutoContainer{FileIDs: []string(container.FileIDs), MemoryLimit: container.MemoryLimit, NetworkPolicy: network}
	return tool, nil
}

func decodeLoadedImage(raw json.RawMessage) (generation.Tool, error) {
	var value struct {
		Model        generation.Optional[string] `json:"model"`
		Action       generation.Optional[string] `json:"action"`
		Background   generation.Optional[string] `json:"background"`
		OutputFormat generation.Optional[string] `json:"output_format"`
		Quality      generation.Optional[string] `json:"quality"`
		Size         generation.Optional[string] `json:"size"`
		Fidelity     generation.Optional[string] `json:"input_fidelity"`
		Moderation   generation.Optional[string] `json:"moderation"`
		Compression  generation.Optional[int64]  `json:"output_compression"`
		Partials     generation.Optional[int64]  `json:"partial_images"`
		Mask         generation.Optional[struct {
			FileID   generation.Optional[string] `json:"file_id"`
			ImageURL generation.Optional[string] `json:"image_url"`
		}] `json:"input_image_mask"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	for _, field := range []generation.Optional[string]{value.Action, value.Background, value.OutputFormat, value.Quality, value.Size, value.Moderation} {
		if field.IsNull() {
			return nil, failure(generation.ProtocolError)
		}
		if text, ok := field.Value(); ok && text == "" {
			return nil, failure(generation.ProtocolError)
		}
	}
	if value.Compression.IsNull() || value.Partials.IsNull() || value.Mask.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	tool := generation.OpenAIImageGenerationTool{InputFidelity: value.Fidelity, OutputCompression: value.Compression, PartialImages: value.Partials}
	tool.Model = value.Model
	tool.Action, _ = value.Action.Value()
	tool.Background, _ = value.Background.Value()
	tool.OutputFormat, _ = value.OutputFormat.Value()
	tool.Quality, _ = value.Quality.Value()
	tool.Size, _ = value.Size.Value()
	tool.Moderation, _ = value.Moderation.Value()
	if mask, ok := value.Mask.Value(); ok {
		tool.Mask = generation.Some(generation.ImageGenerationMask{FileID: mask.FileID, ImageURL: mask.ImageURL})
	}
	return tool, nil
}
