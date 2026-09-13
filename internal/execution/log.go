package execution

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/usage"
)

func (e *Executor) logResult(ctx context.Context, id string, key accesskey.ID, model string, duration time.Duration, result codex.Result, err error, responseStarted bool) {
	outcome := errorCode(err)
	if err == nil {
		var terminal struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(result.Response, &terminal) == nil && terminal.Status != "" {
			outcome = terminal.Status
		}
	}
	attrs := []slog.Attr{slog.String("request_id", id), slog.String("key_id", string(key)),
		slog.String("model", model), slog.Int64("duration_ms", duration.Milliseconds()), slog.String("result", outcome),
		slog.Bool("response_started", responseStarted)}
	for _, counter := range []struct {
		name  string
		value usage.Counter
	}{
		{"input_tokens", result.Usage.Input}, {"output_tokens", result.Usage.Output}, {"total_tokens", result.Usage.Total},
	} {
		if counter.value.Known {
			attrs = append(attrs, slog.Int64(counter.name, counter.value.Tokens))
		}
	}
	e.logger.LogAttrs(ctx, slog.LevelInfo, "request finished", attrs...)
}

func errorCode(err error) string {
	if err == nil {
		return "completed"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, concurrency.ErrLimitReached):
		return "concurrency_limit"
	case errors.Is(err, ErrNoAccounts):
		return "no_accounts"
	case errors.Is(err, ErrClosed):
		return "closed"
	case errors.Is(err, ErrDelivery):
		return "delivery_failed"
	}
	var failure *codex.Failure
	if errors.As(err, &failure) {
		return string(failure.Category)
	}
	var validation *codex.ValidationError
	if errors.As(err, &validation) {
		return "invalid_request"
	}
	return "unavailable"
}
