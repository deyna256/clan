package openai_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

const containerJSON = `{"id":"cntr_1","object":"container","created_at":0,"name":"work","status":"active"}`
const containerFileJSON = `{"id":"cfile_1","object":"container.file","bytes":0,"container_id":"cntr_1","created_at":0,"path":"/mnt/data/input.txt","source":"user"}`

func TestContainerAndToolOptionsUseSameSchema(t *testing.T) {
	policy := generation.InterpreterNetworkAllowlist{Domains: []string{"example.com"}, Secrets: []generation.InterpreterDomainSecret{{Domain: "example.com", Name: "TOKEN", Value: ""}}}
	skills := []generation.ShellSkill{generation.ShellInlineSkill{Name: "demo", Description: "", Data: "UEs="}}
	const wantPolicy = `{"type":"allowlist","allowed_domains":["example.com"],"domain_secrets":[{"domain":"example.com","name":"TOKEN","value":""}]}`
	const wantSkills = `[{"type":"inline","name":"demo","description":"","source":{"type":"base64","media_type":"application/zip","data":"UEs="}}]`
	check := func(t *testing.T, raw []byte, withSkills bool) {
		t.Helper()
		var got, want struct {
			NetworkPolicy json.RawMessage `json:"network_policy"`
			Skills        json.RawMessage `json:"skills"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		expected := `{"network_policy":` + wantPolicy + `}`
		if withSkills {
			expected = `{"network_policy":` + wantPolicy + `,"skills":` + wantSkills + `}`
		}
		if err := json.Unmarshal([]byte(expected), &want); err != nil {
			t.Fatal(err)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, got.NetworkPolicy); err != nil {
			t.Fatal(err)
		}
		if compact.String() != string(want.NetworkPolicy) || string(got.Skills) != string(want.Skills) {
			t.Fatalf("options=%s; want %s", raw, expected)
		}
	}
	client := clientWithTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		check(t, raw, true)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(containerJSON))}, nil
	}))

	_, err := client.CreateContainer(t.Context(), testAttempt(), openai.ContainerCreate{Name: "work", NetworkPolicy: policy, Skills: skills})

	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []generation.Tool{
		generation.OpenAICodeInterpreterTool{Container: generation.InterpreterAutoContainer{NetworkPolicy: policy}},
		generation.OpenAIShellTool{Environment: generation.Some[generation.ShellEnvironment](generation.ShellAutoContainer{NetworkPolicy: policy, Skills: skills})},
	} {
		body, err := wire.EncodeRequest(generation.Request{Model: "test-model", Tools: []generation.Tool{tool}}, false)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			Tools []struct{ Container, Environment json.RawMessage }
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request.Tools[0].Container != nil {
			check(t, request.Tools[0].Container, false)
		} else {
			check(t, request.Tools[0].Environment, true)
		}
	}
}

func TestCreateContainerPreservesNativeOptions(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/containers" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		var got, want any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Error(err)
		}
		_ = json.Unmarshal([]byte(`{"name":"work","expires_after":{"anchor":"last_active_at","minutes":20},"file_ids":[],"memory_limit":"4g","network_policy":{"type":"allowlist","allowed_domains":["example.com"],"domain_secrets":[{"domain":"example.com","name":"API_TOKEN","value":"private-token"}]},"skills":[{"type":"skill_reference","skill_id":"skill_1","version":"latest"},{"type":"inline","name":"demo","description":"","source":{"type":"base64","media_type":"application/zip","data":"UEs="}}]}`), &want)
		if !reflect.DeepEqual(got, want) {
			t.Error("container request does not match native wire options")
		}
		_, _ = io.WriteString(w, strings.TrimSuffix(containerJSON, "}")+`,"memory_limit":"4g","expires_after":{},"network_policy":{"type":"allowlist"},"last_active_at":0}`)
	}, io.Discard)
	container, err := client.CreateContainer(t.Context(), testAttempt(), openai.ContainerCreate{
		Name: "work", ExpiresAfter: generation.Some(openai.ContainerExpiration{Anchor: "last_active_at", Minutes: 20}),
		FileIDs: []string{}, MemoryLimit: generation.Some("4g"),
		NetworkPolicy: generation.InterpreterNetworkAllowlist{Domains: []string{"example.com"}, Secrets: []generation.InterpreterDomainSecret{{Domain: "example.com", Name: "API_TOKEN", Value: "private-token"}}},
		Skills:        []generation.ShellSkill{generation.ShellSkillReference{SkillID: "skill_1", Version: generation.Some("latest")}, generation.ShellInlineSkill{Name: "demo", Data: "UEs="}},
	})
	if err != nil || container.ID != "cntr_1" || container.MemoryLimit != generation.Some("4g") || container.LastActiveAt != generation.Some(int64(0)) {
		t.Fatalf("container = %#v, %v", container, err)
	}
	if expiry, ok := container.ExpiresAfter.Value(); !ok || !expiry.Anchor.IsZero() || !expiry.Minutes.IsZero() {
		t.Fatalf("expiry = %#v", container.ExpiresAfter)
	}
}

func TestContainerFileUploadAndAttachAreDistinct(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/containers/cntr_1/files" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Content-Type") == "application/json" {
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"file_id":"file_1"}` {
				t.Errorf("attach body = %s", body)
			}
		} else {
			parts, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			part, err := parts.NextPart()
			if err != nil {
				t.Error(err)
				return
			}
			body, _ := io.ReadAll(part)
			if part.FormName() != "file" || part.FileName() != "input.txt" || string(body) != "hello" {
				t.Errorf("upload = %s %s %s", part.FormName(), part.FileName(), body)
			}
			if _, err := parts.NextPart(); !errors.Is(err, io.EOF) {
				t.Errorf("extra multipart field: %v", err)
			}
		}
		_, _ = io.WriteString(w, containerFileJSON)
	}, io.Discard)
	upload, err := client.UploadContainerFile(t.Context(), testAttempt(), "cntr_1", "input.txt", io.NopCloser(strings.NewReader("hello")))
	if err != nil || upload.ID != "cfile_1" {
		t.Fatalf("upload = %#v, %v", upload, err)
	}
	attached, err := client.AttachContainerFile(t.Context(), testAttempt(), "cntr_1", "file_1")
	if err != nil || attached.ID != "cfile_1" {
		t.Fatalf("attach = %#v, %v", attached, err)
	}
}

func TestContainerFilesPaginationUsesProviderCursor(t *testing.T) {
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		want := url.Values{"after": {"cursor +&?/"}, "limit": {"2"}, "order": {"asc"}}
		if r.Method != http.MethodGet || r.URL.Path != "/containers/cntr_1/files" || !reflect.DeepEqual(r.URL.Query(), want) {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[`+containerFileJSON+`],"first_id":"cfile_1","last_id":"cfile_1","has_more":true}`)
	}, io.Discard)
	page, err := client.ListContainerFiles(t.Context(), testAttempt(), "cntr_1", openai.ContainerFileListParams{After: generation.Some("cursor +&?/"), Limit: generation.Some(int64(2)), Order: generation.Some("asc")})
	if err != nil || calls != 1 || !page.HasMore || page.LastID != "cfile_1" || len(page.Data) != 1 {
		t.Fatalf("page = %#v, %v, calls=%d", page, err, calls)
	}
	empty := testClient(t, staticJSON(`{"object":"list","data":[],"first_id":"","last_id":"","has_more":false}`), io.Discard)
	page, err = empty.ListContainerFiles(t.Context(), testAttempt(), "cntr_1", openai.ContainerFileListParams{})
	if err != nil || page.HasMore || len(page.Data) != 0 {
		t.Fatalf("empty page = %#v, %v", page, err)
	}
}

func TestContainerRetrieveDeleteAndContentPaths(t *testing.T) {
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.Method + " " + r.URL.Path {
		case "GET /containers/cntr_1":
			_, _ = io.WriteString(w, strings.Replace(containerJSON, `"active"`, `"future_status"`, 1))
		case "GET /containers/cntr_1/files/cfile_1":
			_, _ = io.WriteString(w, containerFileJSON)
		case "GET /containers/cntr_1/files/cfile_1/content":
			_, _ = w.Write([]byte{0, 255})
		case "DELETE /containers/cntr_1", "DELETE /containers/cntr_1/files/cfile_1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
	}, io.Discard)
	container, err := client.RetrieveContainer(t.Context(), testAttempt(), "cntr_1")
	if err != nil || container.Status != "future_status" {
		t.Fatalf("container = %#v, %v", container, err)
	}
	file, err := client.RetrieveContainerFile(t.Context(), testAttempt(), "cntr_1", "cfile_1")
	if err != nil || file.Path != "/mnt/data/input.txt" {
		t.Fatalf("file = %#v, %v", file, err)
	}
	content, err := client.ContainerFileContent(t.Context(), testAttempt(), "cntr_1", "cfile_1")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(content)
	closeErr := content.Close()
	if err != nil || closeErr != nil || string(body) != "\x00\xff" {
		t.Fatalf("content = %v, %v, %v", body, err, closeErr)
	}
	if err := client.DeleteContainerFile(t.Context(), testAttempt(), "cntr_1", "cfile_1"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteContainer(t.Context(), testAttempt(), "cntr_1"); err != nil || calls != 5 {
		t.Fatalf("delete = %v, calls=%d", err, calls)
	}
}

func TestContainerRejectsMalformedSuccess(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, strings.Replace(containerJSON, `"created_at":0,`, "", 1),
		strings.Replace(containerJSON, `"name":"work"`, `"name":null`, 1),
		strings.Replace(containerJSON, `"id":"cntr_1"`, `"id":"cntr_other"`, 1),
		strings.TrimSuffix(containerJSON, "}") + `,"memory_limit":null}`,
		strings.TrimSuffix(containerJSON, "}") + `,"expires_after":{"anchor":null}}`,
		strings.TrimSuffix(containerJSON, "}") + `,"network_policy":{"type":"allowlist","allowed_domains":[null]}}`,
	} {
		t.Run(body, func(t *testing.T) {
			client := testClient(t, staticJSON(body), io.Discard)
			_, err := client.RetrieveContainer(t.Context(), testAttempt(), "cntr_1")
			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError {
				t.Fatalf("error = %v", err)
			}
		})
	}
	for _, item := range []string{`null`, strings.Replace(containerFileJSON, `"bytes":0,`, "", 1), strings.Replace(containerFileJSON, `"container_id":"cntr_1"`, `"container_id":"other"`, 1)} {
		client := testClient(t, staticJSON(`{"object":"list","data":[`+item+`],"first_id":"cfile_1","last_id":"cfile_1","has_more":false}`), io.Discard)
		if page, err := client.ListContainerFiles(t.Context(), testAttempt(), "cntr_1", openai.ContainerFileListParams{}); err == nil || len(page.Data) != 0 {
			t.Fatalf("accepted invalid list item: %#v, %v", page, err)
		}
	}
}

func TestContainerRejectsInvalidInputsBeforeDispatch(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request dispatched") }, io.Discard)
	for _, request := range []openai.ContainerCreate{
		{Name: string([]byte{255})},
		{Name: "work", MemoryLimit: generation.Null[string]()},
		{Name: "work", ExpiresAfter: generation.Some(openai.ContainerExpiration{Anchor: "created_at", Minutes: 20})},
		{Name: "work", Skills: []generation.ShellSkill{generation.ShellSkillReference{SkillID: "skill", Version: generation.Some("0")}}},
		{Name: "work", Skills: []generation.ShellSkill{generation.ShellInlineSkill{Name: "skill", Data: "not base64"}}},
		{Name: "work", FileIDs: []string{""}},
	} {
		_, err := client.CreateContainer(t.Context(), testAttempt(), request)
		var input *openai.InputError
		if !errors.As(err, &input) {
			t.Fatalf("error = %v", err)
		}
	}
	for _, params := range []openai.ContainerFileListParams{{Limit: generation.Some(int64(0))}, {Limit: generation.Some(int64(101))}, {Order: generation.Some("random")}, {After: generation.Null[string]()}} {
		_, err := client.ListContainerFiles(t.Context(), testAttempt(), "cntr_1", params)
		var input *openai.InputError
		if !errors.As(err, &input) {
			t.Fatalf("error = %v", err)
		}
	}
	if _, err := client.AttachContainerFile(t.Context(), testAttempt(), "cntr_1", ""); err == nil {
		t.Fatal("accepted empty file ID")
	}
}
