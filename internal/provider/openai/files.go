package openai

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"

	"github.com/deyna256/clan/internal/generation"
)

// FileUpload transfers ownership of Content to UploadFile, including on validation
// failure. Content.Close must be safe concurrently with Read and unblock it.
type FileUpload struct {
	Filename, Purpose string
	Content           io.ReadCloser
	ExpiresAfter      generation.Optional[FileExpiration]
}

type FileExpiration struct {
	Anchor  string
	Seconds int64
}

type File struct {
	ID            string                      `json:"id"`
	Bytes         int64                       `json:"bytes"`
	CreatedAt     int64                       `json:"created_at"`
	Filename      string                      `json:"filename"`
	Object        string                      `json:"object"`
	Purpose       string                      `json:"purpose"`
	ExpiresAt     generation.Optional[int64]  `json:"expires_at,omitzero"`
	Status        generation.Optional[string] `json:"status,omitzero"`
	StatusDetails generation.Optional[string] `json:"status_details,omitzero"`
}

type FileDeleted struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

func (c *Client) UploadFile(ctx context.Context, attempt Attempt, upload FileUpload) (file File, err error) {
	source := &uploadSource{ReadCloser: upload.Content}
	defer source.finish(&err)
	if !slices.Contains([]string{"assistants", "batch", "fine-tune", "vision", "user_data", "evals"}, upload.Purpose) {
		return File{}, &InputError{Field: "purpose", Problem: "unsupported file purpose"}
	}
	fields := map[string]string{"purpose": upload.Purpose}
	if upload.ExpiresAfter.IsNull() {
		return File{}, &InputError{Field: "expires_after", Problem: "must not be null"}
	}
	if expiry, ok := upload.ExpiresAfter.Value(); ok {
		if expiry.Anchor != "created_at" || expiry.Seconds < 3600 || expiry.Seconds > 2592000 {
			return File{}, &InputError{Field: "expires_after", Problem: "expected created_at and 3600 to 2592000 seconds"}
		}
		fields["expires_after[anchor]"] = expiry.Anchor
		fields["expires_after[seconds]"] = strconv.FormatInt(expiry.Seconds, 10)
	}
	return decodeFile(c.resourceUpload(ctx, attempt, []string{"files"}, fields, upload.Filename, source))
}

func (c *Client) RetrieveFile(ctx context.Context, attempt Attempt, fileID string) (File, error) {
	file, err := decodeFile(c.resourceJSON(ctx, attempt, http.MethodGet, []string{"files", fileID}, nil, nil))
	if err == nil && file.ID != fileID {
		return File{}, protocolError()
	}
	return file, err
}

// FileContent returns a streaming body. The caller must close it.
func (c *Client) FileContent(ctx context.Context, attempt Attempt, fileID string) (io.ReadCloser, error) {
	return c.resourceContent(ctx, attempt, []string{"files", fileID, "content"})
}

func (c *Client) DeleteFile(ctx context.Context, attempt Attempt, fileID string) (FileDeleted, error) {
	body, err := c.resourceJSON(ctx, attempt, http.MethodDelete, []string{"files", fileID}, nil, nil)
	result, err := decodeResource[FileDeleted](body, err)
	if err != nil {
		return FileDeleted{}, err
	}
	if resourceRequired(body, "id", "object", "deleted") != nil || result.ID != fileID || result.Object != "file" {
		return FileDeleted{}, protocolError()
	}
	return result, nil
}

func decodeFile(body []byte, requestErr error) (File, error) {
	file, err := decodeResource[File](body, requestErr)
	if err != nil {
		return File{}, err
	}
	if resourceRequired(body, "id", "bytes", "created_at", "filename", "object", "purpose") != nil || file.ID == "" || file.Object != "file" || file.Bytes < 0 || file.CreatedAt < 0 || file.ExpiresAt.IsNull() || file.Status.IsNull() || file.StatusDetails.IsNull() {
		return File{}, protocolError()
	}
	// The deprecated status is absent from official successful response examples.
	if status, ok := file.Status.Value(); ok && !slices.Contains([]string{"uploaded", "processed", "error"}, status) {
		return File{}, protocolError()
	}
	if expiry, ok := file.ExpiresAt.Value(); ok && expiry < 0 {
		return File{}, protocolError()
	}
	// evals is an upload purpose even though the output schema omits that value.
	if !slices.Contains([]string{"assistants", "assistants_output", "batch", "batch_output", "fine-tune", "fine-tune-results", "vision", "user_data", "evals"}, file.Purpose) {
		return File{}, protocolError()
	}
	return file, nil
}
