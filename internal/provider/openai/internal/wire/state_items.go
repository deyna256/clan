package wire

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
)

type configurationReasoning struct {
	Effort generation.Optional[string] `json:"effort,omitzero"`
}
type configurationUpdate struct {
	Type      string                                      `json:"type"`
	ID        generation.Optional[string]                 `json:"id,omitzero"`
	Reasoning generation.Optional[configurationReasoning] `json:"reasoning,omitzero"`
}

func encodeConfigurationUpdate(item generation.OpenAIConfigurationUpdate) (json.RawMessage, error) {
	if err := stateInputID(item.ID); err != nil {
		return nil, err
	}
	if item.Reasoning.IsNull() {
		return nil, invalid("reasoning", "must not be null")
	}
	var reasoning generation.Optional[configurationReasoning]
	if value, ok := item.Reasoning.Value(); ok {
		if err := configurationEffort(value.Effort); err != nil {
			return nil, err
		}
		reasoning = generation.Some(configurationReasoning{Effort: value.Effort})
	}
	return json.Marshal(configurationUpdate{Type: "configuration_update", ID: item.ID, Reasoning: reasoning})
}

func configurationEffort(effort generation.Optional[string]) error {
	if value, ok := effort.Value(); ok {
		switch value {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return invalid("reasoning.effort", "unsupported reasoning effort")
		}
	}
	return nil
}

// DecodeConfigurationUpdate converts a stored configuration item, not generation output.
func DecodeConfigurationUpdate(raw json.RawMessage) (generation.Item, error) {
	var value configurationUpdate
	if !utf8.Valid(raw) || json.Unmarshal(raw, &value) != nil || value.Type != "configuration_update" || value.Reasoning.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	id, present := value.ID.Value()
	if !present || requiredString("id", id) != nil {
		return nil, failure(generation.ProtocolError)
	}
	item := generation.OpenAIConfigurationUpdate{ID: value.ID}
	if reasoning, ok := value.Reasoning.Value(); ok {
		if configurationEffort(reasoning.Effort) != nil {
			return nil, failure(generation.ProtocolError)
		}
		item.Reasoning = generation.Some(generation.ConfigurationReasoning{Effort: reasoning.Effort})
	}
	return item, nil
}

type compaction struct {
	Type             string                      `json:"type"`
	ID               generation.Optional[string] `json:"id,omitzero"`
	EncryptedContent generation.Optional[string] `json:"encrypted_content,omitzero"`
	CreatedBy        generation.Optional[string] `json:"created_by,omitzero"`
}

func encodeCompaction(item generation.OpenAICompaction) (json.RawMessage, error) {
	if err := stateInputID(item.ID); err != nil {
		return nil, err
	}
	content, present := item.EncryptedContent.Value()
	if !present || !utf8.ValidString(content) {
		return nil, invalid("encrypted_content", "must be valid UTF-8")
	}
	return json.Marshal(compaction{Type: "compaction", ID: item.ID, EncryptedContent: item.EncryptedContent})
}

func decodeCompaction(raw json.RawMessage, complete bool) (generation.Item, error) {
	var value compaction
	if json.Unmarshal(raw, &value) != nil || value.EncryptedContent.IsNull() || value.CreatedBy.IsNull() {
		return nil, failure(generation.ProtocolError)
	}
	_, hasContent := value.EncryptedContent.Value()
	id, present := value.ID.Value()
	if !present || requiredString("id", id) != nil || (complete && !hasContent) {
		return nil, failure(generation.ProtocolError)
	}
	return generation.OpenAICompaction{ID: value.ID, EncryptedContent: value.EncryptedContent, CreatedBy: value.CreatedBy}, nil
}

func encodeCompactionTrigger(item generation.OpenAICompactionTrigger) (json.RawMessage, error) {
	if err := stateInputID(item.ID); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type string                      `json:"type"`
		ID   generation.Optional[string] `json:"id,omitzero"`
	}{"compaction_trigger", item.ID})
}

func encodeItemReference(item generation.OpenAIItemReference) (json.RawMessage, error) {
	if err := requiredString("id", item.ID); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}{"item_reference", item.ID})
}

func stateInputID(id generation.Optional[string]) error {
	if value, ok := id.Value(); ok && !utf8.ValidString(value) {
		return invalid("id", "must be valid UTF-8")
	}
	return nil
}
