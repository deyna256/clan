package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
	"github.com/tidwall/gjson"
)

func decodeLoadedTools(raw []json.RawMessage) ([]generation.Tool, error) {
	tools := make([]generation.Tool, 0, len(raw))
	for _, item := range raw {
		tool, err := decodeLoadedTool(item, false)
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func decodeLoadedTool(raw json.RawMessage, namespaced bool) (generation.Tool, error) {
	kind := gjson.GetBytes(raw, "type")
	if !utf8.Valid(raw) || !gjson.ValidBytes(raw) || kind.Type != gjson.String || kind.Str == "" {
		return nil, failure(generation.ProtocolError)
	}
	if namespaced && kind.Str != "function" && kind.Str != "custom" {
		return nil, failure(generation.ProtocolError)
	}
	switch kind.Str {
	case "function":
		return decodeLoadedFunction(raw, namespaced)
	case "custom":
		return decodeLoadedCustom(raw)
	case "namespace":
		return decodeLoadedNamespace(raw)
	case "mcp":
		return decodeLoadedMCP(raw)
	case "programmatic_tool_calling":
		return generation.OpenAIProgrammaticToolCallingTool{}, nil
	case "tool_search":
		return decodeLoadedToolSearch(raw)
	default:
		return decodeLoadedNative(raw, kind.Str)
	}
}

func decodeLoadedFunction(raw json.RawMessage, namespaced bool) (generation.Tool, error) {
	var value functionTool
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	tool := generation.FunctionTool{Name: value.Name, Description: value.Description, Parameters: value.Parameters, Strict: value.Strict, OpenAI: generation.OpenAIFunctionToolOptions{Async: value.Async, DeferLoading: value.DeferLoading, AllowedCallers: value.AllowedCallers, OutputSchema: value.OutputSchema}}
	if err := validateFunctionTool(tool, namespaced); err != nil {
		return nil, failure(generation.ProtocolError)
	}
	return tool, nil
}

func decodeLoadedCustom(raw json.RawMessage) (generation.Tool, error) {
	var value customTool
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	tool := generation.CustomTool{Name: value.Name, Description: value.Description, OpenAI: generation.OpenAICustomToolOptions{Async: value.Async, DeferLoading: value.DeferLoading, AllowedCallers: value.AllowedCallers}}
	if len(value.Format) != 0 {
		var format struct {
			Type       string  `json:"type"`
			Syntax     *string `json:"syntax"`
			Definition *string `json:"definition"`
		}
		if json.Unmarshal(value.Format, &format) != nil {
			return nil, failure(generation.ProtocolError)
		}
		switch format.Type {
		case "text":
			tool.Format = generation.CustomTextFormat{}
		case "grammar":
			if format.Syntax == nil || format.Definition == nil {
				return nil, failure(generation.ProtocolError)
			}
			tool.Format = generation.CustomGrammarFormat{Syntax: *format.Syntax, Definition: *format.Definition}
		default:
			return nil, failure(generation.ProtocolError)
		}
	}
	if _, err := encodeCustomTool(tool); err != nil {
		return nil, failure(generation.ProtocolError)
	}
	return tool, nil
}

func decodeLoadedNamespace(raw json.RawMessage) (generation.Tool, error) {
	var value struct {
		Name        string            `json:"name"`
		Description *string           `json:"description"`
		Tools       []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(raw, &value) != nil || requiredString("name", value.Name) != nil || value.Description == nil || value.Tools == nil {
		return nil, failure(generation.ProtocolError)
	}
	children := make([]generation.Tool, 0, len(value.Tools))
	for _, child := range value.Tools {
		tool, err := decodeLoadedTool(child, true)
		if err != nil {
			return nil, err
		}
		children = append(children, tool)
	}
	return generation.OpenAINamespaceTool{Name: value.Name, Description: *value.Description, Tools: children}, nil
}

func decodeLoadedToolSearch(raw json.RawMessage) (generation.Tool, error) {
	var value struct {
		Description generation.Optional[string] `json:"description"`
		Parameters  json.RawMessage             `json:"parameters"`
		Execution   generation.Optional[string] `json:"execution"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	tool := generation.OpenAIToolSearchTool{Description: value.Description, Parameters: value.Parameters, Execution: value.Execution}
	if _, err := encodeToolSearchTool(tool); err != nil {
		return nil, failure(generation.ProtocolError)
	}
	return tool, nil
}
