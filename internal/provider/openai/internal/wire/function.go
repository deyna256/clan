package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeCall(call generation.ToolCall) (json.RawMessage, error) {
	if err := itemMetadata(call.ID, call.Status); err != nil {
		return nil, err
	}
	if err := requiredString("call_id", call.CallID); err != nil {
		return nil, err
	}
	if err := requiredString("name", call.Name); err != nil {
		return nil, err
	}
	if !utf8.ValidString(call.Arguments) {
		return nil, invalid("arguments", "must be valid UTF-8")
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
		Arguments string                      `json:"arguments"`
		Status    generation.ItemStatus       `json:"status,omitempty"`
		Async     generation.Optional[bool]   `json:"async,omitzero"`
		Namespace generation.Optional[string] `json:"namespace,omitzero"`
		Caller    json.RawMessage             `json:"caller,omitempty"`
	}{"function_call", call.ID, call.CallID, call.Name, call.Arguments, call.Status, call.OpenAI.Async, call.OpenAI.Namespace, caller})
}

func toolCallData(data generation.OpenAIToolCallData) error {
	if data.Async.IsNull() {
		return invalid("async", "must be a boolean")
	}
	if data.Namespace.IsNull() {
		return invalid("namespace", "must be a string")
	}
	if namespace, ok := data.Namespace.Value(); ok && !utf8.ValidString(namespace) {
		return invalid("namespace", "must be valid UTF-8")
	}
	return nil
}

func decodeCall(data []byte, complete bool) (generation.Item, error) {
	var item struct {
		ID        generation.Optional[string]                `json:"id"`
		CallID    generation.Optional[string]                `json:"call_id"`
		Name      generation.Optional[string]                `json:"name"`
		Arguments *string                                    `json:"arguments"`
		Status    generation.Optional[generation.ItemStatus] `json:"status"`
		Async     generation.Optional[bool]                  `json:"async"`
		Namespace generation.Optional[string]                `json:"namespace"`
		Caller    json.RawMessage                            `json:"caller"`
	}
	if json.Unmarshal(data, &item) != nil || item.Arguments == nil || item.CallID.IsNull() || item.Name.IsNull() || item.ID.IsNull() || item.Status.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	id, _ := item.ID.Value()
	status, _ := item.Status.Value()
	metadata := generation.OpenAIToolCallData{Async: item.Async, Namespace: item.Namespace}
	if itemMetadata(id, status) != nil || toolCallData(metadata) != nil {
		return nil, failure(generation.ProtocolError)
	}
	if complete && (!utf8.ValidString(*item.Arguments) || !json.Valid([]byte(*item.Arguments))) {
		return nil, failure(generation.ProtocolError)
	}
	caller, err := decodeCaller(item.Caller)
	if err != nil {
		return nil, err
	}
	metadata.Caller = caller
	callID, _ := item.CallID.Value()
	name, _ := item.Name.Value()
	return generation.ToolCall{ID: id, CallID: callID, Name: name, Arguments: *item.Arguments, Status: status, OpenAI: metadata}, nil
}

type functionResult struct {
	Type      string                                     `json:"type"`
	ID        generation.Optional[string]                `json:"id,omitzero"`
	CallID    generation.Optional[string]                `json:"call_id,omitzero"`
	Output    json.RawMessage                            `json:"output"`
	Status    generation.Optional[generation.ItemStatus] `json:"status,omitzero"`
	Caller    json.RawMessage                            `json:"caller,omitempty"`
	Name      generation.Optional[string]                `json:"name,omitzero"`
	Namespace generation.Optional[string]                `json:"namespace,omitzero"`
	CreatedBy generation.Optional[string]                `json:"created_by,omitzero"`
}

func encodeToolResult(result generation.ToolResult) (json.RawMessage, error) {
	for _, field := range []struct {
		name  string
		value generation.Optional[string]
	}{{"id", result.ID}, {"call_id", result.CallID}, {"name", result.OpenAI.Name}, {"namespace", result.OpenAI.Namespace}} {
		if value, ok := field.value.Value(); ok && !utf8.ValidString(value) {
			return nil, invalid(field.name, "must be valid UTF-8")
		}
	}
	if status, ok := result.Status.Value(); ok && (status == "" || itemMetadata("", status) != nil) {
		return nil, invalid("status", "unsupported item status")
	}
	output, err := encodeFunctionOutput(result.Output)
	if err != nil {
		return nil, at("output", err)
	}
	caller, err := encodeCaller(result.Caller)
	if err != nil {
		return nil, err
	}
	return json.Marshal(functionResult{Type: "function_call_output", ID: result.ID, CallID: result.CallID, Output: output, Status: result.Status, Caller: caller, Name: result.OpenAI.Name, Namespace: result.OpenAI.Namespace})
}

func decodeToolResult(raw json.RawMessage) (generation.Item, error) {
	var value functionResult
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	id, hasID := value.ID.Value()
	status, hasStatus := value.Status.Value()
	if !hasID || requiredString("id", id) != nil || !hasStatus || status == "" || itemMetadata(id, status) != nil || value.CallID.IsNull() || value.Name.IsNull() || value.Namespace.IsNull() || value.CreatedBy.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	output, err := decodeToolOutputParts(value.Output, decodeFunctionPart)
	if err != nil {
		return nil, err
	}
	caller, err := decodeCaller(value.Caller)
	if err != nil {
		return nil, err
	}
	return generation.ToolResult{ID: value.ID, CallID: value.CallID, Output: output, Status: value.Status, Caller: caller, OpenAI: generation.OpenAIToolResultData{Name: value.Name, Namespace: value.Namespace, CreatedBy: value.CreatedBy}}, nil
}
