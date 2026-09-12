package wire

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeInterpreterTool(tool generation.OpenAICodeInterpreterTool) (json.RawMessage, error) {
	container, err := encodeInterpreterContainer(tool.Container)
	if err != nil {
		return nil, at("container", err)
	}
	if callers, ok := tool.AllowedCallers.Value(); ok {
		if err := allowedCallers(callers); err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		Type      string                        `json:"type"`
		Container json.RawMessage               `json:"container"`
		Callers   generation.Optional[[]string] `json:"allowed_callers,omitzero"`
	}{"code_interpreter", container, tool.AllowedCallers})
}

func encodeInterpreterContainer(container generation.InterpreterContainer) (json.RawMessage, error) {
	switch value := container.(type) {
	case generation.InterpreterContainerID:
		if err := requiredString("", string(value)); err != nil {
			return nil, err
		}
		return json.Marshal(string(value))
	case generation.InterpreterAutoContainer:
		if memory, ok := value.MemoryLimit.Value(); ok && memory != "1g" && memory != "4g" && memory != "16g" && memory != "64g" {
			return nil, invalid("memory_limit", "unsupported memory limit")
		}
		for _, id := range value.FileIDs {
			if err := requiredString("file_ids", id); err != nil {
				return nil, err
			}
		}
		policy, err := EncodeContainerNetwork(value.NetworkPolicy)
		if err != nil {
			return nil, at("network_policy", err)
		}
		return json.Marshal(struct {
			Type    string                      `json:"type"`
			Files   []string                    `json:"file_ids,omitzero"`
			Memory  generation.Optional[string] `json:"memory_limit,omitzero"`
			Network json.RawMessage             `json:"network_policy,omitempty"`
		}{"auto", value.FileIDs, value.MemoryLimit, policy})
	default:
		return nil, invalid("", "expected a container ID or auto configuration")
	}
}

// EncodeContainerNetwork encodes the shared hosted-container network policy.
func EncodeContainerNetwork(policy generation.InterpreterNetworkPolicy) (json.RawMessage, error) {
	switch value := policy.(type) {
	case nil:
		return nil, nil
	case generation.InterpreterNetworkDisabled:
		return json.RawMessage(`{"type":"disabled"}`), nil
	case generation.InterpreterNetworkAllowlist:
		domains := value.Domains
		if domains == nil {
			domains = []string{}
		}
		for _, domain := range domains {
			if err := requiredString("allowed_domains", domain); err != nil {
				return nil, err
			}
		}
		type domainSecret struct {
			Domain string `json:"domain"`
			Name   string `json:"name"`
			Value  string `json:"value"`
		}
		var secrets []domainSecret
		if value.Secrets != nil {
			secrets = make([]domainSecret, len(value.Secrets))
		}
		for i, secret := range value.Secrets {
			for _, field := range []string{secret.Domain, secret.Name} {
				if err := requiredString("domain_secrets", field); err != nil {
					return nil, err
				}
			}
			if !utf8.ValidString(secret.Value) {
				return nil, invalid("domain_secrets", "value must be valid UTF-8")
			}
			secrets[i] = domainSecret{secret.Domain, secret.Name, secret.Value}
		}
		return json.Marshal(struct {
			Type    string         `json:"type"`
			Domains []string       `json:"allowed_domains"`
			Secrets []domainSecret `json:"domain_secrets,omitzero"`
		}{"allowlist", domains, secrets})
	default:
		return nil, invalid("", "unsupported network policy")
	}
}

type interpreterCall struct {
	Type        string                      `json:"type"`
	ID          string                      `json:"id"`
	ContainerID string                      `json:"container_id"`
	Status      string                      `json:"status"`
	Code        generation.Optional[string] `json:"code"`
	Outputs     json.RawMessage             `json:"outputs"`
}

func interpreterStatus(status string) bool {
	switch status {
	case "in_progress", "interpreting", "completed", "incomplete", "failed":
		return true
	default:
		return false
	}
}

func encodeInterpreterCall(call generation.OpenAICodeInterpreterCall) (json.RawMessage, error) {
	if err := requiredString("id", call.ID); err != nil {
		return nil, err
	}
	if err := requiredString("container_id", call.ContainerID); err != nil {
		return nil, err
	}
	if !interpreterStatus(call.Status) {
		return nil, invalid("status", "unsupported interpreter status")
	}
	if code, ok := call.Code.Value(); ok && !utf8.ValidString(code) {
		return nil, invalid("code", "must be valid UTF-8")
	}
	var outputs json.RawMessage
	if values, ok := call.Outputs.Value(); ok {
		encoded := make([]json.RawMessage, 0, len(values))
		for i, value := range values {
			raw, err := encodeInterpreterOutput(value)
			if err != nil {
				return nil, at(fmt.Sprintf("outputs[%d]", i), err)
			}
			encoded = append(encoded, raw)
		}
		var err error
		outputs, err = json.Marshal(encoded)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(interpreterCall{"code_interpreter_call", call.ID, call.ContainerID, call.Status, call.Code, outputs})
}

func encodeInterpreterOutput(output generation.InterpreterOutput) (json.RawMessage, error) {
	switch value := output.(type) {
	case generation.InterpreterLogs:
		if !utf8.ValidString(value.Logs) {
			return nil, invalid("logs", "must be valid UTF-8")
		}
		return json.Marshal(struct {
			Type string `json:"type"`
			Logs string `json:"logs"`
		}{"logs", value.Logs})
	case generation.InterpreterImage:
		if err := requiredString("url", value.URL); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		}{"image", value.URL})
	default:
		return nil, invalid("", "unsupported interpreter output")
	}
}

func decodeInterpreterCall(data []byte, warn func(string)) (generation.Item, error) {
	var call interpreterCall
	if json.Unmarshal(data, &call) != nil || requiredString("id", call.ID) != nil || !interpreterStatus(call.Status) {
		return nil, failure(generation.ProtocolError)
	}
	result := generation.OpenAICodeInterpreterCall{ID: call.ID, ContainerID: call.ContainerID, Status: call.Status, Code: call.Code}
	if len(call.Outputs) == 0 {
		return result, nil
	}
	if absent(call.Outputs) {
		result.Outputs = generation.Null[[]generation.InterpreterOutput]()
		return result, nil
	}
	var raw []json.RawMessage
	if json.Unmarshal(call.Outputs, &raw) != nil {
		warn("invalid_interpreter_outputs")
		return result, nil
	}
	outputs := make([]generation.InterpreterOutput, 0, len(raw))
	for _, value := range raw {
		output, err := decodeInterpreterOutput(value)
		if err != nil {
			warn("invalid_interpreter_outputs")
			continue
		}
		outputs = append(outputs, output)
	}
	result.Outputs = generation.Some(outputs)
	return result, nil
}

func decodeInterpreterOutput(data []byte) (generation.InterpreterOutput, error) {
	var output struct {
		Type string  `json:"type"`
		Logs *string `json:"logs"`
		URL  *string `json:"url"`
	}
	if json.Unmarshal(data, &output) != nil {
		return nil, failure(generation.ProtocolError)
	}
	switch output.Type {
	case "logs":
		if output.Logs != nil {
			return generation.InterpreterLogs{Logs: *output.Logs}, nil
		}
	case "image":
		if output.URL != nil && requiredString("url", *output.URL) == nil {
			return generation.InterpreterImage{URL: *output.URL}, nil
		}
	}
	return nil, failure(generation.ProtocolError)
}
