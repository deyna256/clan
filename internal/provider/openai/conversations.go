package openai

import (
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

type Conversation struct {
	ID        string
	CreatedAt int64
	Metadata  json.RawMessage
}
type ConversationDeleted struct {
	ID      string
	Deleted bool
}
type ConversationCreateOptions struct {
	Items    generation.Optional[[]generation.Item]
	Metadata generation.Optional[map[string]string]
}
type ConversationUpdateOptions struct {
	Metadata generation.Optional[map[string]string]
}
type ConversationItemsCreateOptions struct {
	Items   []generation.Item
	Include []string
}
type ItemListOptions struct {
	After   string
	Limit   generation.Optional[int64]
	Order   string
	Include []string
}

// ItemPage is one resource page; callers decide whether to request the next page.
type ItemPage struct {
	Data            []generation.Item
	FirstID, LastID string
	HasMore         bool
}

func (c *Client) CreateConversation(ctx context.Context, attempt Attempt, options ConversationCreateOptions) (Conversation, error) {
	var items json.RawMessage
	if options.Items.IsNull() {
		items = json.RawMessage(`null`)
	}
	if values, ok := options.Items.Value(); ok {
		var err error
		items, err = conversationInput(values)
		if err != nil {
			return Conversation{}, err
		}
	}
	metadata, err := conversationMetadata(options.Metadata)
	if err != nil {
		return Conversation{}, err
	}
	payload := struct {
		Items    json.RawMessage `json:"items,omitempty"`
		Metadata json.RawMessage `json:"metadata,omitempty"`
	}{items, metadata}
	body, err := c.resourceJSON(ctx, attempt, http.MethodPost, []string{"conversations"}, nil, payload)
	return decodeConversation(body, err, "")
}

func (c *Client) RetrieveConversation(ctx context.Context, attempt Attempt, id string) (Conversation, error) {
	if err := conversationID("conversation_id", id); err != nil {
		return Conversation{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodGet, []string{"conversations", id}, nil, nil)
	return decodeConversation(body, err, id)
}

func (c *Client) UpdateConversation(ctx context.Context, attempt Attempt, id string, options ConversationUpdateOptions) (Conversation, error) {
	if err := conversationID("conversation_id", id); err != nil {
		return Conversation{}, err
	}
	if options.Metadata.IsZero() {
		return Conversation{}, &InputError{Field: "metadata", Problem: "must be present"}
	}
	metadata, err := conversationMetadata(options.Metadata)
	if err != nil {
		return Conversation{}, err
	}
	payload := struct {
		Metadata json.RawMessage `json:"metadata"`
	}{metadata}
	body, err := c.resourceJSON(ctx, attempt, http.MethodPost, []string{"conversations", id}, nil, payload)
	return decodeConversation(body, err, id)
}

func (c *Client) DeleteConversation(ctx context.Context, attempt Attempt, id string) (ConversationDeleted, error) {
	if err := conversationID("conversation_id", id); err != nil {
		return ConversationDeleted{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodDelete, []string{"conversations", id}, nil, nil)
	value, err := decodeResource[struct {
		ID      string `json:"id"`
		Deleted *bool  `json:"deleted"`
		Object  string `json:"object"`
	}](body, err)
	if err != nil {
		return ConversationDeleted{}, err
	}
	if value.ID != id || value.Deleted == nil || value.Object != "conversation.deleted" {
		return ConversationDeleted{}, protocolError()
	}
	return ConversationDeleted{ID: value.ID, Deleted: *value.Deleted}, nil
}

func (c *Client) AddConversationItems(ctx context.Context, attempt Attempt, id string, options ConversationItemsCreateOptions) (ItemPage, error) {
	if err := conversationID("conversation_id", id); err != nil {
		return ItemPage{}, err
	}
	items, err := conversationInput(options.Items)
	if err != nil {
		return ItemPage{}, err
	}
	query, err := conversationIncludes(options.Include)
	if err != nil {
		return ItemPage{}, err
	}
	payload := struct {
		Items json.RawMessage `json:"items"`
	}{items}
	body, err := c.resourceJSON(ctx, attempt, http.MethodPost, []string{"conversations", id, "items"}, query, payload)
	diagnostics := c.diagnostics(attempt)
	defer diagnostics.finish()
	return decodeItemPage(body, err, diagnostics.report)
}

func (c *Client) ListConversationItems(ctx context.Context, attempt Attempt, id string, options ItemListOptions) (ItemPage, error) {
	if err := conversationID("conversation_id", id); err != nil {
		return ItemPage{}, err
	}
	query, err := options.query()
	if err != nil {
		return ItemPage{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodGet, []string{"conversations", id, "items"}, query, nil)
	diagnostics := c.diagnostics(attempt)
	defer diagnostics.finish()
	return decodeItemPage(body, err, diagnostics.report)
}

func (options ItemListOptions) query() (url.Values, error) {
	query, err := conversationIncludes(options.Include)
	if err != nil {
		return nil, err
	}
	if options.After != "" {
		if err := conversationID("after", options.After); err != nil {
			return nil, err
		}
		query.Set("after", options.After)
	}
	if options.Limit.IsNull() {
		return nil, &InputError{Field: "limit", Problem: "must not be null"}
	}
	if limit, ok := options.Limit.Value(); ok {
		if limit < 1 || limit > 100 {
			return nil, &InputError{Field: "limit", Problem: "must be between 1 and 100"}
		}
		query.Set("limit", strconv.FormatInt(limit, 10))
	}
	if options.Order != "" {
		if options.Order != "asc" && options.Order != "desc" {
			return nil, &InputError{Field: "order", Problem: "expected asc or desc"}
		}
		query.Set("order", options.Order)
	}
	return query, nil
}

func (c *Client) RetrieveConversationItem(ctx context.Context, attempt Attempt, conversationIDValue, itemID string, include []string) (generation.Item, error) {
	if err := conversationID("conversation_id", conversationIDValue); err != nil {
		return nil, err
	}
	if err := conversationID("item_id", itemID); err != nil {
		return nil, err
	}
	query, err := conversationIncludes(include)
	if err != nil {
		return nil, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodGet, []string{"conversations", conversationIDValue, "items", itemID}, query, nil)
	if err != nil {
		return nil, err
	}
	diagnostics := c.diagnostics(attempt)
	defer diagnostics.finish()
	item, err := wire.DecodeStoredItem(body, diagnostics.report)
	if err != nil {
		return nil, err
	}
	if storedItemID(item) != itemID {
		return nil, protocolError()
	}
	return item, nil
}

func (c *Client) DeleteConversationItem(ctx context.Context, attempt Attempt, conversationIDValue, itemID string) (Conversation, error) {
	if err := conversationID("conversation_id", conversationIDValue); err != nil {
		return Conversation{}, err
	}
	if err := conversationID("item_id", itemID); err != nil {
		return Conversation{}, err
	}
	body, err := c.resourceJSON(ctx, attempt, http.MethodDelete, []string{"conversations", conversationIDValue, "items", itemID}, nil, nil)
	return decodeConversation(body, err, conversationIDValue)
}

func conversationInput(items []generation.Item) (json.RawMessage, error) {
	if len(items) > 20 {
		return nil, &InputError{Field: "items", Problem: "at most 20 items are allowed"}
	}
	encoded, err := wire.EncodeInput(items)
	if err != nil {
		return nil, err
	}
	return json.Marshal(encoded)
}

func conversationMetadata(metadata generation.Optional[map[string]string]) (json.RawMessage, error) {
	if metadata.IsZero() {
		return nil, nil
	}
	if metadata.IsNull() {
		return json.RawMessage(`null`), nil
	}
	value, _ := metadata.Value()
	encoded, err := wire.EncodeMetadata(value)
	if err != nil {
		return nil, &InputError{Field: "metadata", Problem: "expected at most 16 UTF-8 entries with keys up to 64 and values up to 512 characters"}
	}
	return encoded, nil
}

func conversationIncludes(includes []string) (url.Values, error) {
	query := make(url.Values)
	for _, include := range includes {
		if !utf8.ValidString(include) || strings.TrimSpace(include) == "" {
			return nil, &InputError{Field: "include", Problem: "must contain nonempty UTF-8 strings"}
		}
		query.Add("include[]", include)
	}
	return query, nil
}

func conversationID(field, id string) error {
	if !utf8.ValidString(id) || strings.TrimSpace(id) == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\r\n") {
		return &InputError{Field: field, Problem: "must be a valid resource ID"}
	}
	return nil
}

func decodeConversation(body []byte, requestErr error, expectedID string) (Conversation, error) {
	value, err := decodeResource[struct {
		ID        string          `json:"id"`
		CreatedAt *int64          `json:"created_at"`
		Metadata  json.RawMessage `json:"metadata"`
		Object    string          `json:"object"`
	}](body, requestErr)
	if err != nil {
		return Conversation{}, err
	}
	if value.ID == "" || (expectedID != "" && value.ID != expectedID) || value.CreatedAt == nil || value.Metadata == nil || value.Object != "conversation" {
		return Conversation{}, protocolError()
	}
	return Conversation{ID: value.ID, CreatedAt: *value.CreatedAt, Metadata: value.Metadata}, nil
}

func decodeItemPage(body []byte, requestErr error, warn func(string)) (ItemPage, error) {
	value, err := decodeResource[struct {
		Data    []json.RawMessage `json:"data"`
		FirstID *string           `json:"first_id"`
		LastID  *string           `json:"last_id"`
		HasMore *bool             `json:"has_more"`
		Object  string            `json:"object"`
	}](body, requestErr)
	if err != nil {
		return ItemPage{}, err
	}
	if value.Data == nil || value.FirstID == nil || value.LastID == nil || value.HasMore == nil || value.Object != "list" {
		return ItemPage{}, protocolError()
	}
	items := make([]generation.Item, 0, len(value.Data))
	for _, raw := range value.Data {
		item, err := wire.DecodeStoredItem(raw, warn)
		if err != nil {
			return ItemPage{}, err
		}
		items = append(items, item)
	}
	return ItemPage{Data: items, FirstID: *value.FirstID, LastID: *value.LastID, HasMore: *value.HasMore}, nil
}

func storedItemID(item generation.Item) string {
	if configuration, ok := item.(generation.OpenAIConfigurationUpdate); ok {
		id, _ := configuration.ID.Value()
		return id
	}
	return itemID(item)
}
