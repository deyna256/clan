package gateway

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/deyna256/clan/internal/codex"
	"github.com/tmaxmax/go-sse"
)

func prepareEvent(raw json.RawMessage, previous int64) (json.RawMessage, string, int64, error) {
	var meta struct {
		Type     string          `json:"type"`
		Sequence json.RawMessage `json:"sequence_number"`
	}
	invalid := &codex.Failure{Category: codex.InvalidResponse}
	if json.Unmarshal(raw, &meta) != nil || meta.Type == "" {
		return nil, "", previous, invalid
	}
	if _, err := sse.NewType(meta.Type); err != nil {
		return nil, "", previous, invalid
	}
	errorEvent := meta.Type == "error" || meta.Type == "response.failed"
	sequence, err := strconv.ParseInt(string(meta.Sequence), 10, 64)
	validSequence := err == nil && sequence >= 0
	if !validSequence {
		if len(meta.Sequence) != 0 && !errorEvent {
			return nil, "", previous, invalid
		}
		if previous == math.MaxInt64 {
			return nil, "", previous, invalid
		}
		sequence = previous + 1
	}
	if !errorEvent {
		return raw, meta.Type, max(previous, sequence), nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, "", previous, invalid
	}
	if !validSequence {
		fields["sequence_number"] = json.RawMessage(strconv.FormatInt(sequence, 10))
	}
	if meta.Type == "response.failed" {
		response, err := normalizeFailedResponse(fields["response"])
		if err != nil {
			return nil, "", previous, invalid
		}
		fields["response"] = response
	} else {
		failure, err := wireFailure(raw)
		if err != nil {
			return nil, "", previous, invalid
		}
		fields["code"], _ = json.Marshal(failure.Code)
		fields["message"], _ = json.Marshal(failure.Message)
		fields["param"], _ = json.Marshal(failure.Param)
	}
	raw, err = json.Marshal(fields)
	return raw, meta.Type, max(previous, sequence), err
}

func normalizeFailedResponse(raw json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, &codex.Failure{Category: codex.InvalidResponse}
	}
	failure, err := wireFailure(fields["error"])
	if err != nil {
		return nil, err
	}
	fields["error"], _ = json.Marshal(failure)
	return json.Marshal(fields)
}

func wireFailure(raw json.RawMessage) (clientError, error) {
	var failure struct {
		Code codex.Category `json:"code"`
	}
	if err := json.Unmarshal(raw, &failure); err != nil {
		return clientError{}, &codex.Failure{Category: codex.InvalidResponse}
	}
	return classify(&codex.Failure{Category: failure.Code}), nil
}
