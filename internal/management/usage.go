package management

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/storage"
	"github.com/deyna256/clan/internal/usage"
)

// UsageFilterInput is embedded with an exported name so Huma discovers its query fields.
type UsageFilterInput struct {
	From      string `query:"from"`
	To        string `query:"to"`
	KeyID     string `query:"key_id"`
	AccountID string `query:"account_id"`
	Model     string `query:"model"`
}

type usageInput struct {
	UsageFilterInput
	GroupBy []string `query:"group_by" default:"key" enum:"key,account,model,day" uniqueItems:"true"`
}

type requestsInput struct {
	UsageFilterInput
	Result string `query:"result"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"100"`
	Cursor string `query:"cursor" maxLength:"2048"`
}

// TokenView omits unknown counters and preserves known zeros in JSON.
type TokenView struct {
	Input  *int64 `json:"input_tokens,omitempty"`
	Output *int64 `json:"output_tokens,omitempty"`
	Total  *int64 `json:"total_tokens,omitempty"`
}

func tokensView(u usage.Snapshot) TokenView {
	var view TokenView
	if u.Input.Known {
		view.Input = &u.Input.Tokens
	}
	if u.Output.Known {
		view.Output = &u.Output.Tokens
	}
	if u.Total.Known {
		view.Total = &u.Total.Tokens
	}
	return view
}

type requestView struct {
	ID              string    `json:"id"`
	FinishedAt      time.Time `json:"finished_at"`
	KeyID           string    `json:"key_id"`
	AccountID       string    `json:"account_id,omitempty"`
	Model           string    `json:"model,omitempty"`
	Result          string    `json:"result"`
	DurationMS      int64     `json:"duration_ms"`
	ResponseStarted bool      `json:"response_started"`
	TokenView
}

func recordView(r storage.RequestRecord) requestView {
	return requestView{ID: r.ID, FinishedAt: r.FinishedAt, KeyID: string(r.KeyID),
		AccountID: string(r.AccountID), Model: r.Model, Result: r.Result,
		DurationMS: r.Duration.Milliseconds(), ResponseStarted: r.ResponseStarted, TokenView: tokensView(r.Usage)}
}

type summaryView struct {
	KeyID *string `json:"key_id,omitempty"`
	// The outer pointer selects the dimension; the inner pointer represents SQL NULL.
	AccountID    **string         `json:"account_id,omitempty" nullable:"true"`
	Model        **string         `json:"model,omitempty" nullable:"true"`
	Day          *string          `json:"day,omitempty"`
	Requests     int64            `json:"requests"`
	Results      map[string]int64 `json:"results"`
	UnknownUsage int64            `json:"unknown_usage"`
	TokenView
}

type usageOutput struct {
	Body struct {
		From  time.Time     `json:"from"`
		To    time.Time     `json:"to"`
		Items []summaryView `json:"items"`
	}
}

func (s *service) summarizeUsage(ctx context.Context, input *usageInput) (*usageOutput, error) {
	f, err := parseUsageFilter(input.UsageFilterInput)
	if err != nil {
		return nil, err
	}
	if f.To == nil {
		now := time.Now().UTC()
		f.To = &now
	}
	if f.From == nil {
		from := f.To.Add(-30 * 24 * time.Hour)
		f.From = &from
	}
	if f.From.Year() < 0 || !f.From.Before(*f.To) {
		return nil, invalidUsageQuery()
	}
	rows, err := s.store.SummarizeUsage(ctx, f, input.GroupBy)
	if err != nil {
		return nil, s.failure(ctx, err, false)
	}
	output := &usageOutput{}
	output.Body.From, output.Body.To = *f.From, *f.To
	output.Body.Items = make([]summaryView, 0, len(rows))
	for _, row := range rows {
		item := summaryView{Requests: row.Requests, Results: row.Results,
			UnknownUsage: row.UnknownUsage, TokenView: tokensView(row.Usage)}
		for _, group := range input.GroupBy {
			switch group {
			case "key":
				key := string(row.KeyID)
				item.KeyID = &key
			case "account":
				value := optionalDimension(string(row.AccountID))
				item.AccountID = &value
			case "model":
				value := optionalDimension(row.Model)
				item.Model = &value
			case "day":
				item.Day = &row.Day
			}
		}
		output.Body.Items = append(output.Body.Items, item)
	}
	return output, nil
}

func optionalDimension(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func parseUsageFilter(input UsageFilterInput) (storage.RequestFilter, error) {
	f := storage.RequestFilter{KeyID: accesskey.ID(input.KeyID), AccountID: account.ID(input.AccountID), Model: input.Model}
	for _, bound := range []struct {
		raw    string
		target **time.Time
	}{{input.From, &f.From}, {input.To, &f.To}} {
		if bound.raw == "" {
			continue
		}
		value, err := time.Parse(time.RFC3339Nano, bound.raw)
		if err != nil {
			return f, invalidUsageQuery()
		}
		value = value.UTC()
		if value.Year() < 0 || value.Year() > 9999 {
			return f, invalidUsageQuery()
		}
		*bound.target = &value
	}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return f, invalidUsageQuery()
	}
	return f, nil
}

func invalidUsageQuery() error {
	return problem(422, "invalid-request", "The request does not match the required fields or allowed values.")
}

type requestsOutput struct {
	Body struct {
		Items      []requestView `json:"items"`
		NextCursor string        `json:"next_cursor,omitempty"`
	}
}

type requestCursor struct {
	Time   *int64 `json:"t"`
	ID     string `json:"id"`
	Filter string `json:"filter"`
}

func filterHash(f storage.RequestFilter) (string, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func decodeRequestCursor(raw, hash string) (*storage.RequestPosition, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 2048 {
		return nil, invalidUsageQuery()
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, invalidUsageQuery()
	}
	var cursor requestCursor
	if err := decodeJSON(data, &cursor); err != nil {
		return nil, invalidUsageQuery()
	}
	if cursor.Time == nil || cursor.ID == "" || len(cursor.ID) > 128 || cursor.Filter != hash {
		return nil, invalidUsageQuery()
	}
	return &storage.RequestPosition{FinishedAt: *cursor.Time, ID: cursor.ID}, nil
}

func (s *service) listRequests(ctx context.Context, input *requestsInput) (*requestsOutput, error) {
	f, err := parseUsageFilter(input.UsageFilterInput)
	if err != nil {
		return nil, err
	}
	f.Result = input.Result
	hash, err := filterHash(f)
	if err != nil {
		return nil, invalidUsageQuery()
	}
	after, err := decodeRequestCursor(input.Cursor, hash)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListRequests(ctx, f, after, input.Limit+1)
	if err != nil {
		return nil, s.failure(ctx, err, false)
	}
	output := &requestsOutput{}
	if len(rows) > input.Limit {
		rows = rows[:input.Limit]
		last := rows[len(rows)-1]
		stamp := last.FinishedAt.UnixMilli()
		raw, err := json.Marshal(requestCursor{Time: &stamp, ID: last.ID, Filter: hash})
		if err != nil {
			return nil, s.failure(ctx, err, false)
		}
		output.Body.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	output.Body.Items = make([]requestView, 0, len(rows))
	for _, row := range rows {
		output.Body.Items = append(output.Body.Items, recordView(row))
	}
	return output, nil
}

type requestOutput struct{ Body requestView }

func (s *service) getRequest(ctx context.Context, input *resourceInput) (*requestOutput, error) {
	row, err := s.store.GetRequest(ctx, input.ID)
	if err != nil {
		return nil, s.failure(ctx, err, false)
	}
	return &requestOutput{Body: recordView(row)}, nil
}
