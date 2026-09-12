package wire

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/deyna256/clan/internal/usage"
	"github.com/tidwall/gjson"
)

// NormalizeUsage merges observed counters and reports whether any were invalid.
// Invalid fields leave previously accepted observations intact.
func NormalizeUsage(raw json.RawMessage, previous usage.State) (usage.State, bool) {
	if absent(raw) {
		return previous, false
	}
	fields := gjson.ParseBytes(raw)
	if !gjson.ValidBytes(raw) || !fields.IsObject() {
		return previous, true
	}
	invalid := false
	for _, name := range []string{"input_tokens_details", "output_tokens_details"} {
		group := fields.Get(name)
		if group.Exists() && group.Type != gjson.Null && !group.IsObject() {
			invalid = true
		}
	}
	state := previous
	for i, path := range []string{"input_tokens", "output_tokens", "input_tokens_details.cached_tokens", "output_tokens_details.reasoning_tokens", "total_tokens"} {
		value := fields.Get(path)
		if !value.Exists() || value.Type == gjson.Null {
			continue
		}
		tokens, err := strconv.ParseInt(value.Raw, 10, 64)
		if value.Type != gjson.Number || err != nil || tokens < 0 {
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
