// Package codex implements one Codex Responses attempt and account model discovery.
package codex

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

const maxPayload = 16 << 20

// ValidationError identifies an invalid or unsupported client field, without its value.
type ValidationError struct{ Field string }

func (e *ValidationError) Error() string { return "codex: invalid or unsupported field " + e.Field }

// Request owns validated request data. Construct it with ParseRequest.
// Its zero value is invalid. Copies share immutable storage and are safe to use concurrently.
type Request struct {
	body          []byte
	model         string
	stream        bool
	omittedCap    bool
	effort        string
	summary       bool
	verbosity     bool
	originalImage bool
	image         bool
}

// Model returns the requested model ID without substituting a default.
func (r Request) Model() string { return r.model }

// Streaming reports the client's requested response format.
func (r Request) Streaming() bool { return r.stream }

// OmittedMaxOutputTokens tells HTTP to warn once for this request. The cap is not enforced.
func (r Request) OmittedMaxOutputTokens() bool { return r.omittedCap }

// ValidateModel checks this request against one account's catalog entry.
// Callers must establish enabled-account membership and perform this check before dispatch.
func (r Request) ValidateModel(model Model) error {
	if len(r.body) == 0 || r.model != model.ID {
		return invalid("model")
	}
	if r.effort != "" && !slices.Contains(model.ReasoningEfforts, r.effort) {
		return invalid("reasoning.effort")
	}
	if r.summary && !model.SupportsReasoningSummary {
		return invalid("reasoning.summary")
	}
	if r.verbosity && !model.SupportsVerbosity {
		return invalid("text.verbosity")
	}
	if r.originalImage && !model.SupportsImageDetailOriginal {
		return invalid("input.image.detail")
	}
	if r.image && !slices.Contains(model.InputModalities, "image") {
		return invalid("input.image")
	}
	return nil
}

// ParseRequest validates the supported Responses subset and owns an independent wire body.
// Content is retained as JSON; schema properties and function data are not protocol controls.
func ParseRequest(data []byte) (Request, error) {
	var r Request
	if len(data) > maxPayload {
		return r, invalid("request")
	}
	fields, err := object(data, "request", "model input instructions stream store background max_output_tokens prompt_cache_key tools tool_choice parallel_tool_calls reasoning include text")
	if err != nil {
		return r, err
	}
	omitNull(fields, "instructions stream store background max_output_tokens prompt_cache_key parallel_tool_calls reasoning include")
	r.model, err = textValue(fields["model"], "model", false)
	if err != nil {
		return r, err
	}
	for name, raw := range fields {
		switch name {
		case "model", "input":
		case "instructions", "prompt_cache_key":
			_, err = textValue(raw, name, true)
		case "stream", "store", "background", "parallel_tool_calls":
			var value bool
			value, err = boolValue(raw, name)
			if name == "stream" {
				r.stream = value
			}
			if (name == "store" || name == "background") && value {
				err = invalid(name)
			}
		case "max_output_tokens":
			var value int64
			if json.Unmarshal(raw, &value) != nil || value < 1 {
				err = invalid(name)
			}
			r.omittedCap = true
		case "tools":
			fields[name], err = validateTools(raw)
		case "tool_choice":
			err = validateToolChoice(raw)
		case "reasoning":
			fields[name], err = r.validateReasoning(raw)
		case "include":
			var values []string
			if !isArray(raw) || json.Unmarshal(raw, &values) != nil {
				err = invalid(name)
				break
			}
			for _, value := range values {
				if value != "reasoning.encrypted_content" {
					err = invalid(name)
				}
			}
		case "text":
			fields[name], err = r.validateText(raw)
		}
		if err != nil {
			return Request{}, err
		}
	}
	input, err := r.validateInput(fields["input"])
	if err != nil {
		return Request{}, err
	}
	fields["input"] = input
	fields["stream"] = json.RawMessage("true")
	fields["store"] = json.RawMessage("false")
	delete(fields, "max_output_tokens")
	r.body, err = json.Marshal(fields)
	return r, err
}

func validateToolChoice(raw json.RawMessage) error {
	if len(raw) != 0 && raw[0] == '"' {
		_, err := enumValue(raw, "tool_choice", "auto none required")
		return err
	}
	fields, err := object(raw, "tool_choice", "type name")
	if err != nil {
		return err
	}
	if _, err = enumValue(fields["type"], "tool_choice.type", "function"); err != nil {
		return err
	}
	_, err = textValue(fields["name"], "tool_choice.name", false)
	return err
}

func invalid(field string) error { return &ValidationError{Field: field} }

func object(raw []byte, path, names string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, invalid(path)
	}
	if names != "" {
		if err := allowed(fields, path, names); err != nil {
			return nil, err
		}
	}
	return fields, nil
}

func allowed(fields map[string]json.RawMessage, path, names string) error {
	for name := range fields {
		if !slices.Contains(strings.Fields(names), name) {
			return invalid(path + "." + name)
		}
	}
	return nil
}

func textValue(raw []byte, path string, empty bool) (string, error) {
	var value string
	if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return "", invalid(path)
	}
	if !empty && strings.TrimSpace(value) == "" {
		return "", invalid(path)
	}
	return value, nil
}

func enumValue(raw []byte, path, names string) (string, error) {
	value, err := textValue(raw, path, false)
	if err != nil {
		return "", err
	}
	if !slices.Contains(strings.Fields(names), value) {
		return "", invalid(path)
	}
	return value, nil
}

func boolValue(raw []byte, path string) (bool, error) {
	var value bool
	if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return false, invalid(path)
	}
	return value, nil
}

func isArray(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) != 0 && raw[0] == '['
}

func omitNull(fields map[string]json.RawMessage, names string) {
	for _, name := range strings.Fields(names) {
		if bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			delete(fields, name)
		}
	}
}

func array(raw []byte, path string) ([]json.RawMessage, error) {
	var values []json.RawMessage
	if !isArray(raw) || json.Unmarshal(raw, &values) != nil {
		return nil, invalid(path)
	}
	return values, nil
}
