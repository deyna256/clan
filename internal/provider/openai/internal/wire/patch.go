package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodePatchTool(tool generation.OpenAIApplyPatchTool) (json.RawMessage, error) {
	if callers, ok := tool.AllowedCallers.Value(); ok {
		if err := allowedCallers(callers); err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		Type           string                        `json:"type"`
		AllowedCallers generation.Optional[[]string] `json:"allowed_callers,omitzero"`
	}{"apply_patch", tool.AllowedCallers})
}

func encodePatchCall(call generation.OpenAIApplyPatchCall) (json.RawMessage, error) {
	if err := patchIdentity(call.ID, call.CallID); err != nil {
		return nil, err
	}
	if call.Status != "in_progress" && call.Status != "completed" {
		return nil, invalid("status", "expected in_progress or completed")
	}
	operation, err := encodePatchOperation(call.Operation)
	if err != nil {
		return nil, at("operation", err)
	}
	caller, err := encodeCaller(call.Caller)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type      string          `json:"type"`
		ID        string          `json:"id,omitempty"`
		CallID    string          `json:"call_id"`
		Status    string          `json:"status"`
		Operation json.RawMessage `json:"operation"`
		Caller    json.RawMessage `json:"caller,omitempty"`
	}{"apply_patch_call", call.ID, call.CallID, call.Status, operation, caller})
}

func encodePatchOperation(operation generation.PatchOperation) (json.RawMessage, error) {
	var value struct {
		Type string                      `json:"type"`
		Path string                      `json:"path"`
		Diff generation.Optional[string] `json:"diff,omitzero"`
	}
	switch op := operation.(type) {
	case generation.PatchCreateFile:
		value.Type, value.Path, value.Diff = "create_file", op.Path, generation.Some(op.Diff)
	case generation.PatchUpdateFile:
		value.Type, value.Path, value.Diff = "update_file", op.Path, generation.Some(op.Diff)
	case generation.PatchDeleteFile:
		value.Type, value.Path = "delete_file", op.Path
	default:
		return nil, invalid("", "expected a patch operation value")
	}
	if value.Path == "" || !utf8.ValidString(value.Path) {
		return nil, invalid("path", "expected a nonempty UTF-8 path")
	}
	if diff, _ := value.Diff.Value(); !utf8.ValidString(diff) {
		return nil, invalid("diff", "must be valid UTF-8")
	}
	return json.Marshal(value)
}

func decodePatchCall(raw json.RawMessage) (generation.Item, error) {
	var value struct {
		ID        string                      `json:"id"`
		CallID    string                      `json:"call_id"`
		Status    string                      `json:"status"`
		Operation json.RawMessage             `json:"operation"`
		Caller    json.RawMessage             `json:"caller"`
		CreatedBy generation.Optional[string] `json:"created_by"`
	}
	if json.Unmarshal(raw, &value) != nil || value.CreatedBy.IsNull() || value.ID == "" || patchIdentity(value.ID, value.CallID) != nil || (value.Status != "in_progress" && value.Status != "completed") {
		return nil, failure(generation.ProtocolError)
	}
	operation, err := decodePatchOperation(value.Operation)
	if err != nil {
		return nil, err
	}
	caller, err := decodeCaller(value.Caller)
	if err != nil {
		return nil, err
	}
	return generation.OpenAIApplyPatchCall{ID: value.ID, CallID: value.CallID, Status: value.Status, Operation: operation, Caller: caller, CreatedBy: value.CreatedBy}, nil
}

func decodePatchOperation(raw json.RawMessage) (generation.PatchOperation, error) {
	var value struct {
		Type string  `json:"type"`
		Path string  `json:"path"`
		Diff *string `json:"diff"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Path == "" {
		return nil, failure(generation.ProtocolError)
	}
	switch value.Type {
	case "delete_file":
		if value.Diff != nil {
			return nil, failure(generation.ProtocolError)
		}
		return generation.PatchDeleteFile{Path: value.Path}, nil
	case "create_file", "update_file":
		if value.Diff == nil {
			return nil, failure(generation.ProtocolError)
		}
		if value.Type == "create_file" {
			return generation.PatchCreateFile{Path: value.Path, Diff: *value.Diff}, nil
		}
		return generation.PatchUpdateFile{Path: value.Path, Diff: *value.Diff}, nil
	default:
		return nil, failure(generation.Unsupported)
	}
}

func encodePatchResult(result generation.OpenAIApplyPatchResult) (json.RawMessage, error) {
	if err := patchIdentity(result.ID, result.CallID); err != nil {
		return nil, err
	}
	if result.Status != "completed" && result.Status != "failed" {
		return nil, invalid("status", "expected completed or failed")
	}
	if output, _ := result.Output.Value(); !utf8.ValidString(output) {
		return nil, invalid("output", "must be valid UTF-8")
	}
	caller, err := encodeCaller(result.Caller)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type   string                      `json:"type"`
		ID     string                      `json:"id,omitempty"`
		CallID string                      `json:"call_id"`
		Status string                      `json:"status"`
		Output generation.Optional[string] `json:"output,omitzero"`
		Caller json.RawMessage             `json:"caller,omitempty"`
	}{"apply_patch_call_output", result.ID, result.CallID, result.Status, result.Output, caller})
}

func decodePatchResult(raw json.RawMessage) (generation.Item, error) {
	var value struct {
		ID        string                      `json:"id"`
		CallID    string                      `json:"call_id"`
		Status    string                      `json:"status"`
		Output    generation.Optional[string] `json:"output"`
		Caller    json.RawMessage             `json:"caller"`
		CreatedBy generation.Optional[string] `json:"created_by"`
	}
	if json.Unmarshal(raw, &value) != nil || value.CreatedBy.IsNull() || value.ID == "" || patchIdentity(value.ID, value.CallID) != nil || (value.Status != "completed" && value.Status != "failed") {
		return nil, failure(generation.ProtocolError)
	}
	caller, err := decodeCaller(value.Caller)
	if err != nil {
		return nil, err
	}
	return generation.OpenAIApplyPatchResult{ID: value.ID, CallID: value.CallID, Status: value.Status, Output: value.Output, Caller: caller, CreatedBy: value.CreatedBy}, nil
}

func patchIdentity(id, callID string) error {
	if !utf8.ValidString(id) {
		return invalid("id", "must be valid UTF-8")
	}
	return requiredString("call_id", callID)
}
