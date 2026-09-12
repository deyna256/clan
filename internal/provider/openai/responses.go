package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/transport"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/deyna256/clan/internal/usage"
)

type ResponseRetrieveOptions struct {
	Include            []string
	IncludeObfuscation generation.Optional[bool]
}

// StoredResponse describes provider state, including unfinished or failed work.
// Its usage is historical and must not be charged again on retrieval.
type StoredResponse struct {
	ID, Model, Status string
	CreatedAt         int64
	Output            []generation.Item
	Usage             usage.Snapshot
	Error             generation.Optional[StoredResponseError]
	IncompleteReason  generation.Optional[string]
}

type StoredResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) RetrieveResponse(ctx context.Context, attempt Attempt, id string, options ResponseRetrieveOptions) (StoredResponse, error) {
	query, err := responseQuery(options)
	if err != nil {
		return StoredResponse{}, err
	}
	body, requestErr := c.resourceJSON(ctx, attempt, http.MethodGet, []string{"responses", id}, query, nil)
	r, decodeErr := decodeResource[struct {
		wire.ResponseFields
		Object     string          `json:"object"`
		CreatedAt  *int64          `json:"created_at"`
		Error      json.RawMessage `json:"error"`
		Incomplete json.RawMessage `json:"incomplete_details"`
	}](body, nil)
	diagnostics := c.diagnostics(attempt)
	defer diagnostics.finish()
	state, invalid := wire.NormalizeUsage(r.Usage, usage.State{})
	result := StoredResponse{Usage: state.Usage}
	if invalid {
		diagnostics.report("invalid_usage")
	}
	if requestErr != nil {
		return result, requestErr
	}
	if decodeErr != nil {
		return result, decodeErr
	}
	if r.ID != id || r.Object != "response" || r.Model == "" || r.CreatedAt == nil || r.Output == nil {
		return result, protocolError()
	}
	switch r.Status {
	case "completed", "incomplete", "failed", "in_progress", "queued", "cancelled":
	default:
		return result, protocolError()
	}
	result.ID, result.Model, result.Status, result.CreatedAt = r.ID, r.Model, r.Status, *r.CreatedAt
	if len(r.Error) > 0 {
		if string(r.Error) == "null" {
			result.Error = generation.Null[StoredResponseError]()
		} else {
			if err := resourceRequired(r.Error, "code", "message"); err != nil {
				return result, err
			}
			providerError, err := decodeResource[StoredResponseError](r.Error, nil)
			if err != nil {
				return result, err
			}
			result.Error = generation.Some(providerError)
		}
	}
	var incomplete generation.Optional[struct {
		Reason generation.Optional[string] `json:"reason"`
	}]
	if len(r.Incomplete) > 0 && json.Unmarshal(r.Incomplete, &incomplete) != nil {
		return result, protocolError()
	}
	if reason, present := incomplete.Value(); present {
		if reason.Reason.IsNull() {
			return result, protocolError()
		}
		result.IncompleteReason = reason.Reason
	} else if incomplete.IsNull() {
		result.IncompleteReason = generation.Null[string]()
	}
	output := make([]generation.Item, 0, len(r.Output))
	if r.Status == "completed" || r.Status == "incomplete" {
		terminal, _, err := r.TerminalOutput(diagnostics.report)
		if err != nil {
			return result, err
		}
		for _, item := range terminal {
			if err := validateStateItemCompletion(item); err != nil {
				return result, err
			}
			if err := validateImageCompletion(item); err != nil {
				return result, err
			}
		}
		result.Output = terminal
		return result, nil
	}
	for _, raw := range r.Output {
		item, err := wire.DecodeItem(raw, false, diagnostics.report)
		if err != nil {
			return result, err
		}
		output = append(output, item)
	}
	result.Output = output
	return result, nil
}

// RetrieveResponseStream reads stored events from the beginning. It neither
// creates a generation nor resumes provider background work. Its usage is
// historical and must not be charged again.
func (c *Client) RetrieveResponseStream(ctx context.Context, attempt Attempt, id string, options ResponseRetrieveOptions) (*Stream, error) {
	query, err := responseQuery(options)
	if err != nil {
		return nil, err
	}
	query.Set("stream", "true")
	streamCtx, cancel := context.WithCancel(ctx)
	response, err := c.transport.Do(streamCtx, attempt.Credentials.Key, transport.Request{Method: http.MethodGet, Path: []string{"responses", id}, Query: query, Accept: "text/event-stream"})
	stream, err := c.openStream(streamCtx, cancel, attempt, response, err)
	if err != nil {
		return nil, err
	}
	stream.state.expectedID = id
	return stream, nil
}

func responseQuery(options ResponseRetrieveOptions) (url.Values, error) {
	query, err := conversationIncludes(options.Include)
	if err != nil {
		return nil, err
	}
	if options.IncludeObfuscation.IsNull() {
		return nil, &InputError{Field: "include_obfuscation", Problem: "must be a boolean"}
	}
	if value, present := options.IncludeObfuscation.Value(); present {
		query.Set("include_obfuscation", strconv.FormatBool(value))
	}
	return query, nil
}

// DeleteResponse also accepts an empty successful body, as the official SDK does.
func (c *Client) DeleteResponse(ctx context.Context, attempt Attempt, id string) error {
	body, err := c.resourceJSON(ctx, attempt, http.MethodDelete, []string{"responses", id}, nil, nil)
	if err != nil || len(body) == 0 {
		return err
	}
	deleted, err := decodeResource[struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Deleted bool   `json:"deleted"`
	}](body, nil)
	if err != nil {
		return err
	}
	if deleted.ID != id || deleted.Object != "response" || !deleted.Deleted {
		return protocolError()
	}
	return nil
}

type CompactionResult struct {
	ID        string
	CreatedAt int64
	Output    []generation.Item
	Usage     usage.Snapshot
}

func (c *Client) ListResponseInputItems(ctx context.Context, attempt Attempt, id string, options ItemListOptions) (ItemPage, error) {
	query, err := options.query()
	if err != nil {
		return ItemPage{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodGet, []string{"responses", id, "input_items"}, query, nil)
	diagnostics := c.diagnostics(attempt)
	defer diagnostics.finish()
	return decodeItemPage(body, err, diagnostics.report)
}

func (c *Client) Compact(ctx context.Context, attempt Attempt, request generation.CompactRequest) (CompactionResult, error) {
	result := CompactionResult{}
	payload, err := wire.EncodeCompact(request)
	if err != nil {
		return result, err
	}
	body, requestErr := c.resourceJSON(ctx, attempt, http.MethodPost, []string{"responses", "compact"}, nil, json.RawMessage(payload))
	r, decodeErr := decodeResource[struct {
		wire.ResponseFields
		CreatedAt *int64 `json:"created_at"`
		Object    string `json:"object"`
	}](body, nil)
	diagnostics := c.diagnostics(attempt)
	defer diagnostics.finish()
	state, invalid := wire.NormalizeUsage(r.Usage, usage.State{})
	result.Usage = state.Usage
	if invalid {
		diagnostics.report("invalid_usage")
	}
	if requestErr != nil {
		return result, requestErr
	}
	if decodeErr != nil {
		return result, decodeErr
	}
	if r.ID == "" || r.CreatedAt == nil || r.Object != "response.compaction" || r.Output == nil {
		return result, protocolError()
	}
	result.ID, result.CreatedAt = r.ID, *r.CreatedAt
	output := make([]generation.Item, 0, len(r.Output))
	for _, raw := range r.Output {
		item, err := wire.DecodeStoredItem(raw, diagnostics.report)
		if err != nil {
			return result, err
		}
		output = append(output, item)
	}
	result.Output = output
	return result, nil
}

func (c *Client) CountInputTokens(ctx context.Context, attempt Attempt, request generation.InputTokenRequest) (int64, error) {
	payload, err := wire.EncodeInputTokenRequest(request)
	if err != nil {
		return 0, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodPost, []string{"responses", "input_tokens"}, nil, json.RawMessage(payload))
	count, err := decodeResource[struct {
		Object string `json:"object"`
		Count  *int64 `json:"input_tokens"`
	}](body, err)
	if err != nil {
		return 0, err
	}
	if count.Object != "response.input_tokens" || count.Count == nil || *count.Count < 0 {
		return 0, protocolError()
	}
	return *count.Count, nil
}
