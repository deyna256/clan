package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/usage"
)

// RequestRecord contains generation metadata only. Empty account and model mean unknown.
type RequestRecord struct {
	ID              string
	FinishedAt      time.Time
	KeyID           accesskey.ID
	AccountID       account.ID
	Model           string
	Result          string
	Duration        time.Duration
	ResponseStarted bool
	Usage           usage.Snapshot
}

// RequestFilter combines optional bounds and exact filters with AND.
// From is inclusive; To is exclusive. Nil bounds are unbounded.
type RequestFilter struct {
	From, To  *time.Time
	KeyID     accesskey.ID
	AccountID account.ID
	Model     string
	Result    string
}

// RequestPosition is the exclusive position in descending completion-time order.
type RequestPosition struct {
	FinishedAt int64
	ID         string
}

// UsageSummary contains the selected dimensions and independently known token sums.
type UsageSummary struct {
	KeyID        accesskey.ID
	AccountID    account.ID
	Model        string
	Day          string
	Requests     int64
	Results      map[string]int64
	Usage        usage.Snapshot
	UnknownUsage int64
}

// InsertRequest persists one final outcome; duplicate IDs fail without replacement.
func (s *Store) InsertRequest(ctx context.Context, r RequestRecord) error {
	if r.ID == "" || r.KeyID == "" || r.Result == "" || r.Duration < 0 {
		return ErrInvalid
	}
	for _, c := range []usage.Counter{r.Usage.Input, r.Usage.Output, r.Usage.Total} {
		if c.Tokens < 0 || (!c.Known && c.Tokens != 0) {
			return ErrInvalid
		}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO requests
		(id, finished_at, key_id, account_id, model, result, duration_ms, response_started,
		input_tokens, output_tokens, total_tokens) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.FinishedAt.UnixMilli(), r.KeyID, nullableText(string(r.AccountID)), nullableText(r.Model),
		r.Result, r.Duration.Milliseconds(), r.ResponseStarted,
		nullableCounter(r.Usage.Input), nullableCounter(r.Usage.Output), nullableCounter(r.Usage.Total))
	if err != nil {
		return classifyWriteError("inserting request", err)
	}
	return nil
}

func nullableText(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
func nullableCounter(c usage.Counter) sql.NullInt64 {
	return sql.NullInt64{Int64: c.Tokens, Valid: c.Known}
}

const requestColumns = `id, finished_at, key_id, account_id, model, result, duration_ms,
	response_started, input_tokens, output_tokens, total_tokens`

func scanRequest(scan func(...any) error) (RequestRecord, error) {
	var r RequestRecord
	var finished, duration int64
	var accountID, model sql.NullString
	var input, output, total sql.NullInt64
	err := scan(&r.ID, &finished, &r.KeyID, &accountID, &model, &r.Result, &duration,
		&r.ResponseStarted, &input, &output, &total)
	r.FinishedAt = time.UnixMilli(finished).UTC()
	r.Duration = time.Duration(duration) * time.Millisecond
	r.AccountID, r.Model = account.ID(accountID.String), model.String
	r.Usage = counters(input, output, total)
	return r, err
}

func counters(input, output, total sql.NullInt64) usage.Snapshot {
	return usage.Snapshot{
		Input:  usage.Counter{Tokens: input.Int64, Known: input.Valid},
		Output: usage.Counter{Tokens: output.Int64, Known: output.Valid},
		Total:  usage.Counter{Tokens: total.Int64, Known: total.Valid},
	}
}

// GetRequest returns a retained record or ErrNotFound.
func (s *Store) GetRequest(ctx context.Context, id string) (RequestRecord, error) {
	r, err := scanRequest(s.readDB.QueryRowContext(ctx,
		`SELECT `+requestColumns+` FROM requests WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestRecord{}, ErrNotFound
	}
	return r, err
}

// ListRequests reads up to limit rows after a position. Callers may request one extra row.
func (s *Store) ListRequests(ctx context.Context, f RequestFilter, after *RequestPosition, limit int) ([]RequestRecord, error) {
	if limit < 1 || limit > 101 {
		return nil, ErrInvalid
	}
	where, args, err := requestWhere(f)
	if err != nil {
		return nil, err
	}
	if after != nil {
		where += ` AND (finished_at, id) < (?, ?)`
		args = append(args, after.FinishedAt, after.ID)
	}
	args = append(args, limit)
	rows, err := s.readDB.QueryContext(ctx, `SELECT `+requestColumns+` FROM requests WHERE `+
		where+` ORDER BY finished_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RequestRecord, 0)
	for rows.Next() {
		r, err := scanRequest(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

func requestWhere(f RequestFilter) (string, []any, error) {
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return "", nil, ErrInvalid
	}
	where := []string{"1=1"}
	var args []any
	if f.From != nil {
		where, args = append(where, "finished_at >= ?"), append(args, ceilMillis(*f.From))
	}
	if f.To != nil {
		where, args = append(where, "finished_at < ?"), append(args, ceilMillis(*f.To))
	}
	for _, filter := range []struct{ column, value string }{
		{"key_id", string(f.KeyID)}, {"account_id", string(f.AccountID)}, {"model", f.Model}, {"result", f.Result},
	} {
		if filter.value != "" {
			where, args = append(where, filter.column+" = ?"), append(args, filter.value)
		}
	}
	return strings.Join(where, " AND "), args, nil
}

func ceilMillis(t time.Time) int64 {
	n := t.UnixMilli()
	if t.Nanosecond()%int(time.Millisecond) != 0 {
		n++
	}
	return n
}

// SummarizeUsage reads all metrics from one SQLite snapshot, grouped by allowed dimensions.
func (s *Store) SummarizeUsage(ctx context.Context, f RequestFilter, groups []string) ([]UsageSummary, error) {
	columns := []string{"NULL", "NULL", "NULL", "NULL"}
	seen := make(map[string]bool)
	if len(groups) == 0 {
		return nil, ErrInvalid
	}
	for _, group := range groups {
		if seen[group] {
			return nil, ErrInvalid
		}
		seen[group] = true
		switch group {
		case "key":
			columns[0] = "key_id"
		case "account":
			columns[1] = "account_id"
		case "model":
			columns[2] = "model"
		case "day":
			columns[3] = "strftime('%Y-%m-%d', finished_at / 1000.0, 'unixepoch')"
		default:
			return nil, ErrInvalid
		}
	}
	where, args, err := requestWhere(f)
	if err != nil {
		return nil, err
	}
	query := `SELECT k, a, m, d, SUM(n), json_group_object(result, n),
		SUM(i), SUM(o), SUM(t), SUM(u) FROM (SELECT ` +
		columns[0] + ` AS k, ` + columns[1] + ` AS a, ` + columns[2] + ` AS m, ` + columns[3] + ` AS d,
		result, COUNT(*) AS n, SUM(input_tokens) AS i, SUM(output_tokens) AS o, SUM(total_tokens) AS t,
		SUM(CASE WHEN input_tokens IS NULL OR output_tokens IS NULL OR total_tokens IS NULL THEN 1 ELSE 0 END) AS u
		FROM requests WHERE ` + where + ` GROUP BY 1,2,3,4,5) GROUP BY 1,2,3,4 ORDER BY 1,2,3,4`
	rows, err := s.readDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UsageSummary, 0)
	for rows.Next() {
		var item UsageSummary
		var key, acct, model, day sql.NullString
		var input, output, total sql.NullInt64
		var results string
		if err := rows.Scan(&key, &acct, &model, &day, &item.Requests, &results,
			&input, &output, &total, &item.UnknownUsage); err != nil {
			return nil, err
		}
		item.KeyID, item.AccountID = accesskey.ID(key.String), account.ID(acct.String)
		item.Model, item.Day, item.Usage = model.String, day.String, counters(input, output, total)
		if err := json.Unmarshal([]byte(results), &item.Results); err != nil {
			return nil, fmt.Errorf("storage: reading result counts: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DeleteRequestsBefore removes at most 1,000 records older than before.
func (s *Store) DeleteRequestsBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE rowid IN
		(SELECT rowid FROM requests WHERE finished_at < ? LIMIT 1000)`, ceilMillis(before))
	if err != nil {
		return 0, fmt.Errorf("storage: deleting expired requests: %w", err)
	}
	return result.RowsAffected()
}
