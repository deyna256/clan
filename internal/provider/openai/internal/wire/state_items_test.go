package wire_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
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

	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	want := generation.OpenAICompaction{ID: generation.Some("cmp_1"), EncryptedContent: generation.Some("opaque"), CreatedBy: generation.Some("creator_1")}

	result, err := envelope.Result(nil)

	if err != nil || !reflect.DeepEqual(result.Response.Output, []generation.Item{want}) {
		t.Fatalf("Result = %#v, %v; want %#v", result.Response.Output, err, want)
	}

	body, err := wire.EncodeRequest(requestWith(result.Response.Output...), false)

	if err != nil {
		t.Fatal(err)
	}
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

			if err != nil || !reflect.DeepEqual(item, want) {
				t.Fatalf("configuration = %#v, %v; want %#v", item, err, want)
			}
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

			assertCustomInputError(t, body, err)
		})
	}
}

func TestRejectMalformedStateItems(t *testing.T) {
	for _, tc := range []struct {
		name, raw     string
		configuration bool
	}{
		{name: "configuration missing ID", raw: `{"type":"configuration_update"}`, configuration: true},
		{name: "configuration null ID", raw: `{"type":"configuration_update","id":null}`, configuration: true},
		{name: "configuration null reasoning", raw: `{"type":"configuration_update","id":"c","reasoning":null}`, configuration: true},
		{name: "configuration effort", raw: `{"type":"configuration_update","id":"c","reasoning":{"effort":"invalid"}}`, configuration: true},
		{name: "compaction null content", raw: `{"type":"compaction","id":"c","encrypted_content":null}`},
		{name: "compaction missing ID", raw: `{"type":"compaction","encrypted_content":"opaque"}`},
		{name: "compaction null creator", raw: `{"type":"compaction","id":"c","encrypted_content":"opaque","created_by":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error

			if tc.configuration {
				_, err = wire.DecodeConfigurationUpdate(json.RawMessage(tc.raw))
			} else {
				_, err = wire.DecodeItem([]byte(tc.raw), true, nil)
			}

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("decode error = %v; want protocol error", err)
			}
		})
	}
}
