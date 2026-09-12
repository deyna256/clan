package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeProgramTool() (json.RawMessage, error) {
	return json.RawMessage(`{"type":"programmatic_tool_calling"}`), nil
}

func encodeProgram(program generation.OpenAIProgram) (json.RawMessage, error) {
	if err := programIdentity(program.ID, program.CallID); err != nil {
		return nil, err
	}
	if !utf8.ValidString(program.Code) {
		return nil, invalid("code", "must be valid UTF-8")
	}
	fingerprint, present := program.Fingerprint.Value()
	if !present || !utf8.ValidString(fingerprint) {
		return nil, invalid("fingerprint", "must be a UTF-8 string")
	}
	return json.Marshal(struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		CallID      string `json:"call_id"`
		Code        string `json:"code"`
		Fingerprint string `json:"fingerprint"`
	}{"program", program.ID, program.CallID, program.Code, fingerprint})
}

func encodeProgramOutput(output generation.OpenAIProgramOutput) (json.RawMessage, error) {
	if err := programIdentity(output.ID, output.CallID); err != nil {
		return nil, err
	}
	if !utf8.ValidString(output.Result) {
		return nil, invalid("result", "must be valid UTF-8")
	}
	if !programOutputStatus(output.Status) {
		return nil, invalid("status", "expected completed or incomplete")
	}
	return json.Marshal(struct {
		Type   string                `json:"type"`
		ID     string                `json:"id"`
		CallID string                `json:"call_id"`
		Result string                `json:"result"`
		Status generation.ItemStatus `json:"status"`
	}{"program_output", output.ID, output.CallID, output.Result, output.Status})
}

func programIdentity(id, callID string) error {
	if err := requiredString("id", id); err != nil {
		return err
	}
	return requiredString("call_id", callID)
}

func programOutputStatus(status generation.ItemStatus) bool {
	return status == generation.ItemCompleted || status == generation.ItemIncomplete
}

func decodeProgram(raw json.RawMessage, complete bool) (generation.Item, error) {
	var item struct {
		ID          string                      `json:"id"`
		CallID      generation.Optional[string] `json:"call_id"`
		Code        *string                     `json:"code"`
		Fingerprint generation.Optional[string] `json:"fingerprint"`
	}
	if json.Unmarshal(raw, &item) != nil || requiredString("id", item.ID) != nil || item.Code == nil || item.CallID.IsNull() || item.Fingerprint.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	callID, hasCallID := item.CallID.Value()
	_, hasFingerprint := item.Fingerprint.Value()
	if (hasCallID && requiredString("call_id", callID) != nil) || (complete && (!hasCallID || !hasFingerprint)) {
		return nil, failure(generation.ProtocolError)
	}
	return generation.OpenAIProgram{ID: item.ID, CallID: callID, Code: *item.Code, Fingerprint: item.Fingerprint}, nil
}

func decodeProgramOutput(raw json.RawMessage, complete bool) (generation.Item, error) {
	var item struct {
		ID     string                                     `json:"id"`
		CallID generation.Optional[string]                `json:"call_id"`
		Result *string                                    `json:"result"`
		Status generation.Optional[generation.ItemStatus] `json:"status"`
	}
	if json.Unmarshal(raw, &item) != nil || requiredString("id", item.ID) != nil || item.Result == nil || item.CallID.IsNull() || item.Status.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	callID, hasCallID := item.CallID.Value()
	status, hasStatus := item.Status.Value()
	if (hasCallID && requiredString("call_id", callID) != nil) || (hasStatus && !programOutputStatus(status)) || (complete && (!hasCallID || !hasStatus)) {
		return nil, failure(generation.ProtocolError)
	}
	return generation.OpenAIProgramOutput{ID: item.ID, CallID: callID, Result: *item.Result, Status: status}, nil
}
