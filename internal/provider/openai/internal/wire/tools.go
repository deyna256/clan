package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeTools(tools []generation.Tool) ([]json.RawMessage, error) {
	var encoded []json.RawMessage
	for i, tool := range tools {
		body, err := encodeTool(tool)
		if err != nil {
			return nil, at(fmt.Sprintf("tools[%d]", i), err)
		}
		encoded = append(encoded, body)
	}
	return encoded, nil
}

func encodeTool(tool generation.Tool) (json.RawMessage, error) {
	switch value := tool.(type) {
	case generation.OpenAIProgrammaticToolCallingTool:
		return encodeProgramTool()
	case generation.OpenAIToolSearchTool:
		return encodeToolSearchTool(value)
	case generation.OpenAINamespaceTool:
		return encodeNamespaceTool(value)
	case generation.FunctionTool:
		return encodeFunctionTool(value, false)
	case generation.OpenAIMCPTool:
		return encodeMCPTool(value)
	case generation.OpenAIComputerTool:
		return json.RawMessage(`{"type":"computer"}`), nil
	case generation.OpenAIComputerPreviewTool:
		return encodeComputerPreviewTool(value)
	case generation.OpenAIShellTool:
		return encodeShellTool(value)
	case generation.OpenAILocalShellTool:
		return json.RawMessage(`{"type":"local_shell"}`), nil
	case generation.OpenAIApplyPatchTool:
		return encodePatchTool(value)
	case generation.CustomTool:
		return encodeCustomTool(value)
	case generation.OpenAIImageGenerationTool:
		return encodeImageTool(value)
	case generation.OpenAICodeInterpreterTool:
		return encodeInterpreterTool(value)
	case generation.OpenAIWebSearchTool:
		return encodeWebSearchTool(value)
	case generation.OpenAIFileSearchTool:
		return encodeFileSearchTool(value)
	}
	return nil, invalid("", "expected a supported tool value")
}

func encodeChoice(choice generation.ToolChoice) (json.RawMessage, error) {
	switch value := choice.(type) {
	case generation.OpenAIAllowedToolsChoice:
		if value.Mode != generation.ToolAuto && value.Mode != generation.ToolRequired {
			return nil, invalid("mode", "expected auto or required")
		}
		tools := make([]json.RawMessage, 0, len(value.Tools))
		for i, tool := range value.Tools {
			switch tool.(type) {
			case nil, generation.ToolMode, generation.OpenAIAllowedToolsChoice:
				return nil, invalid(fmt.Sprintf("tools[%d]", i), "expected an individual tool choice")
			}
			encoded, err := encodeChoice(tool)
			if err != nil {
				return nil, at(fmt.Sprintf("tools[%d]", i), err)
			}
			tools = append(tools, encoded)
		}
		return json.Marshal(struct {
			Type  string              `json:"type"`
			Mode  generation.ToolMode `json:"mode"`
			Tools []json.RawMessage   `json:"tools"`
		}{"allowed_tools", value.Mode, tools})
	case generation.OpenAIProgrammaticToolCallingChoice:
		return encodeProgramTool()
	case generation.OpenAIMCPChoice:
		return encodeMCPChoice(value)
	case generation.OpenAIComputerChoice:
		if value != "computer" && value != "computer_use_preview" && value != "computer_use" {
			return nil, invalid("", "unsupported computer choice")
		}
		return json.Marshal(struct {
			Type string `json:"type"`
		}{string(value)})
	case generation.OpenAIShellChoice:
		return json.RawMessage(`{"type":"shell"}`), nil
	case generation.OpenAILocalShellChoice:
		return json.RawMessage(`{"type":"local_shell"}`), nil
	case generation.OpenAIApplyPatchChoice:
		return json.RawMessage(`{"type":"apply_patch"}`), nil
	case generation.NamedCustomTool:
		if err := requiredString("name", value.Name); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{"custom", value.Name})
	case generation.OpenAIImageGenerationChoice:
		return json.RawMessage(`{"type":"image_generation"}`), nil
	case generation.OpenAICodeInterpreterChoice:
		return json.RawMessage(`{"type":"code_interpreter"}`), nil
	case nil:
		return nil, nil
	case generation.ToolMode:
		if value != generation.ToolAuto && value != generation.ToolNone && value != generation.ToolRequired {
			return nil, invalid("", "unsupported choice mode")
		}
		return json.Marshal(string(value))
	case generation.NamedTool:
		if err := requiredString("name", value.Name); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{"function", value.Name})
	case generation.OpenAISearchChoice:
		if value != "web_search" && value != "web_search_2025_08_26" && value != "web_search_preview" && value != "web_search_preview_2025_03_11" && value != "file_search" {
			return nil, invalid("", "unsupported search choice")
		}
		return json.Marshal(struct {
			Type string `json:"type"`
		}{string(value)})
	default:
		return nil, invalid("", "expected a supported choice value")
	}
}

func encodeFormat(format generation.OutputFormat) (json.RawMessage, error) {
	switch value := format.(type) {
	case nil:
		return nil, nil
	case generation.TextFormat:
		return json.RawMessage(`{"type":"text"}`), nil
	case generation.JSONObjectFormat:
		return json.RawMessage(`{"type":"json_object"}`), nil
	case generation.JSONSchemaFormat:
		if err := requiredString("name", value.Name); err != nil {
			return nil, err
		}
		if err := jsonObject("schema", value.Schema); err != nil {
			return nil, err
		}
		if description, ok := value.Description.Value(); ok && !utf8.ValidString(description) {
			return nil, invalid("description", "must be valid UTF-8")
		}
		return json.Marshal(struct {
			Type        string                      `json:"type"`
			Name        string                      `json:"name"`
			Description generation.Optional[string] `json:"description,omitzero"`
			Schema      json.RawMessage             `json:"schema"`
			Strict      generation.Optional[bool]   `json:"strict,omitzero"`
		}{"json_schema", value.Name, value.Description, value.Schema, value.Strict})
	default:
		return nil, invalid("", "expected a supported format value")
	}
}

func jsonObject(field string, value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(trimmed) || !json.Valid(trimmed) {
		return invalid(field, "must be a JSON object")
	}
	return nil
}

func allowedCallers(callers []string) error {
	for _, caller := range callers {
		if caller != "direct" && caller != "programmatic" {
			return invalid("allowed_callers", "unsupported caller")
		}
	}
	return nil
}
