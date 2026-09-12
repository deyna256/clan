package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeNamespaceTool(tool generation.OpenAINamespaceTool) (json.RawMessage, error) {
	if err := requiredString("name", tool.Name); err != nil {
		return nil, err
	}
	if !utf8.ValidString(tool.Description) {
		return nil, invalid("description", "must be valid UTF-8")
	}
	children := make([]json.RawMessage, 0, len(tool.Tools))
	for i, child := range tool.Tools {
		var body json.RawMessage
		var err error
		switch value := child.(type) {
		case generation.FunctionTool:
			body, err = encodeFunctionTool(value, true)
		case generation.CustomTool:
			body, err = encodeCustomTool(value)
		default:
			err = invalid("", "expected a function or custom tool value")
		}
		if err != nil {
			return nil, at(fmt.Sprintf("tools[%d]", i), err)
		}
		children = append(children, body)
	}
	return json.Marshal(struct {
		Type        string            `json:"type"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Tools       []json.RawMessage `json:"tools"`
	}{"namespace", tool.Name, tool.Description, children})
}

type functionTool struct {
	Name           string                        `json:"name"`
	Description    generation.Optional[string]   `json:"description,omitzero"`
	Parameters     json.RawMessage               `json:"parameters,omitempty"`
	Strict         generation.Optional[bool]     `json:"strict,omitzero"`
	Async          generation.Optional[bool]     `json:"async,omitzero"`
	DeferLoading   generation.Optional[bool]     `json:"defer_loading,omitzero"`
	AllowedCallers generation.Optional[[]string] `json:"allowed_callers,omitzero"`
	OutputSchema   json.RawMessage               `json:"output_schema,omitempty"`
}

func encodeFunctionTool(tool generation.FunctionTool, namespaced bool) (json.RawMessage, error) {
	if err := validateFunctionTool(tool, namespaced); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		functionTool
	}{"function", functionTool{tool.Name, tool.Description, tool.Parameters, tool.Strict, tool.OpenAI.Async, tool.OpenAI.DeferLoading, tool.OpenAI.AllowedCallers, tool.OpenAI.OutputSchema}})
}

func validateFunctionTool(tool generation.FunctionTool, namespaced bool) error {
	if err := requiredString("name", tool.Name); err != nil {
		return err
	}
	if namespaced {
		if tool.Parameters != nil && (!utf8.Valid(tool.Parameters) || !json.Valid(tool.Parameters)) {
			return invalid("parameters", "must be valid JSON")
		}
	} else if err := nullableJSONObject("parameters", tool.Parameters); err != nil {
		return err
	}
	if description, ok := tool.Description.Value(); ok && !utf8.ValidString(description) {
		return invalid("description", "must be valid UTF-8")
	}
	if tool.OpenAI.Async.IsNull() {
		return invalid("async", "must be a boolean")
	}
	if tool.OpenAI.DeferLoading.IsNull() {
		return invalid("defer_loading", "must be a boolean")
	}
	if callers, ok := tool.OpenAI.AllowedCallers.Value(); ok {
		if err := allowedCallers(callers); err != nil {
			return err
		}
	}
	if tool.OpenAI.OutputSchema != nil {
		if err := nullableJSONObject("output_schema", tool.OpenAI.OutputSchema); err != nil {
			return err
		}
	}
	return nil
}

func nullableJSONObject(field string, raw json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return jsonObject(field, raw)
}
