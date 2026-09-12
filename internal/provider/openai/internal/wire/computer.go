package wire

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type computerSafetyCheck struct {
	ID      string                      `json:"id"`
	Code    generation.Optional[string] `json:"code,omitzero"`
	Message generation.Optional[string] `json:"message,omitzero"`
}
type computerCall struct {
	Type                string                `json:"type"`
	ID                  string                `json:"id"`
	CallID              string                `json:"call_id"`
	Status              string                `json:"status"`
	Action              json.RawMessage       `json:"action,omitempty"`
	Actions             json.RawMessage       `json:"actions,omitempty"`
	PendingSafetyChecks []computerSafetyCheck `json:"pending_safety_checks"`
}
type computerScreenshot struct {
	Type     string                      `json:"type"`
	FileID   generation.Optional[string] `json:"file_id,omitzero"`
	ImageURL generation.Optional[string] `json:"image_url,omitzero"`
	Detail   generation.Optional[string] `json:"detail,omitzero"`
}
type computerResult struct {
	Type                     string                                     `json:"type"`
	ID                       generation.Optional[string]                `json:"id,omitzero"`
	CallID                   string                                     `json:"call_id"`
	Status                   generation.Optional[string]                `json:"status,omitzero"`
	Output                   computerScreenshot                         `json:"output"`
	AcknowledgedSafetyChecks generation.Optional[[]computerSafetyCheck] `json:"acknowledged_safety_checks,omitzero"`
	CreatedBy                generation.Optional[string]                `json:"created_by,omitzero"`
}

func encodeComputerPreviewTool(tool generation.OpenAIComputerPreviewTool) (json.RawMessage, error) {
	if tool.DisplayWidth <= 0 || tool.DisplayHeight <= 0 {
		return nil, invalid("display", "dimensions must be positive")
	}
	switch tool.Environment {
	case "windows", "mac", "linux", "ubuntu", "browser":
	default:
		return nil, invalid("environment", "unsupported computer environment")
	}
	return json.Marshal(struct {
		Type          string `json:"type"`
		DisplayWidth  int64  `json:"display_width"`
		DisplayHeight int64  `json:"display_height"`
		Environment   string `json:"environment"`
	}{"computer_use_preview", tool.DisplayWidth, tool.DisplayHeight, tool.Environment})
}

func computerIdentity(id, callID, status string) error {
	if err := requiredString("id", id); err != nil {
		return err
	}
	if err := requiredString("call_id", callID); err != nil {
		return err
	}
	if status == "" || itemMetadata(id, generation.ItemStatus(status)) != nil {
		return invalid("status", "unsupported computer status")
	}
	return nil
}

func encodeComputerCall(call generation.OpenAIComputerCall) (json.RawMessage, error) {
	if err := computerIdentity(call.ID, call.CallID, call.Status); err != nil {
		return nil, err
	}
	value := computerCall{Type: "computer_call", ID: call.ID, CallID: call.CallID, Status: call.Status}
	var err error
	if call.Action != nil {
		value.Action, err = encodeComputerAction(call.Action)
		if err != nil {
			return nil, at("action", err)
		}
	}
	if call.Actions != nil {
		actions := make([]json.RawMessage, len(call.Actions))
		for i, action := range call.Actions {
			actions[i], err = encodeComputerAction(action)
			if err != nil {
				return nil, at(fmt.Sprintf("actions[%d]", i), err)
			}
		}
		value.Actions, err = json.Marshal(actions)
		if err != nil {
			return nil, err
		}
	}
	value.PendingSafetyChecks, err = encodeComputerSafetyChecks(call.PendingSafetyChecks)
	if err != nil {
		return nil, at("pending_safety_checks", err)
	}
	return json.Marshal(value)
}

func decodeComputerCall(raw json.RawMessage) (generation.Item, error) {
	var value computerCall
	if json.Unmarshal(raw, &value) != nil || computerIdentity(value.ID, value.CallID, value.Status) != nil || value.PendingSafetyChecks == nil {
		return nil, failure(generation.ProtocolError)
	}
	call := generation.OpenAIComputerCall{ID: value.ID, CallID: value.CallID, Status: value.Status}
	var err error
	if len(value.Action) != 0 {
		call.Action, err = decodeComputerAction(value.Action)
		if err != nil {
			return nil, err
		}
	}
	if len(value.Actions) != 0 {
		var actions []json.RawMessage
		if json.Unmarshal(value.Actions, &actions) != nil || actions == nil {
			return nil, failure(generation.ProtocolError)
		}
		call.Actions = make([]generation.ComputerAction, len(actions))
		for i, action := range actions {
			call.Actions[i], err = decodeComputerAction(action)
			if err != nil {
				return nil, err
			}
		}
	}
	call.PendingSafetyChecks, err = decodeComputerSafetyChecks(value.PendingSafetyChecks)
	if err != nil {
		return nil, err
	}
	return call, nil
}

func encodeComputerSafetyChecks(checks []generation.ComputerSafetyCheck) ([]computerSafetyCheck, error) {
	values := make([]computerSafetyCheck, len(checks))
	seen := make(map[string]bool, len(checks))
	for i, check := range checks {
		if err := requiredString("id", check.ID); err != nil {
			return nil, err
		}
		if seen[check.ID] {
			return nil, invalid("id", "duplicate safety check ID")
		}
		seen[check.ID] = true
		for _, field := range []generation.Optional[string]{check.Code, check.Message} {
			if value, _ := field.Value(); !utf8.ValidString(value) {
				return nil, invalid("", "safety check must be valid UTF-8")
			}
		}
		values[i] = computerSafetyCheck{check.ID, check.Code, check.Message}
	}
	return values, nil
}

func decodeComputerSafetyChecks(values []computerSafetyCheck) ([]generation.ComputerSafetyCheck, error) {
	checks := make([]generation.ComputerSafetyCheck, len(values))
	seen := make(map[string]bool, len(values))
	for i, value := range values {
		if requiredString("id", value.ID) != nil || seen[value.ID] {
			return nil, failure(generation.ProtocolError)
		}
		seen[value.ID] = true
		checks[i] = generation.ComputerSafetyCheck{ID: value.ID, Code: value.Code, Message: value.Message}
	}
	return checks, nil
}

func encodeComputerResult(result generation.OpenAIComputerResult) (json.RawMessage, error) {
	if err := requiredString("call_id", result.CallID); err != nil {
		return nil, err
	}
	if id, ok := result.ID.Value(); ok {
		if err := requiredString("id", id); err != nil {
			return nil, err
		}
	}
	if status, ok := result.Status.Value(); ok && (status == "" || itemMetadata("", generation.ItemStatus(status)) != nil) {
		return nil, invalid("status", "unsupported input computer result status")
	}
	screenshot := computerScreenshot{"computer_screenshot", result.Output.FileID, result.Output.ImageURL, result.Output.Detail}
	if err := validateComputerScreenshot(screenshot); err != nil {
		return nil, at("output", err)
	}
	checks := generation.Optional[[]computerSafetyCheck]{}
	if result.AcknowledgedSafetyChecks.IsNull() {
		checks = generation.Null[[]computerSafetyCheck]()
	}
	if values, ok := result.AcknowledgedSafetyChecks.Value(); ok {
		encoded, err := encodeComputerSafetyChecks(values)
		if err != nil {
			return nil, at("acknowledged_safety_checks", err)
		}
		checks = generation.Some(encoded)
	}
	return json.Marshal(computerResult{Type: "computer_call_output", ID: result.ID, CallID: result.CallID, Status: result.Status, Output: screenshot, AcknowledgedSafetyChecks: checks})
}

func validateComputerScreenshot(screenshot computerScreenshot) error {
	if screenshot.Type != "computer_screenshot" {
		return invalid("type", "expected a computer screenshot")
	}
	if screenshot.FileID.IsNull() || screenshot.ImageURL.IsNull() || screenshot.Detail.IsNull() {
		return invalid("", "screenshot fields must not be null")
	}
	if id, ok := screenshot.FileID.Value(); ok {
		if err := requiredString("file_id", id); err != nil {
			return err
		}
	}
	if value, ok := screenshot.ImageURL.Value(); ok {
		if err := imageURL(value); err != nil {
			return err
		}
	}
	if detail, ok := screenshot.Detail.Value(); ok {
		switch detail {
		case "auto", "low", "high", "original":
		default:
			return invalid("detail", "unsupported image detail")
		}
	}
	return nil
}

func decodeComputerResult(raw json.RawMessage) (generation.Item, error) {
	var value computerResult
	if json.Unmarshal(raw, &value) != nil {
		return nil, failure(generation.ProtocolError)
	}
	id, hasID := value.ID.Value()
	status, hasStatus := value.Status.Value()
	if !hasID || !hasStatus || requiredString("id", id) != nil || requiredString("call_id", value.CallID) != nil {
		return nil, failure(generation.ProtocolError)
	}
	if status != "failed" && (status == "" || itemMetadata(id, generation.ItemStatus(status)) != nil) {
		return nil, failure(generation.ProtocolError)
	}
	if value.CreatedBy.IsNull() || value.AcknowledgedSafetyChecks.IsNull() || validateComputerScreenshot(value.Output) != nil {
		return nil, failure(generation.ProtocolError)
	}
	result := generation.OpenAIComputerResult{ID: value.ID, CallID: value.CallID, Status: value.Status, Output: generation.ComputerScreenshotOutput{FileID: value.Output.FileID, ImageURL: value.Output.ImageURL, Detail: value.Output.Detail}, CreatedBy: value.CreatedBy}
	if checks, ok := value.AcknowledgedSafetyChecks.Value(); ok {
		decoded, err := decodeComputerSafetyChecks(checks)
		if err != nil {
			return nil, err
		}
		result.AcknowledgedSafetyChecks = generation.Some(decoded)
	}
	return result, nil
}
