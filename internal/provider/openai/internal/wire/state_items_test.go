package wire_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodeConversationStateItems(t *testing.T) {
	request := requestWith(
		generation.OpenAIConfigurationUpdate{},
		generation.OpenAIConfigurationUpdate{ID: generation.Null[string](), Reasoning: generation.Some(generation.ConfigurationReasoning{})},
		generation.OpenAIConfigurationUpdate{ID: generation.Some("cfg_1"), Reasoning: generation.Some(generation.ConfigurationReasoning{Effort: generation.Null[string]()})},
		generation.OpenAIConfigurationUpdate{Reasoning: generation.Some(generation.ConfigurationReasoning{Effort: generation.Some("max")})},
		generation.OpenAICompaction{EncryptedContent: generation.Some("")},
		generation.OpenAICompaction{ID: generation.Null[string](), EncryptedContent: generation.Some("opaque\x00state"), CreatedBy: generation.Some("creator_1")},
		generation.OpenAIItemReference{ID: "msg_1"},
		generation.OpenAICompactionTrigger{ID: generation.Null[string]()},
	)

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[
		{"type":"configuration_update"},
		{"type":"configuration_update","id":null,"reasoning":{}},
		{"type":"configuration_update","id":"cfg_1","reasoning":{"effort":null}},
		{"type":"configuration_update","reasoning":{"effort":"max"}},
		{"type":"compaction","encrypted_content":""},
		{"type":"compaction","id":null,"encrypted_content":"opaque\u0000state"},
		{"type":"item_reference","id":"msg_1"},
		{"type":"compaction_trigger","id":null}
	]}`)
}

func TestCompactionResultReplay(t *testing.T) {
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"compaction","id":"cmp_1","encrypted_content":"opaque","created_by":"creator_1"}]}`))
	require.NoError(t, err)
	want := generation.OpenAICompaction{ID: generation.Some("cmp_1"), EncryptedContent: generation.Some("opaque"), CreatedBy: generation.Some("creator_1")}

	result, err := envelope.Result(nil)

	require.NoError(t, err)
	require.Equal(t, []generation.Item{want}, result.Response.Output)

	body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"compaction","id":"cmp_1","encrypted_content":"opaque"}]}`)
}

func TestDecodeStoredConfigurationUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		reasoning    generation.Optional[generation.ConfigurationReasoning]
	}{
		{name: "omitted"},
		{name: "empty", fields: `,"reasoning":{}`, reasoning: generation.Some(generation.ConfigurationReasoning{})},
		{name: "reset", fields: `,"reasoning":{"effort":null}`, reasoning: generation.Some(generation.ConfigurationReasoning{Effort: generation.Null[string]()})},
		{name: "effort", fields: `,"reasoning":{"effort":"minimal"}`, reasoning: generation.Some(generation.ConfigurationReasoning{Effort: generation.Some("minimal")})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"type":"configuration_update","id":"cfg_1"` + tc.fields + `}`)
			want := generation.OpenAIConfigurationUpdate{ID: generation.Some("cfg_1"), Reasoning: tc.reasoning}

			item, err := wire.DecodeConfigurationUpdate(raw)

			require.NoError(t, err)
			require.Equal(t, want, item)
		})
	}
}

func TestRejectInvalidStateInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []generation.Item
	}{
		{name: "null reasoning", items: []generation.Item{generation.OpenAIConfigurationUpdate{Reasoning: generation.Null[generation.ConfigurationReasoning]()}}},
		{name: "invalid effort", items: []generation.Item{generation.OpenAIConfigurationUpdate{Reasoning: generation.Some(generation.ConfigurationReasoning{Effort: generation.Some("private")})}}},
		{name: "invalid ID", items: []generation.Item{generation.OpenAIConfigurationUpdate{ID: generation.Some("private\xff")}}},
		{name: "missing encrypted content", items: []generation.Item{generation.OpenAICompaction{}}},
		{name: "null encrypted content", items: []generation.Item{generation.OpenAICompaction{EncryptedContent: generation.Null[string]()}}},
		{name: "encrypted content UTF8", items: []generation.Item{generation.OpenAICompaction{EncryptedContent: generation.Some("private\xff")}}},
		{name: "missing reference", items: []generation.Item{generation.OpenAIItemReference{}}},
		{name: "trigger ID", items: []generation.Item{generation.OpenAICompactionTrigger{ID: generation.Some("private\xff")}}},
		{name: "trigger is not final", items: []generation.Item{generation.OpenAICompactionTrigger{}, generation.OpenAIItemReference{ID: "msg_1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith(tc.items...)

			body, err := wire.EncodeRequest(request, false)

			assertInputError(t, body, err)
		})
	}
}

func TestRejectMalformedConfigurationUpdates(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{name: "missing ID", raw: `{"type":"configuration_update"}`},
		{name: "null ID", raw: `{"type":"configuration_update","id":null}`},
		{name: "null reasoning", raw: `{"type":"configuration_update","id":"c","reasoning":null}`},
		{name: "invalid effort", raw: `{"type":"configuration_update","id":"c","reasoning":{"effort":"invalid"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wire.DecodeConfigurationUpdate(json.RawMessage(tc.raw))

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("decode error = %v; want protocol error", err)
			}
		})
	}
}

func TestRejectMalformedCompactionItems(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{name: "null content", raw: `{"type":"compaction","id":"c","encrypted_content":null}`},
		{name: "missing ID", raw: `{"type":"compaction","encrypted_content":"opaque"}`},
		{name: "null creator", raw: `{"type":"compaction","id":"c","encrypted_content":"opaque","created_by":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item, err := wire.DecodeItem([]byte(tc.raw), true, nil)

			var failure *generation.Failure
			if item != nil || !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("item = %#v, %v; want no item and protocol error", item, err)
			}
		})
	}
}
