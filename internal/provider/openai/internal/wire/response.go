package wire

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

// ResponseFields keeps usage separate from content decoding for generation and
// stored resources. Callers can retain usage when content is invalid.
type ResponseFields struct {
	ID     string            `json:"id"`
	Model  string            `json:"model"`
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Usage  json.RawMessage   `json:"usage"`
}

type ResponseEnvelope struct {
	ResponseFields
	Error             ProviderError `json:"error"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type ProviderError struct {
	Code string `json:"code"`
}

func DecodeEnvelope(data []byte) (ResponseEnvelope, error) {
	var response ResponseEnvelope
	err := json.Unmarshal(data, &response)
	if !utf8.Valid(data) {
		// Keep accounting without exposing identities repaired by encoding/json.
		var identity struct {
			ID    json.RawMessage `json:"id"`
			Model json.RawMessage `json:"model"`
		}
		if json.Unmarshal(data, &identity) != nil {
			response.ID, response.Model = "", ""
		} else {
			if !utf8.Valid(identity.ID) {
				response.ID = ""
			}
			if !utf8.Valid(identity.Model) {
				response.Model = ""
			}
		}
		return response, failure(generation.ProtocolError)
	}
	if err != nil {
		return response, failure(generation.ProtocolError)
	}
	return response, nil
}

// Result converts terminal content; usage is handled independently by the caller.
func (r ResponseEnvelope) Result(warn func(string)) (generation.Result, error) {
	result := generation.Result{Identity: generation.Identity{ID: r.ID, Model: r.Model}}
	if r.Status == "failed" {
		return result, r.Error.Failure()
	}
	if r.ID == "" || r.Model == "" || r.Output == nil || (r.Status != "completed" && r.Status != "incomplete") {
		return result, failure(generation.ProtocolError)
	}
	finish := generation.Finish{Status: r.Status, Reason: "stop"}
	if r.Status == "incomplete" {
		if r.IncompleteDetails.Reason == "" {
			return result, failure(generation.ProtocolError)
		}
		finish.Reason = r.IncompleteDetails.Reason
	}
	output, toolCalls, err := r.TerminalOutput(warn)
	if err != nil {
		return result, err
	}
	if toolCalls {
		finish.Reason = "tool_calls"
	}
	result.Response = generation.Response{Output: output, Finish: finish}
	return result, nil
}

// TerminalOutput validates final items without imposing generation status on a
// stored response. The boolean identifies calls requiring client execution.
func (r ResponseFields) TerminalOutput(warn func(string)) ([]generation.Item, bool, error) {
	toolCalls := false
	output := make([]generation.Item, 0, len(r.Output))
	itemIDs := make(map[string]bool)
	callIDs, resultIDs := make(map[string]string), make(map[string]string)
	for _, raw := range r.Output {
		item, err := DecodeItem(raw, r.Status == "completed", warn)
		if err != nil {
			return nil, false, err
		}
		var fields struct {
			ID, Type string
			CallID   json.RawMessage `json:"call_id"`
		}
		if json.Unmarshal(raw, &fields) != nil {
			return nil, false, failure(generation.ProtocolError)
		}
		if id := fields.ID; id != "" {
			if itemIDs[id] {
				return nil, false, failure(generation.ProtocolError)
			}
			itemIDs[id] = true
		}
		kind := fields.Type
		switch kind {
		case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output",
			"shell_call", "shell_call_output", "local_shell_call", "local_shell_call_output",
			"computer_call", "computer_call_output", "apply_patch_call", "apply_patch_call_output",
			"tool_search_call", "tool_search_output", "program", "program_output":
		default:
			kind = ""
		}
		if kind != "" && !absent(fields.CallID) {
			var callID string
			if json.Unmarshal(fields.CallID, &callID) != nil {
				return nil, false, failure(generation.ProtocolError)
			}
			seen, paired := callIDs, resultIDs
			if strings.HasSuffix(kind, "_output") {
				kind = strings.TrimSuffix(kind, "_output")
				if kind == "tool_search" {
					kind = "tool_search_call"
				}
				seen, paired = resultIDs, callIDs
			}
			if callID != "" {
				if seen[callID] != "" || (paired[callID] != "" && paired[callID] != kind) {
					return nil, false, failure(generation.ProtocolError)
				}
				seen[callID] = kind
			}
		}
		switch call := item.(type) {
		case generation.ToolCall:
			if call.CallID == "" || strings.TrimSpace(call.Name) == "" {
				return nil, false, failure(generation.ProtocolError)
			}
			toolCalls = toolCalls || r.Status == "completed"
		case generation.CustomToolCall:
			if requiredString("call_id", call.CallID) != nil || requiredString("name", call.Name) != nil {
				return nil, false, failure(generation.ProtocolError)
			}
			toolCalls = toolCalls || r.Status == "completed"
		case generation.OpenAIApplyPatchCall, generation.OpenAIComputerCall, generation.OpenAIMCPApprovalRequest, generation.OpenAILocalShellCall:
			toolCalls = toolCalls || r.Status == "completed"
		case generation.OpenAIToolSearchCall:
			execution, _ := call.Execution.Value()
			if execution == "client" && r.Status == "completed" {
				callID, _ := call.CallID.Value()
				if requiredString("call_id", callID) != nil {
					return nil, false, failure(generation.ProtocolError)
				}
				toolCalls = true
			}
		case generation.OpenAIShellCall:
			environment, _ := call.Environment.Value()
			if _, hosted := environment.(generation.ShellContainerReference); !hosted {
				toolCalls = toolCalls || r.Status == "completed"
			}
		case generation.OpenAICodeInterpreterCall:
			if strings.TrimSpace(call.ContainerID) == "" {
				return nil, false, failure(generation.ProtocolError)
			}
		}
		output = append(output, item)
	}
	return output, toolCalls, nil
}

func (e ProviderError) Failure() *generation.Failure {
	switch e.Code {
	case "rate_limit_exceeded":
		return &generation.Failure{Kind: generation.RateLimited, Retryable: true}
	case "insufficient_quota", "billing_hard_limit_reached":
		return failure(generation.QuotaExhausted)
	case "invalid_api_key":
		return failure(generation.Authentication)
	case "server_error", "overloaded":
		return &generation.Failure{Kind: generation.Unavailable, Retryable: true}
	case "invalid_request", "invalid_request_error", "context_length_exceeded":
		return failure(generation.InvalidRequest)
	default:
		return failure(generation.Unavailable)
	}
}

func failure(kind generation.FailureKind) *generation.Failure {
	return &generation.Failure{Kind: kind}
}

// DecodeItem reads supported output values. complete requires executable function
// arguments; incomplete generations retain their unfinished argument strings.
func DecodeItem(data []byte, complete bool, warn func(string)) (generation.Item, error) {
	var head struct {
		Type string `json:"type"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &head) != nil {
		return nil, failure(generation.ProtocolError)
	}
	switch head.Type {
	case "compaction":
		return decodeCompaction(data, false)
	case "program":
		return decodeProgram(data, complete)
	case "program_output":
		return decodeProgramOutput(data, complete)
	case "tool_search_call", "tool_search_output":
		return decodeToolSearchItem(data)
	case "additional_tools":
		return decodeAdditionalTools(data)
	case "mcp_list_tools":
		return decodeMCPListTools(data)
	case "mcp_call":
		return decodeMCPCall(data)
	case "mcp_approval_request":
		return decodeMCPApprovalRequest(data)
	case "mcp_approval_response":
		return decodeMCPApprovalResponse(data)
	case "computer_call":
		return decodeComputerCall(data)
	case "computer_call_output":
		return decodeComputerResult(data)
	case "shell_call":
		return decodeShellCall(data)
	case "shell_call_output":
		return decodeShellResult(data)
	case "local_shell_call":
		return decodeLocalShellCall(data)
	case "local_shell_call_output":
		return decodeLocalShellResult(data)
	case "apply_patch_call":
		return decodePatchCall(data)
	case "apply_patch_call_output":
		return decodePatchResult(data)
	case "custom_tool_call":
		return decodeCustomCall(data)
	case "custom_tool_call_output":
		return decodeCustomResult(data)
	case "image_generation_call":
		return decodeImageCall(data, warn)
	case "code_interpreter_call":
		return decodeInterpreterCall(data, warn)
	case "message":
		return decodeMessage(data, warn)
	case "reasoning":
		return decodeReasoning(data, warn)
	case "function_call":
		return decodeCall(data, complete)
	case "function_call_output":
		return decodeToolResult(data)
	case "web_search_call":
		return decodeWebSearchCall(data, warn)
	case "file_search_call":
		return decodeFileSearchCall(data, warn)
	default:
		return nil, failure(generation.Unsupported)
	}
}

func decodeMessage(data []byte, warn func(string)) (generation.Item, error) {
	var message struct {
		ID      string                `json:"id"`
		Role    generation.Role       `json:"role"`
		Status  generation.ItemStatus `json:"status"`
		Phase   json.RawMessage       `json:"phase"`
		Content []json.RawMessage     `json:"content"`
	}
	if json.Unmarshal(data, &message) != nil || message.ID == "" || message.Role != generation.Assistant || message.Status == "" || message.Content == nil || itemMetadata(message.ID, message.Status) != nil {
		return nil, failure(generation.ProtocolError)
	}
	result := generation.Message{ID: message.ID, Role: message.Role, Status: message.Status}
	if len(message.Phase) != 0 {
		phase, err := optionalString(message.Phase)
		if err != nil {
			return nil, err
		}
		result.OpenAI.Phase = phase
	}
	for _, raw := range message.Content {
		part, err := DecodePart(raw, warn)
		if err != nil {
			return nil, err
		}
		switch part.(type) {
		case generation.Text, generation.Refusal:
			result.Parts = append(result.Parts, part)
		default:
			return nil, failure(generation.ProtocolError)
		}
	}
	return result, nil
}

func decodeReasoning(data []byte, warn func(string)) (generation.Item, error) {
	var item struct {
		ID               string                `json:"id"`
		Status           generation.ItemStatus `json:"status"`
		Summary          []json.RawMessage     `json:"summary"`
		Content          []json.RawMessage     `json:"content"`
		EncryptedContent json.RawMessage       `json:"encrypted_content"`
	}
	if json.Unmarshal(data, &item) != nil || item.ID == "" || itemMetadata(item.ID, item.Status) != nil {
		return nil, failure(generation.ProtocolError)
	}
	result := generation.Reasoning{ID: item.ID, Status: item.Status}
	for _, group := range []struct {
		parts   []json.RawMessage
		summary bool
	}{{item.Summary, true}, {item.Content, false}} {
		for _, raw := range group.parts {
			part, err := DecodePart(raw, warn)
			if err != nil {
				return nil, err
			}
			_, summary := part.(generation.ReasoningSummary)
			_, text := part.(generation.ReasoningText)
			if (group.summary && !summary) || (!group.summary && !text) {
				return nil, failure(generation.ProtocolError)
			}
			result.Parts = append(result.Parts, part)
		}
	}
	if len(item.EncryptedContent) != 0 {
		encrypted, err := optionalString(item.EncryptedContent)
		if err != nil {
			return nil, err
		}
		result.OpenAI.EncryptedContent = encrypted
	}
	return result, nil
}

func DecodePart(data []byte, warn func(string)) (generation.Part, error) {
	var part struct {
		Type        string          `json:"type"`
		Text        *string         `json:"text"`
		Refusal     *string         `json:"refusal"`
		Annotations json.RawMessage `json:"annotations"`
		Logprobs    json.RawMessage `json:"logprobs"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &part) != nil {
		return nil, failure(generation.ProtocolError)
	}
	if part.Type == "refusal" && part.Refusal != nil {
		return generation.Refusal{Text: *part.Refusal}, nil
	}
	if part.Text == nil {
		return nil, failure(generation.ProtocolError)
	}
	switch part.Type {
	case "output_text":
		return generation.Text{Text: *part.Text, OpenAI: decodeTextData(part.Annotations, part.Logprobs, warn)}, nil
	case "summary_text":
		return generation.ReasoningSummary{Text: *part.Text}, nil
	case "reasoning_text":
		return generation.ReasoningText{Text: *part.Text}, nil
	default:
		return nil, failure(generation.Unsupported)
	}
}

func optionalString(raw json.RawMessage) (generation.Optional[string], error) {
	if string(raw) == "null" {
		return generation.Null[string](), nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return generation.Optional[string]{}, failure(generation.ProtocolError)
	}
	return generation.Some(value), nil
}
