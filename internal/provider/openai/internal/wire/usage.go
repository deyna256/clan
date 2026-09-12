package wire

import (
	"bytes"
	"encoding/json"

	"github.com/deyna256/clan/internal/usage"
)

// NormalizeUsage merges observed counters and reports whether any were invalid.
// Invalid fields leave previously accepted observations intact.
func NormalizeUsage(raw json.RawMessage, previous usage.State) (usage.State, bool) {
	if absent(raw) {
		return previous, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return previous, true
	}
	var details struct {
		cached, reasoning json.RawMessage
	}
	invalid := false
	for _, group := range []struct {
		name, field string
		target      *json.RawMessage
	}{
		{"input_tokens_details", "cached_tokens", &details.cached},
		{"output_tokens_details", "reasoning_tokens", &details.reasoning},
	} {
		if absent(fields[group.name]) {
			continue
		}
		var values map[string]json.RawMessage
		if json.Unmarshal(fields[group.name], &values) != nil {
			invalid = true
			continue
		}
		*group.target = values[group.field]
	}
	state := previous
	for i, value := range []json.RawMessage{fields["input_tokens"], fields["output_tokens"], details.cached, details.reasoning, fields["total_tokens"]} {
		if absent(value) {
			continue
		}
		var tokens int64
		if json.Unmarshal(value, &tokens) != nil || tokens < 0 {
			invalid = true
			continue
		}
		candidate := state.Usage
		counters := []*usage.Counter{&candidate.Input, &candidate.Output, &candidate.CacheRead, &candidate.Reasoning, &candidate.Total}
		*counters[i] = usage.Counter{Tokens: tokens, Known: true}
		next, _, err := usage.Advance(state, candidate)
		if err != nil && i != 4 {
			candidate.Total = usage.Counter{}
			next, _, err = usage.Advance(state, candidate)
		}
		if err != nil {
			invalid = true
			continue
		}
		state = next
	}
	return state, invalid
}

func absent(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
