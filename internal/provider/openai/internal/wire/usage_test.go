package wire_test

import (
	"encoding/json"
	"testing"

	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/deyna256/clan/internal/usage"
)

func TestNormalizeUsagePreservesIntegerCountersAndIndependentDetails(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      usage.State
		invalid   bool
	}{
		{name: "exact large integer", raw: `{"input_tokens":9007199254740993}`, want: usage.State{Usage: usage.Snapshot{Input: usage.Counter{Tokens: 9007199254740993, Known: true}}, Charged: 9007199254740993}},
		{name: "fraction", raw: `{"input_tokens":1.5,"output_tokens":0}`, want: usage.State{Usage: usage.Snapshot{Output: usage.Counter{Known: true}}}, invalid: true},
		{name: "exponent", raw: `{"input_tokens":1e2}`, invalid: true},
		{name: "overflow", raw: `{"input_tokens":9223372036854775808}`, invalid: true},
		{name: "numeric string", raw: `{"input_tokens":"12"}`, invalid: true},
		{name: "malformed details retain sibling", raw: `{"input_tokens_details":[],"output_tokens_details":{"reasoning_tokens":3}}`, want: usage.State{Usage: usage.Snapshot{Reasoning: usage.Counter{Tokens: 3, Known: true}}, Charged: 3}, invalid: true},
		{name: "null counters and details", raw: `{"input_tokens":null,"input_tokens_details":null}`},
		{name: "non-object usage", raw: `[]`, invalid: true},
		{name: "trailing document", raw: `{"input_tokens":12}{}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, invalid := wire.NormalizeUsage(json.RawMessage(tc.raw), usage.State{})

			if got != tc.want || invalid != tc.invalid {
				t.Fatalf("usage = %#v, invalid=%v; want %#v, invalid=%v", got, invalid, tc.want, tc.invalid)
			}
		})
	}
}

func TestNormalizeUsageRejectsDecreaseAndKeepsAcceptedCounters(t *testing.T) {
	previous := usage.State{Usage: usage.Snapshot{Input: usage.Counter{Tokens: 12, Known: true}, Total: usage.Counter{Tokens: 12, Known: true}}, Charged: 12}

	got, invalid := wire.NormalizeUsage(json.RawMessage(`{"input_tokens":10,"output_tokens":3}`), previous)

	want := usage.State{Usage: usage.Snapshot{Input: usage.Counter{Tokens: 12, Known: true}, Output: usage.Counter{Tokens: 3, Known: true}}, Charged: 15}
	if got != want || !invalid {
		t.Fatalf("usage = %#v, invalid=%v; want %#v, invalid=true", got, invalid, want)
	}
}
