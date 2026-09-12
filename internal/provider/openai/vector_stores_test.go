package openai_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
)

func TestCreateVectorStore(t *testing.T) {
	client, requests := vectorClient(t, vectorStoreJSON("in_progress"))
	request := openai.CreateVectorStoreRequest{
		Name: generation.Some("Knowledge"), Description: generation.Some("Docs"), FileIDs: generation.Some([]string{"file_1"}),
		Metadata: generation.Some(map[string]string{"project": "clan"}), ExpiresAfter: generation.Some(openai.VectorStoreExpiry{Anchor: "last_active_at", Days: 7}),
		ChunkingStrategy: generation.Some(openai.VectorStoreChunking{Type: "static", Static: generation.Some(openai.VectorStoreStaticChunking{MaxChunkSizeTokens: 100, ChunkOverlapTokens: 50})}),
	}

	store, err := client.CreateVectorStore(t.Context(), testAttempt(), request)

	if err != nil || store.ID != "vs_1" || store.Status != "in_progress" || store.FileCounts.InProgress != 1 || !store.LastActiveAt.IsNull() || !store.Metadata.IsNull() {
		t.Fatalf("store = %#v, %v; want pending store with one file and null metadata", store, err)
	}
	if len(*requests) != 1 || (*requests)[0].Method != "POST" || (*requests)[0].Path != "/v1/vector_stores" || (*requests)[0].Beta != "assistants=v2" {
		t.Fatalf("requests = %#v; want one create request with beta header", *requests)
	}
	assertRequestJSON(t, (*requests)[0].Body, `{"name":"Knowledge","description":"Docs","file_ids":["file_1"],"metadata":{"project":"clan"},"expires_after":{"anchor":"last_active_at","days":7},"chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":100,"chunk_overlap_tokens":50}}}`)
}

func TestCreateVectorStorePresence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request openai.CreateVectorStoreRequest
		want    string
	}{
		{name: "omitted", want: `{}`},
		{name: "null metadata", request: openai.CreateVectorStoreRequest{Metadata: generation.Null[map[string]string]()}, want: `{"metadata":null}`},
		{name: "empty values", request: openai.CreateVectorStoreRequest{Name: generation.Some(""), Description: generation.Some(""), FileIDs: generation.Some([]string(nil)), Metadata: generation.Some(map[string]string(nil))}, want: `{"name":"","description":"","file_ids":[],"metadata":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, requests := vectorClient(t, vectorStoreJSON("completed"))

			_, err := client.CreateVectorStore(t.Context(), testAttempt(), tc.request)

			if err != nil {
				t.Fatal(err)
			}
			assertRequestJSON(t, (*requests)[0].Body, tc.want)
		})
	}
}

func TestAttachVectorStoreFilePreservesAttributesAndFailure(t *testing.T) {
	client, requests := vectorClient(t, vectorFileJSON("failed", `{"code":"invalid_file","message":"private provider detail"}`, `,"attributes":{"revision":9007199254740993,"active":false},"chunking_strategy":{"type":"other"}`))
	request := openai.AttachVectorStoreFileRequest{
		FileID: "file_1", Attributes: generation.Some(map[string]json.RawMessage{"revision": json.RawMessage(`9007199254740993`), "active": json.RawMessage(`false`)}),
		ChunkingStrategy: generation.Some(openai.VectorStoreChunking{Type: "auto"}),
	}

	file, err := client.AttachVectorStoreFile(t.Context(), testAttempt(), "vs_1", request)

	if err != nil {
		t.Fatal(err)
	}
	attributes, _ := file.Attributes.Value()
	last, _ := file.LastError.Value()
	chunking, _ := file.ChunkingStrategy.Value()
	if file.Status != "failed" || string(attributes["revision"]) != "9007199254740993" || last != (openai.VectorStoreFileError{Code: "invalid_file", Message: "private provider detail"}) || chunking.Type != "other" {
		t.Fatalf("file = %#v; want failed file with preserved error, integer, and legacy chunking", file)
	}
	if len(*requests) != 1 || (*requests)[0].Path != "/v1/vector_stores/vs_1/files" || (*requests)[0].Method != "POST" {
		t.Fatalf("requests = %#v; want one attach request", *requests)
	}
	assertRequestJSON(t, (*requests)[0].Body, `{"file_id":"file_1","attributes":{"revision":9007199254740993,"active":false},"chunking_strategy":{"type":"auto"}}`)
	attributes["revision"][0] = '1'
	unchanged, _ := request.Attributes.Value()
	if string(unchanged["revision"]) != "9007199254740993" {
		t.Fatal("reply attributes alias request data")
	}
}

func TestRetrieveVectorStoreAndFile(t *testing.T) {
	client, requests := vectorClient(t, vectorStoreJSON("expired"))

	store, err := client.RetrieveVectorStore(t.Context(), testAttempt(), "vs_1")

	if err != nil || store.Status != "expired" || (*requests)[0].Method != "GET" || (*requests)[0].Path != "/v1/vector_stores/vs_1" {
		t.Fatalf("store = %#v, %v; requests = %#v", store, err, *requests)
	}
	client, requests = vectorClient(t, vectorFileJSON("cancelled", "null", `,"chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":800,"chunk_overlap_tokens":400}}`))

	file, err := client.RetrieveVectorStoreFile(t.Context(), testAttempt(), "vs_1", "file_1")

	chunking, _ := file.ChunkingStrategy.Value()
	chunk, _ := chunking.Static.Value()
	if err != nil || file.Status != "cancelled" || !file.LastError.IsNull() || chunk.MaxChunkSizeTokens != 800 || chunk.ChunkOverlapTokens != 400 || (*requests)[0].Path != "/v1/vector_stores/vs_1/files/file_1" {
		t.Fatalf("file = %#v, %v; requests = %#v", file, err, *requests)
	}
}

func TestListVectorStoreFilesReturnsOnePage(t *testing.T) {
	client, requests := vectorClient(t, `{"object":"list","data":[`+vectorFileJSON("in_progress", "null", "")+`],"first_id":"file_1","last_id":"file_1","has_more":true}`)
	options := openai.ListVectorStoreFilesOptions{After: "cursor&x=1", Before: "before?", Limit: generation.Some(int64(1)), Order: "asc", Filter: "in_progress"}

	page, err := client.ListVectorStoreFiles(t.Context(), testAttempt(), "vs_1", options)

	if err != nil || len(page.Data) != 1 || page.Data[0].Status != "in_progress" || page.FirstID != "file_1" || page.LastID != "file_1" || !page.HasMore {
		t.Fatalf("page = %#v, %v; want one pending file and continuation", page, err)
	}
	if len(*requests) != 1 || (*requests)[0].Query != "after=cursor%26x%3D1&before=before%3F&filter=in_progress&limit=1&order=asc" {
		t.Fatalf("requests = %#v; want one explicitly paginated request", *requests)
	}
}

func TestVectorStoreFilePagePresenceAndAtomicFailure(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{name: "empty page", data: `,"data":[]`, valid: true},
		{name: "missing data"},
		{name: "null data", data: `,"data":null`},
		{name: "invalid second item", data: `,"data":[` + vectorFileJSON("completed", "null", "") + `,null]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := vectorClient(t, `{"object":"list","first_id":"","last_id":"","has_more":false`+tc.data+`}`)

			page, err := client.ListVectorStoreFiles(t.Context(), testAttempt(), "vs_1", openai.ListVectorStoreFilesOptions{})

			if tc.valid {
				if err != nil || page.Data == nil || len(page.Data) != 0 || page.HasMore || page.FirstID != "" || page.LastID != "" {
					t.Fatalf("empty page = %#v, %v", page, err)
				}
			} else {
				assertProtocolError(t, err)
				if page.Data != nil {
					t.Fatalf("invalid page retained earlier items: %#v", page)
				}
			}
		})
	}
}

func TestDeleteVectorStoreAndDetachFile(t *testing.T) {
	client, requests := vectorClient(t, `{"id":"vs_1","object":"vector_store.deleted","deleted":true}`)

	deleted, err := client.DeleteVectorStore(t.Context(), testAttempt(), "vs_1")

	if err != nil || !deleted.Deleted || (*requests)[0].Method != "DELETE" || (*requests)[0].Path != "/v1/vector_stores/vs_1" {
		t.Fatalf("deleted = %#v, %v; requests = %#v", deleted, err, *requests)
	}
	client, requests = vectorClient(t, `{"id":"file_1","object":"vector_store.file.deleted","deleted":true}`)

	detached, err := client.DetachVectorStoreFile(t.Context(), testAttempt(), "vs_1", "file_1")

	if err != nil || !detached.Deleted || len(*requests) != 1 || (*requests)[0].Method != "DELETE" || (*requests)[0].Path != "/v1/vector_stores/vs_1/files/file_1" {
		t.Fatalf("detached = %#v, %v; requests = %#v; want only detach endpoint", detached, err, *requests)
	}
}

func TestVectorStoreFileValidationBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request openai.AttachVectorStoreFileRequest
		field   string
	}{
		{name: "missing file ID", field: "file_id"},
		{name: "nested attribute", request: openai.AttachVectorStoreFileRequest{FileID: "f", Attributes: generation.Some(map[string]json.RawMessage{"key": json.RawMessage(`{}`)})}, field: "attributes"},
		{name: "null attribute", request: openai.AttachVectorStoreFileRequest{FileID: "f", Attributes: generation.Some(map[string]json.RawMessage{"key": json.RawMessage(`null`)})}, field: "attributes"},
		{name: "invalid attribute JSON", request: openai.AttachVectorStoreFileRequest{FileID: "f", Attributes: generation.Some(map[string]json.RawMessage{"key": json.RawMessage(`secret`)})}, field: "attributes"},
		{name: "null chunking", request: openai.AttachVectorStoreFileRequest{FileID: "f", ChunkingStrategy: generation.Null[openai.VectorStoreChunking]()}, field: "chunking_strategy"},
		{name: "reply-only chunking", request: openai.AttachVectorStoreFileRequest{FileID: "f", ChunkingStrategy: generation.Some(openai.VectorStoreChunking{Type: "other"})}, field: "chunking_strategy"},
		{name: "small chunk", request: openai.AttachVectorStoreFileRequest{FileID: "f", ChunkingStrategy: staticChunk(99, 0)}, field: "chunking_strategy.static"},
		{name: "large overlap", request: openai.AttachVectorStoreFileRequest{FileID: "f", ChunkingStrategy: staticChunk(100, 51)}, field: "chunking_strategy.static"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, requests := vectorClient(t, `{}`)

			_, err := client.AttachVectorStoreFile(t.Context(), testAttempt(), "vs_1", tc.request)

			assertVectorInputError(t, err, tc.field, len(*requests))
		})
	}
}

func TestVectorStorePaginationValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options openai.ListVectorStoreFilesOptions
		field   string
	}{
		{name: "zero limit", options: openai.ListVectorStoreFilesOptions{Limit: generation.Some(int64(0))}, field: "limit"},
		{name: "large limit", options: openai.ListVectorStoreFilesOptions{Limit: generation.Some(int64(101))}, field: "limit"},
		{name: "null limit", options: openai.ListVectorStoreFilesOptions{Limit: generation.Null[int64]()}, field: "limit"},
		{name: "invalid order", options: openai.ListVectorStoreFilesOptions{Order: "secret"}, field: "order"},
		{name: "invalid filter", options: openai.ListVectorStoreFilesOptions{Filter: "secret"}, field: "filter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, requests := vectorClient(t, `{}`)

			_, err := client.ListVectorStoreFiles(t.Context(), testAttempt(), "vs_1", tc.options)

			assertVectorInputError(t, err, tc.field, len(*requests))
		})
	}
}

func TestVectorStoreFileMalformedReplies(t *testing.T) {
	valid := vectorFileJSON("completed", "null", "")
	for _, tc := range []struct{ name, body string }{
		{name: "wrong file", body: strings.Replace(valid, `"file_1"`, `"other"`, 1)},
		{name: "wrong store", body: strings.Replace(valid, `"vs_1"`, `"other"`, 1)},
		{name: "unknown status", body: strings.Replace(valid, `"completed"`, `"unknown"`, 1)},
		{name: "missing last error", body: strings.Replace(valid, `,"last_error":null`, "", 1)},
		{name: "request-only chunking", body: vectorFileJSON("completed", "null", `,"chunking_strategy":{"type":"auto"}`)},
		{name: "invalid error", body: vectorFileJSON("failed", `{"code":"unknown","message":"secret"}`, "")},
		{name: "missing error message", body: vectorFileJSON("failed", `{"code":"invalid_file"}`, "")},
		{name: "missing overlap", body: vectorFileJSON("completed", "null", `,"chunking_strategy":{"type":"static","static":{"max_chunk_size_tokens":100}}`)},
		{name: "invalid attributes", body: vectorFileJSON("completed", "null", `,"attributes":{"nested":[]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := vectorClient(t, tc.body)

			_, err := client.RetrieveVectorStoreFile(t.Context(), testAttempt(), "vs_1", "file_1")

			assertProtocolError(t, err)
		})
	}
}

func TestVectorStoreDeletionBooleanPresence(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		wantError   bool
	}{
		{name: "false is a value", field: `,"deleted":false`},
		{name: "missing", wantError: true},
		{name: "null", field: `,"deleted":null`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := vectorClient(t, `{"id":"vs_1","object":"vector_store.deleted"`+tc.field+`}`)

			deleted, err := client.DeleteVectorStore(t.Context(), testAttempt(), "vs_1")

			if tc.wantError {
				assertProtocolError(t, err)
			} else if err != nil || deleted.Deleted {
				t.Fatalf("deleted = %#v, %v; want explicit false", deleted, err)
			}
		})
	}
}

func TestVectorStoreRejectsMissingRequiredData(t *testing.T) {
	valid := vectorStoreJSON("completed")
	for _, tc := range []struct{ name, body string }{
		{name: "missing timestamp", body: strings.Replace(valid, `,"created_at":10`, "", 1)},
		{name: "missing count", body: strings.Replace(valid, `,"total":1`, "", 1)},
		{name: "unknown status", body: strings.Replace(valid, `"status":"completed"`, `"status":"unknown"`, 1)},
		{name: "wrong ID", body: strings.Replace(valid, `"vs_1"`, `"vs_other"`, 1)},
		{name: "null metadata value", body: strings.Replace(valid, `"metadata":null`, `"metadata":{"label":null}`, 1)},
		{name: "null name", body: strings.Replace(valid, `"name":"Knowledge"`, `"name":null`, 1)},
		{name: "null timestamp", body: strings.Replace(valid, `"created_at":10`, `"created_at":null`, 1)},
		{name: "null count", body: strings.Replace(valid, `"total":1`, `"total":null`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := vectorClient(t, tc.body)

			_, err := client.RetrieveVectorStore(t.Context(), testAttempt(), "vs_1")

			assertProtocolError(t, err)
		})
	}
}

func TestVectorStoreHTTPFailureDoesNotRetry(t *testing.T) {
	requests := 0
	client := clientWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limit_exceeded","message":"private detail"}}`))}, nil
	}))

	_, err := client.RetrieveVectorStore(t.Context(), testAttempt(), "vs_1")

	var failure *generation.Failure
	if requests != 1 || !errors.As(err, &failure) || failure.Kind != generation.RateLimited || strings.Contains(err.Error(), "private") {
		t.Fatalf("requests = %d; error = %v; want one classified rate-limit failure", requests, err)
	}
}

type vectorRequest struct{ Method, Path, Query, Body, Beta string }

func vectorClient(t *testing.T, reply string) (*openai.Client, *[]vectorRequest) {
	t.Helper()
	requests := []vectorRequest{}
	client := clientWithTransport(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body []byte
		if request.Body != nil {
			var err error
			body, err = io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
		}
		requests = append(requests, vectorRequest{Method: request.Method, Path: request.URL.EscapedPath(), Query: request.URL.RawQuery, Body: string(body), Beta: request.Header.Get("OpenAI-Beta")})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(reply))}, nil
	}))
	return client, &requests
}

func vectorStoreJSON(status string) string {
	return `{"id":"vs_1","object":"vector_store","created_at":10,"name":"Knowledge","status":"` + status + `","usage_bytes":0,"file_counts":{"cancelled":0,"completed":0,"failed":0,"in_progress":1,"total":1},"last_active_at":null,"metadata":null}`
}

func vectorFileJSON(status, lastError, extra string) string {
	return `{"id":"file_1","object":"vector_store.file","created_at":10,"vector_store_id":"vs_1","status":"` + status + `","usage_bytes":20,"last_error":` + lastError + extra + `}`
}

func staticChunk(size, overlap int64) generation.Optional[openai.VectorStoreChunking] {
	return generation.Some(openai.VectorStoreChunking{Type: "static", Static: generation.Some(openai.VectorStoreStaticChunking{MaxChunkSizeTokens: size, ChunkOverlapTokens: overlap})})
}

func assertVectorInputError(t *testing.T, err error, field string, calls int) {
	t.Helper()
	var input *openai.InputError
	var failure *generation.Failure
	if calls != 0 || !errors.As(err, &input) || input.Field != field || !errors.As(err, &failure) || failure.Kind != generation.InvalidRequest || failure.OutcomeUnknown {
		t.Fatalf("calls = %d; error = %v; want local invalid field %q", calls, err, field)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error exposes request data: %v", err)
	}
}
