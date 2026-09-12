package wire

import (
	"encoding/json"

	"github.com/deyna256/clan/internal/generation"
)

func EncodeWebSocketCreate(request generation.Request, lane string, generate generation.Optional[bool]) ([]byte, error) {
	if err := ValidateWebSocketLane(lane); err != nil {
		return nil, err
	}
	if generate.IsNull() {
		return nil, invalid("generate", "must be a boolean")
	}
	body, err := encodeCreateRequest(request, true)
	if err != nil {
		return nil, err
	}
	// Explicit fields shadow the HTTP transport fields in the embedded body.
	return json.Marshal(struct {
		createRequest
		Type       string                    `json:"type"`
		Lane       string                    `json:"stream_id,omitempty"`
		Generate   generation.Optional[bool] `json:"generate,omitzero"`
		Stream     *bool                     `json:"stream,omitempty"`
		Background *bool                     `json:"background,omitempty"`
	}{createRequest: body, Type: "response.create", Lane: lane, Generate: generate})
}

// ValidateWebSocketLane accepts the empty default lane and named ASCII lanes.
func ValidateWebSocketLane(lane string) error {
	if len(lane) > 256 {
		return invalid("stream_id", "must have at most 256 characters")
	}
	for _, char := range lane {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return invalid("stream_id", "expected letters, numbers, underscore, hyphen, or period")
	}
	return nil
}
