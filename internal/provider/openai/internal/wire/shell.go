package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type shellAction struct {
	Commands        strictStrings              `json:"commands"`
	TimeoutMs       generation.Optional[int64] `json:"timeout_ms,omitzero"`
	MaxOutputLength generation.Optional[int64] `json:"max_output_length,omitzero"`
}

// encoding/json accepts null into a string; these entries must be strings.
type strictStrings []string

func (s *strictStrings) UnmarshalJSON(raw []byte) error {
	var values []*string
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	if values == nil {
		return failure(generation.ProtocolError)
	}
	result := make(strictStrings, len(values))
	for i, value := range values {
		if value == nil {
			return failure(generation.ProtocolError)
		}
		result[i] = *value
	}
	*s = result
	return nil
}

type shellCall struct {
	Type        string                      `json:"type"`
	ID          generation.Optional[string] `json:"id,omitzero"`
	CallID      string                      `json:"call_id"`
	Status      generation.Optional[string] `json:"status,omitzero"`
	Action      shellAction                 `json:"action"`
	Environment json.RawMessage             `json:"environment,omitempty"`
	Caller      json.RawMessage             `json:"caller,omitempty"`
	CreatedBy   generation.Optional[string] `json:"created_by,omitzero"`
}
type shellResult struct {
	Type            string                      `json:"type"`
	ID              generation.Optional[string] `json:"id,omitzero"`
	CallID          string                      `json:"call_id"`
	Status          generation.Optional[string] `json:"status,omitzero"`
	MaxOutputLength generation.Optional[int64]  `json:"max_output_length,omitzero"`
	Output          json.RawMessage             `json:"output"`
	Caller          json.RawMessage             `json:"caller,omitempty"`
	CreatedBy       generation.Optional[string] `json:"created_by,omitzero"`
}
type shellOutput struct {
	Stdout    string                      `json:"stdout"`
	Stderr    string                      `json:"stderr"`
	Outcome   json.RawMessage             `json:"outcome"`
	CreatedBy generation.Optional[string] `json:"created_by,omitzero"`
}

func shellStatus(status string) bool {
	return status == "in_progress" || status == "completed" || status == "incomplete"
}

func shellMetadata(id generation.Optional[string], callID string, status, createdBy generation.Optional[string]) error {
	if err := requiredString("call_id", callID); err != nil {
		return err
	}
	if value, ok := id.Value(); ok {
		if err := requiredString("id", value); err != nil {
			return err
		}
	}
	if value, ok := status.Value(); ok && !shellStatus(value) {
		return invalid("status", "unsupported shell status")
	}
	if createdBy.IsNull() {
		return invalid("created_by", "must not be null")
	}
	if value, ok := createdBy.Value(); ok && !utf8.ValidString(value) {
		return invalid("created_by", "must be valid UTF-8")
	}
	return nil
}

func shellLimits(timeout, maximum generation.Optional[int64]) error {
	if value, ok := timeout.Value(); ok && value < 0 {
		return invalid("timeout_ms", "must not be negative")
	}
	if value, ok := maximum.Value(); ok && value < 0 {
		return invalid("max_output_length", "must not be negative")
	}
	return nil
}

func encodeShellCall(call generation.OpenAIShellCall) (json.RawMessage, error) {
	if err := shellMetadata(call.ID, call.CallID, call.Status, call.CreatedBy); err != nil {
		return nil, err
	}
	if err := shellLimits(call.Action.TimeoutMs, call.Action.MaxOutputLength); err != nil {
		return nil, at("action", err)
	}
	commands := call.Action.Commands
	if commands == nil {
		commands = []string{}
	}
	for _, command := range commands {
		if !utf8.ValidString(command) {
			return nil, invalid("action.commands", "must be valid UTF-8")
		}
	}
	environment, err := encodeShellEnvironment(call.Environment, false)
	if err != nil {
		return nil, at("environment", err)
	}
	caller, err := encodeCaller(call.Caller)
	if err != nil {
		return nil, err
	}
	return json.Marshal(shellCall{"shell_call", call.ID, call.CallID, call.Status, shellAction{commands, call.Action.TimeoutMs, call.Action.MaxOutputLength}, environment, caller, generation.Optional[string]{}})
}

func decodeShellCall(raw json.RawMessage) (generation.Item, error) {
	var value shellCall
	if json.Unmarshal(raw, &value) != nil || value.Action.Commands == nil || value.Action.TimeoutMs.IsZero() || value.Action.MaxOutputLength.IsZero() || len(value.Environment) == 0 {
		return nil, failure(generation.ProtocolError)
	}
	if _, ok := value.ID.Value(); !ok {
		return nil, failure(generation.ProtocolError)
	}
	if _, ok := value.Status.Value(); !ok {
		return nil, failure(generation.ProtocolError)
	}
	if shellMetadata(value.ID, value.CallID, value.Status, value.CreatedBy) != nil || shellLimits(value.Action.TimeoutMs, value.Action.MaxOutputLength) != nil {
		return nil, failure(generation.ProtocolError)
	}
	environment, err := decodeShellEnvironment(value.Environment)
	if err != nil {
		return nil, err
	}
	caller, err := decodeCaller(value.Caller)
	if err != nil {
		return nil, err
	}
	return generation.OpenAIShellCall{ID: value.ID, CallID: value.CallID, Status: value.Status, Action: generation.ShellAction{Commands: value.Action.Commands, TimeoutMs: value.Action.TimeoutMs, MaxOutputLength: value.Action.MaxOutputLength}, Environment: environment, Caller: caller, CreatedBy: value.CreatedBy}, nil
}

func encodeShellResult(result generation.OpenAIShellResult) (json.RawMessage, error) {
	if err := shellMetadata(result.ID, result.CallID, result.Status, result.CreatedBy); err != nil {
		return nil, err
	}
	if err := shellLimits(generation.Optional[int64]{}, result.MaxOutputLength); err != nil {
		return nil, err
	}
	outputs, err := encodeShellOutputs(result.Output)
	if err != nil {
		return nil, at("output", err)
	}
	caller, err := encodeCaller(result.Caller)
	if err != nil {
		return nil, err
	}
	return json.Marshal(shellResult{"shell_call_output", result.ID, result.CallID, result.Status, result.MaxOutputLength, outputs, caller, generation.Optional[string]{}})
}

func decodeShellResult(raw json.RawMessage) (generation.Item, error) {
	var value shellResult
	if json.Unmarshal(raw, &value) != nil || value.MaxOutputLength.IsZero() {
		return nil, failure(generation.ProtocolError)
	}
	if _, ok := value.ID.Value(); !ok {
		return nil, failure(generation.ProtocolError)
	}
	if _, ok := value.Status.Value(); !ok {
		return nil, failure(generation.ProtocolError)
	}
	if shellMetadata(value.ID, value.CallID, value.Status, value.CreatedBy) != nil || shellLimits(generation.Optional[int64]{}, value.MaxOutputLength) != nil {
		return nil, failure(generation.ProtocolError)
	}
	outputs, err := DecodeShellOutputs(value.Output)
	if err != nil {
		return nil, err
	}
	caller, err := decodeCaller(value.Caller)
	if err != nil {
		return nil, err
	}
	return generation.OpenAIShellResult{ID: value.ID, CallID: value.CallID, Status: value.Status, MaxOutputLength: value.MaxOutputLength, Output: outputs, Caller: caller, CreatedBy: value.CreatedBy}, nil
}

func encodeShellOutputs(outputs []generation.ShellOutput) (json.RawMessage, error) {
	values := make([]shellOutput, 0, len(outputs))
	for _, output := range outputs {
		if !utf8.ValidString(output.Stdout) || !utf8.ValidString(output.Stderr) {
			return nil, invalid("", "output must be valid UTF-8")
		}
		if output.CreatedBy.IsNull() {
			return nil, invalid("created_by", "must not be null")
		}
		if value, _ := output.CreatedBy.Value(); !utf8.ValidString(value) {
			return nil, invalid("created_by", "must be valid UTF-8")
		}
		var outcome json.RawMessage
		switch value := output.Outcome.(type) {
		case generation.ShellTimeout:
			outcome = json.RawMessage(`{"type":"timeout"}`)
		case generation.ShellExit:
			var err error
			outcome, err = json.Marshal(struct {
				Type     string `json:"type"`
				ExitCode int64  `json:"exit_code"`
			}{"exit", value.ExitCode})
			if err != nil {
				return nil, err
			}
		default:
			return nil, invalid("outcome", "expected exit or timeout")
		}
		values = append(values, shellOutput{output.Stdout, output.Stderr, outcome, generation.Optional[string]{}})
	}
	return json.Marshal(values)
}

// DecodeShellOutputs reads the complete output chunks of an item or stream event.
func DecodeShellOutputs(raw json.RawMessage) ([]generation.ShellOutput, error) {
	var values []struct {
		Stdout  *string `json:"stdout"`
		Stderr  *string `json:"stderr"`
		Outcome struct {
			Type     string `json:"type"`
			ExitCode *int64 `json:"exit_code"`
		} `json:"outcome"`
		CreatedBy generation.Optional[string] `json:"created_by"`
	}
	if !utf8.Valid(raw) || json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, failure(generation.ProtocolError)
	}
	outputs := make([]generation.ShellOutput, 0, len(values))
	for _, value := range values {
		if value.Stdout == nil || value.Stderr == nil || value.CreatedBy.IsNull() {
			return nil, failure(generation.ProtocolError)
		}
		var outcome generation.ShellOutcome
		switch value.Outcome.Type {
		case "timeout":
			if value.Outcome.ExitCode != nil {
				return nil, failure(generation.ProtocolError)
			}
			outcome = generation.ShellTimeout{}
		case "exit":
			if value.Outcome.ExitCode == nil {
				return nil, failure(generation.ProtocolError)
			}
			outcome = generation.ShellExit{ExitCode: *value.Outcome.ExitCode}
		default:
			return nil, failure(generation.ProtocolError)
		}
		outputs = append(outputs, generation.ShellOutput{Stdout: *value.Stdout, Stderr: *value.Stderr, Outcome: outcome, CreatedBy: value.CreatedBy})
	}
	return outputs, nil
}
