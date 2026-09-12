package wire_test

import (
	"errors"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
)

func TestAllowedToolsChoice(t *testing.T) {
	for _, mode := range []generation.ToolMode{generation.ToolAuto, generation.ToolRequired} {
		t.Run(string(mode), func(t *testing.T) {
			request := requestWith()
			request.ToolChoice = generation.OpenAIAllowedToolsChoice{Mode: mode, Tools: []generation.ToolChoice{
				generation.NamedTool{Name: "get_weather"}, generation.NamedCustomTool{Name: "execute"},
				generation.OpenAIMCPChoice{ServerLabel: "deepwiki"}, generation.OpenAIImageGenerationChoice{},
				generation.OpenAIComputerChoice("computer_use"),
			}}

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tool_choice":{"type":"allowed_tools","mode":"`+string(mode)+`","tools":[{"type":"function","name":"get_weather"},{"type":"custom","name":"execute"},{"type":"mcp","server_label":"deepwiki"},{"type":"image_generation"},{"type":"computer_use"}]}}`)
		})
	}
}

func TestAllowedToolsEmptySelection(t *testing.T) {
	request := requestWith()
	request.ToolChoice = generation.OpenAIAllowedToolsChoice{Mode: generation.ToolAuto}

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[]}}`)
}

func TestAllowedToolsRejectsInvalidSelections(t *testing.T) {
	for _, tc := range []struct {
		name   string
		choice generation.OpenAIAllowedToolsChoice
	}{
		{name: "missing mode"},
		{name: "none mode", choice: generation.OpenAIAllowedToolsChoice{Mode: generation.ToolNone}},
		{name: "nil entry", choice: generation.OpenAIAllowedToolsChoice{Mode: generation.ToolAuto, Tools: []generation.ToolChoice{nil}}},
		{name: "mode entry", choice: generation.OpenAIAllowedToolsChoice{Mode: generation.ToolAuto, Tools: []generation.ToolChoice{generation.ToolRequired}}},
		{name: "recursive selection", choice: generation.OpenAIAllowedToolsChoice{Mode: generation.ToolAuto, Tools: []generation.ToolChoice{generation.OpenAIAllowedToolsChoice{Mode: generation.ToolAuto}}}},
		{name: "invalid individual selector", choice: generation.OpenAIAllowedToolsChoice{Mode: generation.ToolAuto, Tools: []generation.ToolChoice{generation.NamedTool{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := requestWith()
			request.ToolChoice = tc.choice

			body, err := wire.EncodeRequest(request, false)

			var failure *generation.Failure
			if len(body) != 0 || !errors.As(err, &failure) || failure.Kind != generation.InvalidRequest {
				t.Fatalf("body=%s error=%v", body, err)
			}
		})
	}
}

func TestComputerUseChoice(t *testing.T) {
	request := requestWith()
	request.ToolChoice = generation.OpenAIComputerChoice("computer_use")

	body, err := wire.EncodeRequest(request, false)

	require.NoError(t, err)
	assertJSON(t, body, `{"model":"test-model","input":[],"stream":false,"tool_choice":{"type":"computer_use"}}`)
}
