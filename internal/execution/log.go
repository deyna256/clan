package execution

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/storage"
	"github.com/deyna256/clan/internal/usage"
)

// RecordRejection finalizes a generation refused before admission, after HTTP cleanup.
// keyID must come from successful initial authentication, even if later revoked.
// This remains available to draining HTTP handlers after Close.
func (e *Executor) RecordRejection(ctx context.Context, info RequestInfo, keyID accesskey.ID, model, outcome string, started bool) {
	finished := time.Now()
	e.recordResult(ctx, storage.RequestRecord{ID: info.ID, FinishedAt: finished, KeyID: keyID,
		Model: model, Result: outcome, Duration: finished.Sub(info.Started), ResponseStarted: started})
}

func resultCode(result codex.Result, err error) string {
	outcome := errorCode(err)
	if err == nil && result.Status != "" {
		outcome = result.Status
	}
	return outcome
}

func (e *Executor) recordResult(ctx context.Context, r storage.RequestRecord) {
	attrs := []slog.Attr{slog.String("request_id", r.ID), slog.String("key_id", string(r.KeyID)),
		slog.Int64("duration_ms", r.Duration.Milliseconds()), slog.String("result", r.Result),
		slog.Bool("response_started", r.ResponseStarted), slog.Time("finished_at", r.FinishedAt)}
	if r.Model != "" {
		attrs = append(attrs, slog.String("model", r.Model))
	}
	if r.AccountID != "" {
		attrs = append(attrs, slog.String("account_id", string(r.AccountID)))
	}
	for _, counter := range []struct {
		name  string
		value usage.Counter
	}{
		{"input_tokens", r.Usage.Input}, {"output_tokens", r.Usage.Output}, {"total_tokens", r.Usage.Total},
	} {
		if counter.value.Known {
			attrs = append(attrs, slog.Int64(counter.name, counter.value.Tokens))
		}
	}
	e.logger.LogAttrs(ctx, slog.LevelInfo, "request finished", attrs...)
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := e.store.InsertRequest(writeCtx, r); err != nil {
		e.logger.WarnContext(writeCtx, "request accounting failed", "request_id", r.ID)
	}
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
