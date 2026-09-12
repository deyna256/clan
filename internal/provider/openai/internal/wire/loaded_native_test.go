package wire_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

func TestLoadedNativeToolsAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want generation.Tool
	}{
		{name: "computer", raw: `{"type":"computer"}`, want: generation.OpenAIComputerTool{}},
		{name: "computer preview", raw: `{"type":"computer_use_preview","display_width":1024,"display_height":768,"environment":"linux"}`, want: generation.OpenAIComputerPreviewTool{DisplayWidth: 1024, DisplayHeight: 768, Environment: "linux"}},
		{name: "local shell", raw: `{"type":"local_shell"}`, want: generation.OpenAILocalShellTool{}},
		{name: "patch null callers", raw: `{"type":"apply_patch","allowed_callers":null}`, want: generation.OpenAIApplyPatchTool{AllowedCallers: generation.Null[[]string]()}},
		{name: "patch empty callers", raw: `{"type":"apply_patch","allowed_callers":[]}`, want: generation.OpenAIApplyPatchTool{AllowedCallers: generation.Some([]string{})}},
		{name: "interpreter reference", raw: `{"type":"code_interpreter","container":"cn_1","allowed_callers":null}`, want: generation.OpenAICodeInterpreterTool{Container: generation.InterpreterContainerID("cn_1"), AllowedCallers: generation.Null[[]string]()}},
		{name: "interpreter empty secret value", raw: `{"type":"code_interpreter","container":{"type":"auto","network_policy":{"type":"allowlist","allowed_domains":[],"domain_secrets":[{"domain":"example.com","name":"token","value":""}]}}}`, want: generation.OpenAICodeInterpreterTool{Container: generation.InterpreterAutoContainer{NetworkPolicy: generation.InterpreterNetworkAllowlist{Domains: []string{}, Secrets: []generation.InterpreterDomainSecret{{Domain: "example.com", Name: "token", Value: ""}}}}}},
		{name: "shell empty secret value", raw: `{"type":"shell","environment":{"type":"container_auto","network_policy":{"type":"allowlist","allowed_domains":[],"domain_secrets":[{"domain":"example.com","name":"token","value":""}]}}}`, want: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{NetworkPolicy: generation.InterpreterNetworkAllowlist{Domains: []string{}, Secrets: []generation.InterpreterDomainSecret{{Domain: "example.com", Name: "token", Value: ""}}}})}},
		{name: "interpreter auto", raw: `{"type":"code_interpreter","container":{"type":"auto","file_ids":[],"memory_limit":null,"network_policy":{"type":"allowlist","allowed_domains":["example.com"],"domain_secrets":[{"domain":"example.com","name":"token","value":"secret"}]}},"allowed_callers":["direct"]}`, want: generation.OpenAICodeInterpreterTool{AllowedCallers: generation.Some([]string{"direct"}), Container: generation.InterpreterAutoContainer{FileIDs: []string{}, MemoryLimit: generation.Null[string](), NetworkPolicy: generation.InterpreterNetworkAllowlist{Domains: []string{"example.com"}, Secrets: []generation.InterpreterDomainSecret{{Domain: "example.com", Name: "token", Value: "secret"}}}}}},
		{name: "image references", raw: `{"type":"image_generation","model":"","input_fidelity":null,"output_compression":0,"partial_images":0,"input_image_mask":{"file_id":"file_1","image_url":"https://example.com/mask.png"}}`, want: generation.OpenAIImageGenerationTool{Model: generation.Some(""), InputFidelity: generation.Null[string](), OutputCompression: generation.Some(int64(0)), PartialImages: generation.Some(int64(0)), Mask: generation.Some(generation.ImageGenerationMask{FileID: generation.Some("file_1"), ImageURL: generation.Some("https://example.com/mask.png")})}},
		{name: "empty mask", raw: `{"type":"image_generation","input_image_mask":{}}`, want: generation.OpenAIImageGenerationTool{Mask: generation.Some(generation.ImageGenerationMask{})}},
		{name: "image enums", raw: `{"type":"image_generation","model":"future-image","action":"edit","background":"transparent","output_format":"webp","quality":"max","size":"1536x864","input_fidelity":"high","moderation":"low"}`, want: generation.OpenAIImageGenerationTool{Model: generation.Some("future-image"), Action: "edit", Background: "transparent", OutputFormat: "webp", Quality: "max", Size: "1536x864", InputFidelity: generation.Some("high"), Moderation: "low"}},
		{name: "web null filters", raw: `{"type":"web_search","external_web_access":false,"filters":null,"user_location":null}`, want: generation.OpenAIWebSearchTool{Version: "web_search", ExternalWebAccess: generation.Some(false), Filters: generation.Null[generation.WebSearchFilters](), Location: generation.Null[generation.SearchLocation]()}},
		{name: "web empty objects", raw: `{"type":"web_search","filters":{},"user_location":{}}`, want: generation.OpenAIWebSearchTool{Version: "web_search", Filters: generation.Some(generation.WebSearchFilters{}), Location: generation.Some(generation.SearchLocation{})}},
		{name: "web domains null", raw: `{"type":"web_search_2025_08_26","filters":{"allowed_domains":null},"search_context_size":"low"}`, want: generation.OpenAIWebSearchTool{Version: "web_search_2025_08_26", Filters: generation.Some(generation.WebSearchFilters{AllowedDomains: generation.Null[[]string]()}), ContextSize: "low"}},
		{name: "web preview", raw: `{"type":"web_search_preview","search_content_types":["text","image"],"user_location":{"type":"approximate","city":null,"country":"GB"}}`, want: generation.OpenAIWebSearchTool{Version: "web_search_preview", SearchContentTypes: []string{"text", "image"}, Location: generation.Some(generation.SearchLocation{Type: generation.Some("approximate"), City: generation.Null[string](), Country: generation.Some("GB")})}},
		{name: "web preview version", raw: `{"type":"web_search_preview_2025_03_11","search_content_types":[]}`, want: generation.OpenAIWebSearchTool{Version: "web_search_preview_2025_03_11", SearchContentTypes: []string{}}},
		{name: "file null filter", raw: `{"type":"file_search","vector_store_ids":["vs_1"],"filters":null,"ranking_options":{}}`, want: generation.OpenAIFileSearchTool{VectorStoreIDs: []string{"vs_1"}, Filter: generation.Null[generation.SearchFilter](), Ranking: generation.Some(generation.SearchRanking{})}},
		{name: "file ranking and precise filter", raw: `{"type":"file_search","vector_store_ids":["vs_1"],"max_num_results":50,"filters":{"type":"and","filters":[{"type":"eq","key":"revision","value":9007199254740993}]},"ranking_options":{"ranker":"auto","score_threshold":0,"hybrid_search":{"embedding_weight":0,"text_weight":1}}}`, want: generation.OpenAIFileSearchTool{VectorStoreIDs: []string{"vs_1"}, MaxResults: generation.Some(int64(50)), Filter: generation.Some[generation.SearchFilter](generation.SearchCompound{Operator: "and", Filters: []generation.SearchFilter{generation.SearchComparison{Operator: "eq", Key: "revision", Value: json.RawMessage(`9007199254740993`)}}}), Ranking: generation.Some(generation.SearchRanking{Ranker: "auto", ScoreThreshold: generation.Some(0.0), Hybrid: generation.Some(generation.HybridSearch{TextWeight: 1})})}},
		{name: "shell null environment", raw: `{"type":"shell","allowed_callers":null,"environment":null}`, want: generation.OpenAIShellTool{AllowedCallers: generation.Null[[]string](), Environment: generation.Null[generation.ShellEnvironment]()}},
		{name: "shell reference", raw: `{"type":"shell","environment":{"type":"container_reference","container_id":"cn_1"}}`, want: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellContainerReference{ContainerID: "cn_1"})}},
		{name: "shell local skill", raw: `{"type":"shell","environment":{"type":"local","skills":[{"name":"test","description":"","path":"/skills/test"}]}}`, want: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellLocalEnvironment{Skills: []generation.ShellLocalSkill{{Name: "test", Path: "/skills/test"}}})}},
		{name: "shell auto skills", raw: `{"type":"shell","environment":{"type":"container_auto","file_ids":["file_1"],"memory_limit":"4g","network_policy":{"type":"disabled"},"skills":[{"type":"skill_reference","skill_id":"skill_1","version":"latest"},{"type":"inline","name":"test","description":"","source":{"type":"base64","media_type":"application/zip","data":"UEs="}}]}}`, want: generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{FileIDs: []string{"file_1"}, MemoryLimit: generation.Some("4g"), NetworkPolicy: generation.InterpreterNetworkDisabled{}, Skills: []generation.ShellSkill{generation.ShellSkillReference{SkillID: "skill_1", Version: generation.Some("latest")}, generation.ShellInlineSkill{Name: "test", Data: "UEs="}}})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"additional_tools","id":"extra_1","role":"developer","tools":[` + tc.raw + `]}]}`))
			if err != nil {
				t.Fatal(err)
			}

			result, err := envelope.Result(nil)

			if err != nil {
				t.Fatal(err)
			}
			loaded := result.Response.Output[0].(generation.OpenAIAdditionalTools)
			if !reflect.DeepEqual(loaded.Tools, []generation.Tool{tc.want}) {
				t.Fatalf("tools = %#v; want %#v", loaded.Tools, tc.want)
			}

			body, err := wire.EncodeRequest(requestWith(loaded), false)

			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, body, `{"model":"test-model","stream":false,"input":[{"type":"additional_tools","id":"extra_1","role":"developer","tools":[`+tc.raw+`]}]}`)
			if strings.Contains(tc.raw, "9007199254740993") && !strings.Contains(string(body), "9007199254740993") {
				t.Fatal("filter lost integer precision")
			}
		})
	}
}

func TestRejectMalformedLoadedNativeTools(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{name: "computer missing display", raw: `{"type":"computer_use_preview","environment":"linux"}`},
		{name: "patch null caller", raw: `{"type":"apply_patch","allowed_callers":[null]}`},
		{name: "interpreter missing container", raw: `{"type":"code_interpreter"}`},
		{name: "interpreter null files", raw: `{"type":"code_interpreter","container":{"type":"auto","file_ids":null}}`},
		{name: "interpreter missing allowlist", raw: `{"type":"code_interpreter","container":{"type":"auto","network_policy":{"type":"allowlist"}}}`},
		{name: "interpreter missing secret value", raw: `{"type":"code_interpreter","container":{"type":"auto","network_policy":{"type":"allowlist","allowed_domains":[],"domain_secrets":[{"domain":"example.com","name":"token"}]}}}`},
		{name: "interpreter null secret value", raw: `{"type":"code_interpreter","container":{"type":"auto","network_policy":{"type":"allowlist","allowed_domains":[],"domain_secrets":[{"domain":"example.com","name":"token","value":null}]}}}`},
		{name: "shell missing secret value", raw: `{"type":"shell","environment":{"type":"container_auto","network_policy":{"type":"allowlist","allowed_domains":[],"domain_secrets":[{"domain":"example.com","name":"token"}]}}}`},
		{name: "shell null secret value", raw: `{"type":"shell","environment":{"type":"container_auto","network_policy":{"type":"allowlist","allowed_domains":[],"domain_secrets":[{"domain":"example.com","name":"token","value":null}]}}}`},
		{name: "interpreter null secret", raw: `{"type":"code_interpreter","container":{"type":"auto","network_policy":{"type":"allowlist","allowed_domains":[],"domain_secrets":[null]}}}`},
		{name: "image null model", raw: `{"type":"image_generation","model":null}`},
		{name: "image null action", raw: `{"type":"image_generation","action":null}`},
		{name: "image null number", raw: `{"type":"image_generation","partial_images":null}`},
		{name: "image null mask", raw: `{"type":"image_generation","input_image_mask":null}`},
		{name: "image null ref", raw: `{"type":"image_generation","input_image_mask":{"image_url":null}}`},
		{name: "web null context", raw: `{"type":"web_search","search_context_size":null}`},
		{name: "web null domain entry", raw: `{"type":"web_search","filters":{"allowed_domains":[null]}}`},
		{name: "web wrong location", raw: `{"type":"web_search","user_location":{"type":"private"}}`},
		{name: "preview missing location type", raw: `{"type":"web_search_preview","user_location":{}}`},
		{name: "preview forbidden filters", raw: `{"type":"web_search_preview","filters":{}}`},
		{name: "normal search content types", raw: `{"type":"web_search","search_content_types":[]}`},
		{name: "file null stores", raw: `{"type":"file_search","vector_store_ids":null}`},
		{name: "file null max", raw: `{"type":"file_search","vector_store_ids":["vs"],"max_num_results":null}`},
		{name: "file null ranking", raw: `{"type":"file_search","vector_store_ids":["vs"],"ranking_options":null}`},
		{name: "file missing hybrid weight", raw: `{"type":"file_search","vector_store_ids":["vs"],"ranking_options":{"hybrid_search":{"embedding_weight":0}}}`},
		{name: "file null filter child", raw: `{"type":"file_search","vector_store_ids":["vs"],"filters":{"type":"and","filters":[null]}}`},
		{name: "shell null skills", raw: `{"type":"shell","environment":{"type":"local","skills":null}}`},
		{name: "shell missing local description", raw: `{"type":"shell","environment":{"type":"local","skills":[{"name":"test","path":"/skills/test"}]}}`},
		{name: "shell null network", raw: `{"type":"shell","environment":{"type":"container_auto","network_policy":null}}`},
		{name: "shell invalid source", raw: `{"type":"shell","environment":{"type":"container_auto","skills":[{"type":"inline","name":"test","description":"","source":{"type":"file","media_type":"application/zip","data":"private"}}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"additional_tools","id":"extra_1","role":"developer","tools":[` + tc.raw + `]}]}`))
			if err != nil {
				t.Fatal(err)
			}

			_, err = envelope.Result(nil)

			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("Result error = %v; want protocol error", err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("error leaked native tool data")
			}
		})
	}
}

func TestLoadedFileFilterDepthBound(t *testing.T) {
	filter := strings.Repeat(`{"type":"and","filters":[`, 64) + `{"type":"eq","key":"revision","value":1}` + strings.Repeat(`]}`, 64)
	envelope, err := wire.DecodeEnvelope([]byte(`{"id":"resp_1","model":"test-model","status":"completed","output":[{"type":"additional_tools","id":"extra_1","role":"developer","tools":[{"type":"file_search","vector_store_ids":["vs_1"],"filters":` + filter + `}]}]}`))
	if err != nil {
		t.Fatal(err)
	}

	_, err = envelope.Result(nil)

	var failure *generation.Failure
	if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
		t.Fatalf("Result error = %v; want protocol error", err)
	}
}
