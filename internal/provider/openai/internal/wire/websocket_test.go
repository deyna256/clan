package wire_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestEncodeWebSocketCreate(t *testing.T) {
	request := generation.Request{Model: "test-model", OpenAI: generation.OpenAIOptions{
		Background: generation.Some(false), PreviousResponseID: generation.Some("resp_previous"), Store: generation.Some(false),
	}}

	body, err := wire.EncodeWebSocketCreate(request, "draft_1.a-b", generation.Some(false))

	require.NoError(t, err)
	assertJSON(t, body, `{"type":"response.create","model":"test-model","input":[],"stream_id":"draft_1.a-b","generate":false,"previous_response_id":"resp_previous","store":false}`)
}

func TestEncodeWebSocketDefaultLane(t *testing.T) {
	request := generation.Request{Model: "test-model", OpenAI: generation.OpenAIOptions{Background: generation.Null[bool]()}}

	body, err := wire.EncodeWebSocketCreate(request, "", generation.Optional[bool]{})

	require.NoError(t, err)
	assertJSON(t, body, `{"type":"response.create","model":"test-model","input":[]}`)
}

func TestEncodeWebSocketRejectsInvalidOptions(t *testing.T) {
	for _, tc := range []struct {
		name, lane, field string
		generate          generation.Optional[bool]
		background        generation.Optional[bool]
	}{
		{name: "space", lane: "private lane", field: "stream_id"},
		{name: "unicode", lane: "приватный", field: "stream_id"},
		{name: "long", lane: strings.Repeat("a", 257), field: "stream_id"},
		{name: "generate null", generate: generation.Null[bool](), field: "generate"},
		{name: "background true", background: generation.Some(true), field: "background"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := generation.Request{Model: "test-model", OpenAI: generation.OpenAIOptions{Background: tc.background}}

			body, err := wire.EncodeWebSocketCreate(request, tc.lane, tc.generate)

			var input *wire.InputError
			if body != nil || !errors.As(err, &input) || input.Field != tc.field {
				t.Fatalf("encode = %q, %v; want validation at %s", body, err, tc.field)
			}
		})
	}
}
