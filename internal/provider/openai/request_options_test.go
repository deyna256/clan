package openai_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
)

func TestRequestOptionsFailBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options generation.OpenAIOptions
		field   string
	}{
		{name: "invalid prompt image", options: generation.OpenAIOptions{Prompt: generation.Some(generation.OpenAIPrompt{ID: "p", Variables: generation.Some(map[string]generation.PromptVariable{"image": generation.ImageURL{URL: "file:///secret"}})})}, field: "prompt.variables.image_url"},
		{name: "nonstream options", options: generation.OpenAIOptions{StreamOptions: generation.Some(generation.OpenAIStreamOptions{IncludeObfuscation: generation.Some(false)})}, field: "stream_options"},
		{name: "conflicting conversation", options: generation.OpenAIOptions{Conversation: generation.Some("conv_1"), PreviousResponseID: generation.Some("resp_1")}, field: "conversation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := clientWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected dispatch")
			}))
			request := textRequest()
			request.OpenAI = tc.options

			_, err := client.Generate(t.Context(), testAttempt(), request)

			var input *openai.InputError
			var failure *generation.Failure
			if calls != 0 || !errors.As(err, &input) || input.Field != tc.field || !errors.As(err, &failure) || failure.Kind != generation.InvalidRequest || failure.OutcomeUnknown {
				t.Fatalf("calls = %d; error = %v; want local invalid request field %q", calls, err, tc.field)
			}
		})
	}
}
