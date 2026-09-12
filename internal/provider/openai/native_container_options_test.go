package openai_test

import (
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeContainerToolOptionsUseSameSchema(t *testing.T) {
	policy := generation.InterpreterNetworkAllowlist{Domains: []string{"example.com"}, Secrets: []generation.InterpreterDomainSecret{{Domain: "example.com", Name: "TOKEN", Value: ""}}}
	skills := []generation.ShellSkill{generation.ShellInlineSkill{Name: "demo", Description: "", Data: "UEs="}}
	const wantPolicy = `{"type":"allowlist","allowed_domains":["example.com"],"domain_secrets":[{"domain":"example.com","name":"TOKEN","value":""}]}`
	for _, tc := range []struct {
		name, path, want string
		tool             generation.Tool
	}{
		{
			name: "code interpreter", path: "tools.0.container.network_policy", want: wantPolicy,
			tool: generation.OpenAICodeInterpreterTool{Container: generation.InterpreterAutoContainer{NetworkPolicy: policy}},
		},
		{
			name: "shell", path: "tools.0.environment",
			want: `{"type":"container_auto","network_policy":` + wantPolicy + `,"skills":[{"type":"inline","name":"demo","description":"","source":{"type":"base64","media_type":"application/zip","data":"UEs="}}]}`,
			tool: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{NetworkPolicy: policy, Skills: skills})},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := generation.Request{Model: "test-model", Tools: []generation.Tool{tc.tool}}

			body, err := wire.EncodeRequest(request, false)

			require.NoError(t, err)
			assertRequestJSON(t, gjson.GetBytes(body, tc.path).Raw, tc.want)
		})
	}
}
