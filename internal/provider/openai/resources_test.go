package openai_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
)

func TestResourceUploadsRejectControlFilenames(t *testing.T) {
	for name, upload := range resourceUploaders() {
		t.Run(name, func(t *testing.T) {
			client := clientWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("invalid upload reached transport")
				return nil, errors.New("unexpected request")
			}))
			for _, filename := range []string{"bad\x00name", "bad\x01name", "bad\x1fname", "bad\x7fname", "bad\rname", "bad\nname"} {
				source := &trackedFileBody{Reader: strings.NewReader("file bytes")}
				err := upload(t.Context(), client, filename, source)
				var input *openai.InputError
				if !errors.As(err, &input) || source.reads != 0 || !source.closed {
					t.Fatalf("filename %q: error=%v reads=%d closed=%v", filename, err, source.reads, source.closed)
				}
			}
		})
	}
}

func TestResourceUploadReaderOwnershipAndFailure(t *testing.T) {
	for name, upload := range resourceUploaders() {
		for _, tc := range []struct {
			name      string
			readFails bool
		}{{name: "success"}, {name: "read_error", readFails: true}} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				cause := errors.New("private upload read failure")
				var reader io.Reader = strings.NewReader("file bytes")
				if tc.readFails {
					reader = io.MultiReader(reader, uploadErrorReader{cause})
				}
				source := &trackedFileBody{Reader: reader}
				responseJSON := uploadedFile
				if name == "container" {
					responseJSON = containerFileJSON
				}
				responseBody := &trackedFileBody{Reader: strings.NewReader(responseJSON)}
				calls := 0
				client := clientWithTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.GetBody != nil {
						t.Error("upload body is rewindable")
					}
					_, err := io.ReadAll(r.Body)
					_ = r.Body.Close()
					if err != nil {
						return nil, err
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: responseBody}, nil
				}))
				err := upload(t.Context(), client, "input.txt", source)
				if !source.closed || source.reads == 0 || calls != 1 {
					t.Fatalf("reads=%d source closed=%v calls=%d", source.reads, source.closed, calls)
				}
				if tc.readFails {
					var failure *generation.Failure
					if !errors.As(err, &failure) || failure.Kind != generation.TransportError || !failure.OutcomeUnknown || strings.Contains(err.Error(), "private") {
						t.Fatalf("read failure = %v", err)
					}
				} else if err != nil || !responseBody.closed {
					t.Fatalf("success error=%v response closed=%v", err, responseBody.closed)
				}
			})
		}
	}
}

func TestResourceUploadCleanupPreservesOriginalError(t *testing.T) {
	for name, upload := range resourceUploaders() {
		for _, tc := range []struct {
			name, filename string
			status         int
		}{
			{name: "success", filename: "input.txt", status: 200},
			{name: "provider failure", filename: "input.txt", status: 429},
			{name: "validation failure", filename: "bad\nname", status: 200},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				source := &failingCloseUpload{Reader: strings.NewReader("content")}
				client := clientWithTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					_, _ = io.Copy(io.Discard, r.Body)
					_ = r.Body.Close()
					body := uploadedFile
					if name == "container" {
						body = containerFileJSON
					}
					return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				}))

				err := upload(t.Context(), client, tc.filename, source)

				if err == nil || source.closes != 1 || strings.Contains(err.Error(), "private") {
					t.Fatalf("error=%v closes=%d", err, source.closes)
				}
				if tc.name == "validation failure" {
					var input *openai.InputError
					if !errors.As(err, &input) {
						t.Fatalf("lost validation error: %v", err)
					}
				} else if tc.status == 429 {
					var failure *openai.HTTPError
					if !errors.As(err, &failure) || failure.StatusCode != 429 {
						t.Fatalf("lost provider error: %v", err)
					}
				}
			})
		}
	}
}

func TestFileUploadClosesSourceBeforeValidationReturn(t *testing.T) {
	client := clientWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("invalid purpose dispatched"); return nil, nil }))
	source := &failingCloseUpload{Reader: strings.NewReader("content")}

	_, err := client.UploadFile(t.Context(), testAttempt(), openai.FileUpload{Purpose: "invalid", Content: source})

	var input *openai.InputError
	if !errors.As(err, &input) || input.Field != "purpose" || source.closes != 1 {
		t.Fatalf("error=%v closes=%d", err, source.closes)
	}
}

type failingCloseUpload struct {
	io.Reader
	closes int
}

func (source *failingCloseUpload) Close() error {
	source.closes++
	return errors.New("private source error")
}

func TestResourceUploadClosesBlockedRead(t *testing.T) {
	for name, upload := range resourceUploaders() {
		for _, cancelRequest := range []bool{false, true} {
			label := "early response"
			if cancelRequest {
				label = "cancellation"
			}
			t.Run(name+"/"+label, func(t *testing.T) {
				source := &controlledUploadReader{entered: make(chan struct{}), closed: make(chan struct{})}
				readingDone := make(chan struct{})
				responseJSON := uploadedFile
				if name == "container" {
					responseJSON = containerFileJSON
				}
				client := clientWithTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					go func() {
						defer close(readingDone)
						_, _ = io.Copy(io.Discard, r.Body)
					}()
					<-source.entered
					if cancelRequest {
						<-r.Context().Done()
						_ = r.Body.Close()
						return nil, r.Context().Err()
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(responseJSON))}, nil
				}))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				finished := make(chan error, 1)

				go func() { finished <- upload(ctx, client, "input.txt", source) }()
				<-source.entered
				if cancelRequest {
					cancel()
				}

				select {
				case err := <-finished:
					if cancelRequest && !errors.Is(err, context.Canceled) {
						t.Fatalf("cancellation error = %v", err)
					}
					if !cancelRequest && err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("upload did not close the blocked source")
				}
				<-readingDone
				if source.closes.Load() != 1 {
					t.Fatalf("source closed %d times", source.closes.Load())
				}
			})
		}
	}
}

type controlledUploadReader struct {
	entered, closed chan struct{}
	closes          atomic.Int64
}

func (source *controlledUploadReader) Read([]byte) (int, error) {
	close(source.entered)
	<-source.closed
	return 0, io.EOF
}

func (source *controlledUploadReader) Close() error {
	if source.closes.Add(1) == 1 {
		close(source.closed)
	}
	return nil
}

func TestResourceContentCancellationAfterOpen(t *testing.T) {
	for name, open := range map[string]func(context.Context, *openai.Client) (io.ReadCloser, error){
		"file": func(ctx context.Context, c *openai.Client) (io.ReadCloser, error) {
			return c.FileContent(ctx, testAttempt(), "file_1")
		},
		"container": func(ctx context.Context, c *openai.Client) (io.ReadCloser, error) {
			return c.ContainerFileContent(ctx, testAttempt(), "cntr_1", "cfile_1")
		},
	} {
		t.Run(name, func(t *testing.T) {
			stopped := make(chan struct{})
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(stopped)
			}, io.Discard)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			body, err := open(ctx, client)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = body.Close() })
			finished := make(chan error, 1)
			go func() {
				_, err := io.ReadAll(body)
				finished <- err
			}()
			cancel()
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("body read = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not interrupt content read")
			}
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("provider request was not cancelled")
			}
		})
	}
}

func resourceUploaders() map[string]func(context.Context, *openai.Client, string, io.ReadCloser) error {
	return map[string]func(context.Context, *openai.Client, string, io.ReadCloser) error{
		"file": func(ctx context.Context, c *openai.Client, filename string, content io.ReadCloser) error {
			_, err := c.UploadFile(ctx, testAttempt(), openai.FileUpload{Filename: filename, Purpose: "user_data", Content: content})
			return err
		},
		"container": func(ctx context.Context, c *openai.Client, filename string, content io.ReadCloser) error {
			_, err := c.UploadContainerFile(ctx, testAttempt(), "cntr_1", filename, content)
			return err
		},
	}
}

type uploadErrorReader struct{ err error }

func (r uploadErrorReader) Read([]byte) (int, error) { return 0, r.err }
