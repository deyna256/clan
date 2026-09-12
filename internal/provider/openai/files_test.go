package openai_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
)

const uploadedFile = `{"id":"file_1","object":"file","bytes":0,"created_at":0,"filename":"input.txt","purpose":"evals"}`

func TestUploadFileMultipartAndExpiry(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/files" || r.Header.Get("Authorization") != "Bearer secret-key" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		parts, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		got := make(map[string]string)
		for {
			part, err := parts.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Error(err)
				return
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Error(err)
			}
			if part.FormName() == "file" && part.FileName() != "input.txt" {
				t.Errorf("filename = %q", part.FileName())
			}
			got[part.FormName()] = string(body)
		}
		want := map[string]string{"file": "hello\x00world", "purpose": "evals", "expires_after[anchor]": "created_at", "expires_after[seconds]": "3600"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("multipart = %#v", got)
		}
		_, _ = io.WriteString(w, uploadedFile)
	}, io.Discard)
	file, err := client.UploadFile(t.Context(), testAttempt(), openai.FileUpload{
		Filename: "input.txt", Purpose: "evals", Content: io.NopCloser(strings.NewReader("hello\x00world")),
		ExpiresAfter: generation.Some(openai.FileExpiration{Anchor: "created_at", Seconds: 3600}),
	})
	if err != nil || file.ID != "file_1" || !file.Status.IsZero() || file.Bytes != 0 {
		t.Fatalf("file = %#v, %v", file, err)
	}
}

func TestFileRetrieveAndDelete(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files/file_1" || r.URL.RawQuery != "" {
			t.Errorf("URL = %s", r.URL)
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, strings.TrimSuffix(uploadedFile, "}")+`,"status":"error","status_details":"processing failed","expires_at":0}`)
		case http.MethodDelete:
			_, _ = io.WriteString(w, `{"id":"file_1","object":"file","deleted":false}`)
		default:
			t.Errorf("method = %s", r.Method)
		}
	}, io.Discard)
	file, err := client.RetrieveFile(t.Context(), testAttempt(), "file_1")
	if err != nil || file.Status != generation.Some("error") || file.ExpiresAt != generation.Some(int64(0)) {
		t.Fatalf("file = %#v, %v", file, err)
	}
	deleted, err := client.DeleteFile(t.Context(), testAttempt(), "file_1")
	if err != nil || deleted.ID != "file_1" || deleted.Deleted {
		t.Fatalf("delete = %#v, %v", deleted, err)
	}
}

func TestFilesRejectMalformedSuccess(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `[]`, uploadedFile + `{}`, strings.Replace(uploadedFile, `"bytes":0,`, "", 1),
		strings.Replace(uploadedFile, `"created_at":0`, `"created_at":null`, 1),
		strings.Replace(uploadedFile, `"bytes":0`, `"bytes":-1`, 1),
		strings.Replace(uploadedFile, `"filename":"input.txt"`, `"filename":null`, 1),
		strings.Replace(uploadedFile, `"id":"file_1"`, `"id":"other"`, 1),
		strings.TrimSuffix(uploadedFile, "}") + `,"expires_at":null}`,
		strings.TrimSuffix(uploadedFile, "}") + `,"status":null}`,
	} {
		t.Run(body, func(t *testing.T) {
			client := testClient(t, staticJSON(body), io.Discard)
			file, err := client.RetrieveFile(t.Context(), testAttempt(), "file_1")
			var failure *generation.Failure
			if !errors.As(err, &failure) || failure.Kind != generation.ProtocolError || file.ID != "" {
				t.Fatalf("file = %#v, %v", file, err)
			}
		})
	}
	client := testClient(t, staticJSON(`{"id":"file_1","object":"file"}`), io.Discard)
	if _, err := client.DeleteFile(t.Context(), testAttempt(), "file_1"); err == nil {
		t.Fatal("accepted missing deleted flag")
	}
}

func TestFileUploadValidatesBeforeDispatch(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid upload dispatched") }, io.Discard)
	for _, upload := range []openai.FileUpload{
		{Filename: "input", Purpose: "assistants_output", Content: io.NopCloser(strings.NewReader(""))},
		{Filename: "input", Purpose: "user_data"},
		{Filename: "bad\nname", Purpose: "user_data", Content: io.NopCloser(strings.NewReader(""))},
		{Filename: "input", Purpose: "user_data", Content: io.NopCloser(strings.NewReader("")), ExpiresAfter: generation.Null[openai.FileExpiration]()},
		{Filename: "input", Purpose: "user_data", Content: io.NopCloser(strings.NewReader("")), ExpiresAfter: generation.Some(openai.FileExpiration{Anchor: "created_at", Seconds: 3599})},
		{Filename: "input", Purpose: "user_data", Content: io.NopCloser(strings.NewReader("")), ExpiresAfter: generation.Some(openai.FileExpiration{Anchor: "created_at", Seconds: 2592001})},
	} {
		_, err := client.UploadFile(t.Context(), testAttempt(), upload)
		var input *openai.InputError
		if !errors.As(err, &input) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestFileContentStreamsAndCallerCloses(t *testing.T) {
	body := &trackedFileBody{Reader: bytes.NewReader([]byte{0, 255, 1})}
	client := clientWithTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/files/file_1/content" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	}))
	content, err := client.FileContent(t.Context(), testAttempt(), "file_1")
	if err != nil || body.reads != 0 || body.closed {
		t.Fatalf("content = %v, reads=%d closed=%v", err, body.reads, body.closed)
	}
	got, err := io.ReadAll(content)
	if err != nil || !bytes.Equal(got, []byte{0, 255, 1}) {
		t.Fatalf("content = %v, %v", got, err)
	}
	if err := content.Close(); err != nil || !body.closed {
		t.Fatalf("close = %v, closed=%v", err, body.closed)
	}
}

func TestFileHTTPFailureClosesBodyWithoutRetry(t *testing.T) {
	body := &trackedFileBody{Reader: strings.NewReader(`{"error":{"code":"rate_limit_exceeded","message":"private provider detail"}}`)}
	calls := 0
	client := clientWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"3"}}, Body: body}, nil
	}))
	content, err := client.FileContent(t.Context(), testAttempt(), "file_1")
	var failure *openai.HTTPError
	if !errors.As(err, &failure) || failure.StatusCode != 429 || content != nil || !body.closed || calls != 1 {
		t.Fatalf("error = %v, content=%v closed=%v calls=%d", err, content, body.closed, calls)
	}
}

type trackedFileBody struct {
	io.Reader
	reads  int
	closed bool
}

func (b *trackedFileBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func (b *trackedFileBody) Close() error { b.closed = true; return nil }
