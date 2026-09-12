package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeToolSearchTool(tool generation.OpenAIToolSearchTool) (json.RawMessage, error) {
	if err := searchExecution(tool.Execution); err != nil {
		return nil, err
	}
	if value, ok := tool.Description.Value(); ok && !utf8.ValidString(value) {
		return nil, invalid("description", "must be valid UTF-8")
	}
	if tool.Parameters != nil && (!utf8.Valid(tool.Parameters) || !json.Valid(tool.Parameters)) {
		return nil, invalid("parameters", "must be valid JSON")
	}
	return json.Marshal(struct {
		Type        string                      `json:"type"`
		Description generation.Optional[string] `json:"description,omitzero"`
		Parameters  json.RawMessage             `json:"parameters,omitempty"`
		Execution   generation.Optional[string] `json:"execution,omitzero"`
	}{"tool_search", tool.Description, tool.Parameters, tool.Execution})
}

type toolSearchItem struct {
	Type      string                                     `json:"type"`
	ID        generation.Optional[string]                `json:"id,omitzero"`
	CallID    generation.Optional[string]                `json:"call_id,omitzero"`
	Execution generation.Optional[string]                `json:"execution,omitzero"`
	Status    generation.Optional[generation.ItemStatus] `json:"status,omitzero"`
	CreatedBy generation.Optional[string]                `json:"created_by,omitzero"`
	Arguments json.RawMessage                            `json:"arguments,omitempty"`
	Tools     json.RawMessage                            `json:"tools,omitempty"`
}

func encodeToolSearchCall(call generation.OpenAIToolSearchCall) (json.RawMessage, error) {
	item := toolSearchItem{Type: "tool_search_call", ID: call.ID, CallID: call.CallID, Execution: call.Execution, Status: call.Status, Arguments: call.Arguments}
	if err := searchInputMetadata(item); err != nil {
		return nil, err
	}
	if !utf8.Valid(call.Arguments) || !json.Valid(call.Arguments) {
		return nil, invalid("arguments", "must be valid JSON")
	}
	return json.Marshal(item)
}

func encodeToolSearchOutput(output generation.OpenAIToolSearchOutput) (json.RawMessage, error) {
	item := toolSearchItem{Type: "tool_search_output", ID: output.ID, CallID: output.CallID, Execution: output.Execution, Status: output.Status}
	if err := searchInputMetadata(item); err != nil {
		return nil, err
	}
	tools, err := encodeToolCatalog(output.Tools)
	if err != nil {
		return nil, at("tools", err)
	}
	item.Tools = tools
	return json.Marshal(item)
}

func encodeToolCatalog(tools []generation.Tool) (json.RawMessage, error) {
	encoded, err := encodeTools(tools)
	if err != nil {
		return nil, err
	}
	if encoded == nil {
		return json.RawMessage(`[]`), nil
	}
	return json.Marshal(encoded)
}

func searchInputMetadata(item toolSearchItem) error {
	for _, field := range []struct {
		name  string
		value generation.Optional[string]
	}{{"id", item.ID}, {"call_id", item.CallID}} {
		if value, ok := field.value.Value(); ok && !utf8.ValidString(value) {
			return invalid(field.name, "must be valid UTF-8")
		}
	}
	if status, ok := item.Status.Value(); ok && (status == "" || itemMetadata("", status) != nil) {
		return invalid("status", "unsupported item status")
	}
	return searchExecution(item.Execution)
}

func searchExecution(execution generation.Optional[string]) error {
	value, present := execution.Value()
	if execution.IsNull() || (present && value != "server" && value != "client") {
		return invalid("execution", "expected server or client")
	}
	return nil
}

func decodeToolSearchItem(raw json.RawMessage) (generation.Item, error) {
	var item toolSearchItem
	if json.Unmarshal(raw, &item) != nil || searchInputMetadata(item) != nil {
		return nil, failure(generation.ProtocolError)
	}
	id, hasID := item.ID.Value()
	_, hasExecution := item.Execution.Value()
	_, hasStatus := item.Status.Value()
	if !hasID || requiredString("id", id) != nil || !hasExecution || !hasStatus || item.CallID.IsZero() || item.CreatedBy.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	if item.Type == "tool_search_call" {
		if !json.Valid(item.Arguments) {
			return nil, failure(generation.ProtocolError)
		}
		return generation.OpenAIToolSearchCall{ID: item.ID, CallID: item.CallID, Execution: item.Execution, Status: item.Status, Arguments: item.Arguments, CreatedBy: item.CreatedBy}, nil
	}
	tools, err := decodeToolCatalog(item.Tools)
	if err != nil {
		return nil, err
	}
	return generation.OpenAIToolSearchOutput{ID: item.ID, CallID: item.CallID, Execution: item.Execution, Status: item.Status, Tools: tools, CreatedBy: item.CreatedBy}, nil
}

func decodeToolCatalog(raw json.RawMessage) ([]generation.Tool, error) {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || tools == nil {
		return nil, failure(generation.ProtocolError)
	}
	return decodeLoadedTools(tools)
}

func encodeAdditionalTools(item generation.OpenAIAdditionalTools) (json.RawMessage, error) {
	if item.Role != "developer" {
		return nil, invalid("role", "additional tools require developer role")
	}
	if id, ok := item.ID.Value(); ok && !utf8.ValidString(id) {
		return nil, invalid("id", "must be valid UTF-8")
	}
	tools, err := encodeToolCatalog(item.Tools)
	if err != nil {
		return nil, at("tools", err)
	}
	return json.Marshal(struct {
		Type  string                      `json:"type"`
		ID    generation.Optional[string] `json:"id,omitzero"`
		Role  string                      `json:"role"`
		Tools json.RawMessage             `json:"tools"`
	}{"additional_tools", item.ID, item.Role, tools})
}

func decodeAdditionalTools(raw json.RawMessage) (generation.Item, error) {
	var item struct {
		ID    string          `json:"id"`
		Role  string          `json:"role"`
		Tools json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(raw, &item) != nil || requiredString("id", item.ID) != nil {
		return nil, failure(generation.ProtocolError)
	}
	switch item.Role {
	case "unknown", "user", "assistant", "system", "critic", "discriminator", "developer", "tool":
	default:
		return nil, failure(generation.ProtocolError)
	}
	tools, err := decodeToolCatalog(item.Tools)
	if err != nil {
		return nil, err
	}
	return generation.OpenAIAdditionalTools{ID: generation.Some(item.ID), Role: item.Role, Tools: tools}, nil
}
