package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type localShellAction struct {
	Type             string                      `json:"type"`
	Command          strictStrings               `json:"command"`
	Env              shellEnv                    `json:"env"`
	TimeoutMs        generation.Optional[int64]  `json:"timeout_ms,omitzero"`
	User             generation.Optional[string] `json:"user,omitzero"`
	WorkingDirectory generation.Optional[string] `json:"working_directory,omitzero"`
}

type shellEnv map[string]string

func (s *shellEnv) UnmarshalJSON(raw []byte) error {
	var values map[string]*string
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	if values == nil {
		return failure(generation.ProtocolError)
	}
	result := make(shellEnv, len(values))
	for key, value := range values {
		if value == nil {
			return failure(generation.ProtocolError)
		}
		result[key] = *value
	}
	*s = result
	return nil
}

type localShellCall struct {
	Type   string           `json:"type"`
	ID     string           `json:"id"`
	CallID string           `json:"call_id"`
	Status string           `json:"status"`
	Action localShellAction `json:"action"`
}
type localShellResult struct {
	Type   string                      `json:"type"`
	ID     string                      `json:"id"`
	Output string                      `json:"output"`
	Status generation.Optional[string] `json:"status,omitzero"`
}

func encodeLocalShellCall(call generation.OpenAILocalShellCall) (json.RawMessage, error) {
	if err := shellMetadata(generation.Some(call.ID), call.CallID, generation.Some(call.Status), generation.Optional[string]{}); err != nil {
		return nil, err
	}
	if err := shellLimits(call.Action.TimeoutMs, generation.Optional[int64]{}); err != nil {
		return nil, at("action", err)
	}
	for _, command := range call.Action.Command {
		if !utf8.ValidString(command) {
			return nil, invalid("action.command", "must be valid UTF-8")
		}
	}
	for key, value := range call.Action.Env {
		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			return nil, invalid("action.env", "must be valid UTF-8")
		}
	}
	for _, field := range []generation.Optional[string]{call.Action.User, call.Action.WorkingDirectory} {
		if value, _ := field.Value(); !utf8.ValidString(value) {
			return nil, invalid("action", "must be valid UTF-8")
		}
	}
	commands, env := call.Action.Command, call.Action.Env
	if commands == nil {
		commands = []string{}
	}
	if env == nil {
		env = map[string]string{}
	}
	return json.Marshal(localShellCall{"local_shell_call", call.ID, call.CallID, call.Status, localShellAction{"exec", commands, env, call.Action.TimeoutMs, call.Action.User, call.Action.WorkingDirectory}})
}

func decodeLocalShellCall(raw json.RawMessage) (generation.Item, error) {
	var value localShellCall
	if json.Unmarshal(raw, &value) != nil || value.Action.Type != "exec" || value.Action.Command == nil || value.Action.Env == nil {
		return nil, failure(generation.ProtocolError)
	}
	call := generation.OpenAILocalShellCall{ID: value.ID, CallID: value.CallID, Status: value.Status, Action: generation.LocalShellAction{Command: value.Action.Command, Env: value.Action.Env, TimeoutMs: value.Action.TimeoutMs, User: value.Action.User, WorkingDirectory: value.Action.WorkingDirectory}}
	if _, err := encodeLocalShellCall(call); err != nil {
		return nil, failure(generation.ProtocolError)
	}
	return call, nil
}

func encodeLocalShellResult(result generation.OpenAILocalShellResult) (json.RawMessage, error) {
	if err := requiredString("id", result.ID); err != nil {
		return nil, err
	}
	if value, ok := result.Status.Value(); ok && !shellStatus(value) {
		return nil, invalid("status", "unsupported shell status")
	}
	if !utf8.ValidString(result.Output) {
		return nil, invalid("output", "must be valid UTF-8")
	}
	return json.Marshal(localShellResult{"local_shell_call_output", result.ID, result.Output, result.Status})
}

func decodeLocalShellResult(raw json.RawMessage) (generation.Item, error) {
	var value struct {
		ID     string                      `json:"id"`
		Output *string                     `json:"output"`
		Status generation.Optional[string] `json:"status"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Output == nil {
		return nil, failure(generation.ProtocolError)
	}
	result := generation.OpenAILocalShellResult{ID: value.ID, Output: *value.Output, Status: value.Status}
	if _, err := encodeLocalShellResult(result); err != nil {
		return nil, failure(generation.ProtocolError)
	}
	return result, nil
}
