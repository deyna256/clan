package wire

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

func encodeItem(item generation.Item) (json.RawMessage, error) {
	switch value := item.(type) {
	case generation.OpenAIConfigurationUpdate:
		return encodeConfigurationUpdate(value)
	case generation.OpenAICompaction:
		return encodeCompaction(value)
	case generation.OpenAICompactionTrigger:
		return encodeCompactionTrigger(value)
	case generation.OpenAIItemReference:
		return encodeItemReference(value)
	case generation.OpenAIProgram:
		return encodeProgram(value)
	case generation.OpenAIProgramOutput:
		return encodeProgramOutput(value)
	case generation.OpenAIToolSearchCall:
		return encodeToolSearchCall(value)
	case generation.OpenAIToolSearchOutput:
		return encodeToolSearchOutput(value)
	case generation.OpenAIAdditionalTools:
		return encodeAdditionalTools(value)
	case generation.OpenAIMCPListTools:
		return encodeMCPListTools(value)
	case generation.OpenAIMCPCall:
		return encodeMCPCall(value)
	case generation.OpenAIMCPApprovalRequest:
		return encodeMCPApprovalRequest(value)
	case generation.OpenAIMCPApprovalResponse:
		return encodeMCPApprovalResponse(value)
	case generation.OpenAIComputerCall:
		return encodeComputerCall(value)
	case generation.OpenAIComputerResult:
		return encodeComputerResult(value)
	case generation.OpenAIShellCall:
		return encodeShellCall(value)
	case generation.OpenAIShellResult:
		return encodeShellResult(value)
	case generation.OpenAILocalShellCall:
		return encodeLocalShellCall(value)
	case generation.OpenAILocalShellResult:
		return encodeLocalShellResult(value)
	case generation.OpenAIApplyPatchCall:
		return encodePatchCall(value)
	case generation.OpenAIApplyPatchResult:
		return encodePatchResult(value)
	case generation.CustomToolCall:
		return encodeCustomCall(value)
	case generation.CustomToolResult:
		return encodeCustomResult(value)
	case generation.OpenAIImageGenerationCall:
		return encodeImageCall(value)
	case generation.OpenAICodeInterpreterCall:
		return encodeInterpreterCall(value)
	case generation.Message:
		return encodeMessage(value)
	case generation.Reasoning:
		return encodeReasoning(value)
	case generation.ToolCall:
		return encodeCall(value)
	case generation.ToolResult:
		return encodeToolResult(value)
	case generation.OpenAIWebSearchCall:
		return encodeWebSearchCall(value)
	case generation.OpenAIFileSearchCall:
		return encodeFileSearchCall(value)
	default:
		return nil, invalid("", "expected a supported item value")
	}
}

func encodeMessage(message generation.Message) (json.RawMessage, error) {
	switch message.Role {
	case generation.User, generation.System, generation.Developer, generation.Assistant:
	default:
		return nil, invalid("role", "unsupported message role")
	}
	if err := itemMetadata(message.ID, message.Status); err != nil {
		return nil, err
	}
	if !message.OpenAI.Phase.IsZero() && message.Role != generation.Assistant {
		return nil, invalid("phase", "only assistant messages have a phase")
	}
	if phase, ok := message.OpenAI.Phase.Value(); ok && phase != "commentary" && phase != "final_answer" {
		return nil, invalid("phase", "unsupported assistant phase")
	}
	if len(message.Parts) == 0 {
		return nil, invalid("content", "at least one part is required")
	}
	output := message.Role == generation.Assistant && message.ID != ""
	if output && message.Status == "" {
		return nil, invalid("status", "replayed output messages require a status")
	}
	parts, err := encodeParts(message.Parts, output)
	if err != nil {
		return nil, at("content", err)
	}
	return json.Marshal(struct {
		Type    string                      `json:"type"`
		ID      string                      `json:"id,omitempty"`
		Role    generation.Role             `json:"role"`
		Content []json.RawMessage           `json:"content"`
		Status  generation.ItemStatus       `json:"status,omitempty"`
		Phase   generation.Optional[string] `json:"phase,omitzero"`
	}{"message", message.ID, message.Role, parts, message.Status, message.OpenAI.Phase})
}

func encodeReasoning(reasoning generation.Reasoning) (json.RawMessage, error) {
	if err := requiredString("id", reasoning.ID); err != nil {
		return nil, err
	}
	if err := itemMetadata(reasoning.ID, reasoning.Status); err != nil {
		return nil, err
	}
	encrypted := reasoning.OpenAI.EncryptedContent
	if value, ok := encrypted.Value(); ok && !utf8.ValidString(value) {
		return nil, invalid("encrypted_content", "must be valid UTF-8")
	}
	summary := make([]textPart, 0)
	var content []textPart
	for i, part := range reasoning.Parts {
		switch value := part.(type) {
		case generation.ReasoningText:
			if !utf8.ValidString(value.Text) {
				return nil, invalid(fmt.Sprintf("parts[%d]", i), "must be valid UTF-8")
			}
			content = append(content, textPart{Type: "reasoning_text", Text: value.Text})
		case generation.ReasoningSummary:
			if !utf8.ValidString(value.Text) {
				return nil, invalid(fmt.Sprintf("parts[%d]", i), "must be valid UTF-8")
			}
			summary = append(summary, textPart{Type: "summary_text", Text: value.Text})
		default:
			return nil, invalid(fmt.Sprintf("parts[%d]", i), "expected reasoning text or summary")
		}
	}
	return json.Marshal(struct {
		Type             string                      `json:"type"`
		ID               string                      `json:"id"`
		Summary          []textPart                  `json:"summary"`
		Content          []textPart                  `json:"content,omitempty"`
		EncryptedContent generation.Optional[string] `json:"encrypted_content,omitzero"`
		Status           generation.ItemStatus       `json:"status,omitempty"`
	}{"reasoning", reasoning.ID, summary, content, encrypted, reasoning.Status})
}

func encodeToolOutput(output generation.ToolOutput) (json.RawMessage, error) {
	switch value := output.(type) {
	case generation.ToolTextOutput:
		if !utf8.ValidString(string(value)) {
			return nil, invalid("", "must be valid UTF-8")
		}
		return json.Marshal(string(value))
	case generation.ToolPartsOutput:
		parts, err := encodeParts(value, false)
		if err != nil {
			return nil, err
		}
		return json.Marshal(parts)
	default:
		return nil, invalid("", "expected text or content parts")
	}
}

func encodeParts(parts []generation.Part, output bool) ([]json.RawMessage, error) {
	encoded := make([]json.RawMessage, 0, len(parts))
	for i, part := range parts {
		body, err := encodePart(part, output)
		if err != nil {
			return nil, at(fmt.Sprintf("[%d]", i), err)
		}
		encoded = append(encoded, body)
	}
	return encoded, nil
}

type textPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func encodePart(part generation.Part, output bool) (json.RawMessage, error) {
	switch value := part.(type) {
	case generation.Text:
		if !utf8.ValidString(value.Text) {
			return nil, invalid("text", "must be valid UTF-8")
		}
		if output {
			if value.OpenAI.PromptCacheBreakpoint {
				return nil, invalid("prompt_cache_breakpoint", "requires an input content part")
			}
			return encodeText(value)
		}
		if len(value.OpenAI.Annotations) != 0 || len(value.OpenAI.Logprobs) != 0 {
			return nil, invalid("", "text metadata requires an assistant output message")
		}
		return json.Marshal(struct {
			Type       string          `json:"type"`
			Text       string          `json:"text"`
			Breakpoint json.RawMessage `json:"prompt_cache_breakpoint,omitempty"`
		}{"input_text", value.Text, explicitBreakpoint(value.OpenAI.PromptCacheBreakpoint)})
	case generation.Refusal:
		if !output {
			return nil, invalid("", "refusal requires an assistant output message")
		}
		if !utf8.ValidString(value.Text) {
			return nil, invalid("refusal", "must be valid UTF-8")
		}
		return json.Marshal(struct {
			Type    string `json:"type"`
			Refusal string `json:"refusal"`
		}{"refusal", value.Text})
	case generation.ImageURL:
		if output {
			return nil, invalid("", "input images cannot be assistant output parts")
		}
		if err := imageURL(value.URL); err != nil {
			return nil, err
		}
		return encodeImage(value.URL, "", value.Detail, value.OpenAI.PromptCacheBreakpoint)
	case generation.ImageFile:
		if output {
			return nil, invalid("", "input images cannot be assistant output parts")
		}
		if err := requiredString("file_id", value.FileID); err != nil {
			return nil, err
		}
		return encodeImage("", value.FileID, value.Detail, value.OpenAI.PromptCacheBreakpoint)
	case generation.FileID, generation.FileURL, generation.FileData:
		return encodeFile(part, output)
	case generation.OpenAIStoredImage:
		return encodeStoredImage(value, output)
	case generation.OpenAIStoredFile:
		return encodeStoredFile(value, output)
	default:
		return nil, invalid("", "expected a supported content part value")
	}
}

func encodeImage(imageURL, fileID, detail string, breakpoint bool) (json.RawMessage, error) {
	switch detail {
	case "", "auto", "low", "high", "original":
	default:
		return nil, invalid("detail", "unsupported image detail")
	}
	return json.Marshal(struct {
		Type       string          `json:"type"`
		URL        string          `json:"image_url,omitempty"`
		FileID     string          `json:"file_id,omitempty"`
		Detail     string          `json:"detail,omitempty"`
		Breakpoint json.RawMessage `json:"prompt_cache_breakpoint,omitempty"`
	}{"input_image", imageURL, fileID, detail, explicitBreakpoint(breakpoint)})
}

func imageURL(value string) error {
	if !utf8.ValidString(value) {
		return invalid("image_url", "must be valid UTF-8")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return invalid("image_url", "invalid image URL")
	}
	if (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil {
		return nil
	}
	if parsed.Scheme == "data" && strings.HasPrefix(parsed.Opaque, "image/") && strings.Contains(parsed.Opaque, ";base64,") {
		return nil
	}
	return invalid("image_url", "expected HTTP(S) or a base64 image data URL")
}

func itemMetadata(id string, status generation.ItemStatus) error {
	if !utf8.ValidString(id) {
		return invalid("id", "must be valid UTF-8")
	}
	switch status {
	case "", generation.ItemInProgress, generation.ItemCompleted, generation.ItemIncomplete:
		return nil
	default:
		return invalid("status", "unsupported item status")
	}
}
