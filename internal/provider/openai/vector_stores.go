package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
)

type CreateVectorStoreRequest struct {
	Name             generation.Optional[string]              `json:"name,omitzero"`
	Description      generation.Optional[string]              `json:"description,omitzero"`
	FileIDs          generation.Optional[[]string]            `json:"file_ids,omitzero"`
	Metadata         generation.Optional[map[string]string]   `json:"metadata,omitzero"`
	ExpiresAfter     generation.Optional[VectorStoreExpiry]   `json:"expires_after,omitzero"`
	ChunkingStrategy generation.Optional[VectorStoreChunking] `json:"chunking_strategy,omitzero"`
}

type VectorStoreExpiry struct {
	Anchor string `json:"anchor"`
	Days   int64  `json:"days"`
}

type VectorStoreChunking struct {
	Type   string                                         `json:"type"`
	Static generation.Optional[VectorStoreStaticChunking] `json:"static,omitzero"`
}

type VectorStoreStaticChunking struct {
	MaxChunkSizeTokens int64 `json:"max_chunk_size_tokens"`
	ChunkOverlapTokens int64 `json:"chunk_overlap_tokens"`
}

type VectorStore struct {
	ID           string                                 `json:"id"`
	Object       string                                 `json:"object"`
	CreatedAt    int64                                  `json:"created_at"`
	Name         string                                 `json:"name"`
	Status       string                                 `json:"status"`
	UsageBytes   int64                                  `json:"usage_bytes"`
	FileCounts   VectorStoreFileCounts                  `json:"file_counts"`
	LastActiveAt generation.Optional[int64]             `json:"last_active_at"`
	Metadata     generation.Optional[map[string]string] `json:"metadata"`
	ExpiresAfter generation.Optional[VectorStoreExpiry] `json:"expires_after"`
	ExpiresAt    generation.Optional[int64]             `json:"expires_at"`
}

type VectorStoreFileCounts struct {
	Cancelled  int64 `json:"cancelled"`
	Completed  int64 `json:"completed"`
	Failed     int64 `json:"failed"`
	InProgress int64 `json:"in_progress"`
	Total      int64 `json:"total"`
}

type VectorStoreDeleted struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

type AttachVectorStoreFileRequest struct {
	FileID           string                                          `json:"file_id"`
	Attributes       generation.Optional[map[string]json.RawMessage] `json:"attributes,omitzero"`
	ChunkingStrategy generation.Optional[VectorStoreChunking]        `json:"chunking_strategy,omitzero"`
}

type VectorStoreFile struct {
	ID               string                                          `json:"id"`
	Object           string                                          `json:"object"`
	CreatedAt        int64                                           `json:"created_at"`
	VectorStoreID    string                                          `json:"vector_store_id"`
	Status           string                                          `json:"status"`
	UsageBytes       int64                                           `json:"usage_bytes"`
	LastError        generation.Optional[VectorStoreFileError]       `json:"last_error"`
	Attributes       generation.Optional[map[string]json.RawMessage] `json:"attributes"`
	ChunkingStrategy generation.Optional[VectorStoreChunking]        `json:"chunking_strategy"`
}

type VectorStoreFileError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ListVectorStoreFilesOptions struct {
	After, Before string
	Limit         generation.Optional[int64]
	Order, Filter string
}

type VectorStoreFilePage struct {
	Data            []VectorStoreFile
	FirstID, LastID string
	HasMore         bool
}

type VectorStoreFileDeleted struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

func (c *Client) CreateVectorStore(ctx context.Context, attempt Attempt, request CreateVectorStoreRequest) (VectorStore, error) {
	for _, field := range []struct {
		name  string
		value generation.Optional[string]
	}{{"name", request.Name}, {"description", request.Description}} {
		if value, present := field.value.Value(); field.value.IsNull() || (present && !utf8.ValidString(value)) {
			return VectorStore{}, &InputError{Field: field.name, Problem: "must be a UTF-8 string"}
		}
	}
	if request.FileIDs.IsNull() || request.ExpiresAfter.IsNull() {
		return VectorStore{}, &InputError{Field: "body", Problem: "file_ids and expires_after cannot be null"}
	}
	if ids, present := request.FileIDs.Value(); present {
		for _, id := range ids {
			if err := vectorID("file_ids", id); err != nil {
				return VectorStore{}, err
			}
		}
		if ids == nil {
			request.FileIDs = generation.Some([]string{})
		}
	}
	if metadata, present := request.Metadata.Value(); present {
		if _, err := wire.EncodeMetadata(metadata); err != nil {
			return VectorStore{}, &InputError{Field: "metadata", Problem: "invalid metadata"}
		}
		if metadata == nil {
			request.Metadata = generation.Some(map[string]string{})
		}
	}
	if expiry, present := request.ExpiresAfter.Value(); present && expiry.Anchor != "last_active_at" {
		return VectorStore{}, &InputError{Field: "expires_after.anchor", Problem: "expected last_active_at"}
	}
	if err := vectorChunking(request.ChunkingStrategy, false); err != nil {
		return VectorStore{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodPost, []string{"vector_stores"}, nil, request)
	return decodeVectorStore(body, err, "")
}

func (c *Client) RetrieveVectorStore(ctx context.Context, attempt Attempt, id string) (VectorStore, error) {
	if err := vectorID("vector_store_id", id); err != nil {
		return VectorStore{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodGet, []string{"vector_stores", id}, nil, nil)
	return decodeVectorStore(body, err, id)
}

func (c *Client) DeleteVectorStore(ctx context.Context, attempt Attempt, id string) (VectorStoreDeleted, error) {
	if err := vectorID("vector_store_id", id); err != nil {
		return VectorStoreDeleted{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodDelete, []string{"vector_stores", id}, nil, nil)
	value, err := decodeResource[VectorStoreDeleted](body, err)
	if err == nil && (resourceRequired(body, "id", "object", "deleted") != nil || value.ID != id || value.Object != "vector_store.deleted") {
		return VectorStoreDeleted{}, protocolError()
	}
	return value, err
}

func (c *Client) AttachVectorStoreFile(ctx context.Context, attempt Attempt, storeID string, request AttachVectorStoreFileRequest) (VectorStoreFile, error) {
	if err := vectorFileIDs(storeID, request.FileID); err != nil {
		return VectorStoreFile{}, err
	}
	if err := vectorAttributes(request.Attributes); err != nil {
		return VectorStoreFile{}, err
	}
	if attributes, present := request.Attributes.Value(); present && attributes == nil {
		request.Attributes = generation.Some(map[string]json.RawMessage{})
	}
	if err := vectorChunking(request.ChunkingStrategy, false); err != nil {
		return VectorStoreFile{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodPost, []string{"vector_stores", storeID, "files"}, nil, request)
	return decodeVectorStoreFile(body, err, storeID, request.FileID)
}

func (c *Client) RetrieveVectorStoreFile(ctx context.Context, attempt Attempt, storeID, fileID string) (VectorStoreFile, error) {
	if err := vectorFileIDs(storeID, fileID); err != nil {
		return VectorStoreFile{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodGet, []string{"vector_stores", storeID, "files", fileID}, nil, nil)
	return decodeVectorStoreFile(body, err, storeID, fileID)
}

func (c *Client) DetachVectorStoreFile(ctx context.Context, attempt Attempt, storeID, fileID string) (VectorStoreFileDeleted, error) {
	if err := vectorFileIDs(storeID, fileID); err != nil {
		return VectorStoreFileDeleted{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodDelete, []string{"vector_stores", storeID, "files", fileID}, nil, nil)
	value, err := decodeResource[VectorStoreFileDeleted](body, err)
	if err == nil && (resourceRequired(body, "id", "object", "deleted") != nil || value.ID != fileID || value.Object != "vector_store.file.deleted") {
		return VectorStoreFileDeleted{}, protocolError()
	}
	return value, err
}

// ListVectorStoreFiles returns one page without polling or fetching further pages.
func (c *Client) ListVectorStoreFiles(ctx context.Context, attempt Attempt, storeID string, options ListVectorStoreFilesOptions) (VectorStoreFilePage, error) {
	if err := vectorID("vector_store_id", storeID); err != nil {
		return VectorStoreFilePage{}, err
	}
	query, err := vectorFileQuery(options)
	if err != nil {
		return VectorStoreFilePage{}, err
	}
	page, err := decodeResource[struct {
		Data    []json.RawMessage `json:"data"`
		Object  string            `json:"object"`
		FirstID *string           `json:"first_id"`
		LastID  *string           `json:"last_id"`
		HasMore *bool             `json:"has_more"`
	}](c.resourceJSON(ctx, attempt, http.MethodGet, []string{"vector_stores", storeID, "files"}, query, nil))
	if err != nil {
		return VectorStoreFilePage{}, err
	}
	if page.Data == nil || page.Object != "list" || page.FirstID == nil || page.LastID == nil || page.HasMore == nil {
		return VectorStoreFilePage{}, protocolError()
	}
	files := make([]VectorStoreFile, 0, len(page.Data))
	for _, raw := range page.Data {
		file, err := decodeVectorStoreFile(raw, nil, storeID, "")
		if err != nil {
			return VectorStoreFilePage{}, err
		}
		files = append(files, file)
	}
	return VectorStoreFilePage{Data: files, FirstID: *page.FirstID, LastID: *page.LastID, HasMore: *page.HasMore}, nil
}

func vectorFileQuery(options ListVectorStoreFilesOptions) (url.Values, error) {
	query := make(url.Values)
	for _, field := range []struct{ name, value string }{{"after", options.After}, {"before", options.Before}} {
		if !utf8.ValidString(field.value) {
			return nil, &InputError{Field: field.name, Problem: "must be valid UTF-8"}
		}
		if field.value != "" {
			query.Set(field.name, field.value)
		}
	}
	limit, present := options.Limit.Value()
	if options.Limit.IsNull() || (present && (limit < 1 || limit > 100)) {
		return nil, &InputError{Field: "limit", Problem: "must be between 1 and 100"}
	}
	if present {
		query.Set("limit", strconv.FormatInt(limit, 10))
	}
	if options.Order != "" && options.Order != "asc" && options.Order != "desc" {
		return nil, &InputError{Field: "order", Problem: "expected asc or desc"}
	}
	if options.Order != "" {
		query.Set("order", options.Order)
	}
	if options.Filter != "" && !vectorFileStatus(options.Filter) {
		return nil, &InputError{Field: "filter", Problem: "unsupported file status"}
	}
	if options.Filter != "" {
		query.Set("filter", options.Filter)
	}
	return query, nil
}

func decodeVectorStore(body []byte, requestErr error, id string) (VectorStore, error) {
	value, err := decodeResource[VectorStore](body, requestErr)
	if err != nil {
		return value, err
	}
	if resourceRequired(body, "id", "object", "created_at", "name", "status", "usage_bytes", "file_counts") != nil || vectorID("id", value.ID) != nil || (id != "" && value.ID != id) || value.Object != "vector_store" || (value.Status != "expired" && value.Status != "in_progress" && value.Status != "completed") || value.LastActiveAt.IsZero() || value.Metadata.IsZero() {
		return VectorStore{}, protocolError()
	}
	if metadata, present := value.Metadata.Value(); present {
		var fields struct {
			Metadata map[string]json.RawMessage `json:"metadata"`
		}
		if json.Unmarshal(body, &fields) != nil {
			return VectorStore{}, protocolError()
		}
		for _, raw := range fields.Metadata {
			if text := bytes.TrimSpace(raw); len(text) == 0 || text[0] != '"' {
				return VectorStore{}, protocolError()
			}
		}
		if _, err := wire.EncodeMetadata(metadata); err != nil {
			return VectorStore{}, protocolError()
		}
	}
	if expiry, present := value.ExpiresAfter.Value(); value.ExpiresAfter.IsNull() || (present && expiry.Anchor != "last_active_at") {
		return VectorStore{}, protocolError()
	}
	return value, nil
}

func decodeVectorStoreFile(body []byte, requestErr error, storeID, fileID string) (VectorStoreFile, error) {
	value, err := decodeResource[VectorStoreFile](body, requestErr)
	if err == nil && (resourceRequired(body, "id", "object", "created_at", "vector_store_id", "status", "usage_bytes") != nil || !validVectorStoreFile(value, storeID, fileID)) {
		return VectorStoreFile{}, protocolError()
	}
	return value, err
}

func validVectorStoreFile(value VectorStoreFile, storeID, fileID string) bool {
	if vectorID("id", value.ID) != nil || (fileID != "" && value.ID != fileID) || value.VectorStoreID != storeID || value.Object != "vector_store.file" || !vectorFileStatus(value.Status) || value.LastError.IsZero() {
		return false
	}
	if last, present := value.LastError.Value(); present && last.Code != "server_error" && last.Code != "unsupported_file" && last.Code != "invalid_file" {
		return false
	}
	return vectorAttributes(value.Attributes) == nil && vectorChunking(value.ChunkingStrategy, true) == nil
}

func vectorChunking(option generation.Optional[VectorStoreChunking], returned bool) error {
	value, present := option.Value()
	if option.IsZero() {
		return nil
	}
	if !present {
		return &InputError{Field: "chunking_strategy", Problem: "must be an object"}
	}
	if (!returned && value.Type == "auto") || (returned && value.Type == "other") {
		if value.Static.IsZero() {
			return nil
		}
		return &InputError{Field: "chunking_strategy.static", Problem: "requires static chunking"}
	}
	chunk, present := value.Static.Value()
	if value.Type != "static" || !present {
		return &InputError{Field: "chunking_strategy", Problem: "unsupported chunking strategy"}
	}
	if chunk.MaxChunkSizeTokens < 100 || chunk.MaxChunkSizeTokens > 4096 || chunk.ChunkOverlapTokens < 0 || chunk.ChunkOverlapTokens > chunk.MaxChunkSizeTokens/2 {
		return &InputError{Field: "chunking_strategy.static", Problem: "invalid chunk size or overlap"}
	}
	return nil
}

func vectorAttributes(option generation.Optional[map[string]json.RawMessage]) error {
	attributes, _ := option.Value()
	if len(attributes) > 16 {
		return &InputError{Field: "attributes", Problem: "must have at most 16 entries"}
	}
	for key, raw := range attributes {
		if !utf8.ValidString(key) || utf8.RuneCountInString(key) > 64 || !utf8.Valid(raw) || !json.Valid(raw) {
			return &InputError{Field: "attributes", Problem: "expected UTF-8 keys and scalar JSON values"}
		}
		value := bytes.TrimSpace(raw)
		if value[0] == '"' {
			var text string
			if json.Unmarshal(value, &text) != nil || utf8.RuneCountInString(text) > 512 {
				return &InputError{Field: "attributes", Problem: "strings must have at most 512 characters"}
			}
		} else if value[0] == '{' || value[0] == '[' || bytes.Equal(value, []byte("null")) {
			return &InputError{Field: "attributes", Problem: "expected string, number, or boolean values"}
		}
	}
	return nil
}

func vectorID(field, value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
		return &InputError{Field: field, Problem: "must be nonblank UTF-8"}
	}
	return nil
}

func vectorFileIDs(storeID, fileID string) error {
	if err := vectorID("vector_store_id", storeID); err != nil {
		return err
	}
	return vectorID("file_id", fileID)
}

func vectorFileStatus(status string) bool {
	return status == "in_progress" || status == "completed" || status == "failed" || status == "cancelled"
}

func (value *VectorStoreStaticChunking) UnmarshalJSON(raw []byte) error {
	if err := resourceRequired(raw, "max_chunk_size_tokens", "chunk_overlap_tokens"); err != nil {
		return err
	}
	type plain VectorStoreStaticChunking
	return json.Unmarshal(raw, (*plain)(value))
}

func (value *VectorStoreFileCounts) UnmarshalJSON(raw []byte) error {
	if err := resourceRequired(raw, "cancelled", "completed", "failed", "in_progress", "total"); err != nil {
		return err
	}
	type plain VectorStoreFileCounts
	return json.Unmarshal(raw, (*plain)(value))
}

func (value *VectorStoreFileError) UnmarshalJSON(raw []byte) error {
	if err := resourceRequired(raw, "code", "message"); err != nil {
		return err
	}
	type plain VectorStoreFileError
	return json.Unmarshal(raw, (*plain)(value))
}

func (value *VectorStoreExpiry) UnmarshalJSON(raw []byte) error {
	if err := resourceRequired(raw, "anchor", "days"); err != nil {
		return err
	}
	type plain VectorStoreExpiry
	return json.Unmarshal(raw, (*plain)(value))
}
