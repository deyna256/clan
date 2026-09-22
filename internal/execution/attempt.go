package execution

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/retry"
	"github.com/deyna256/clan/internal/storage"
)

type candidate struct {
	id       account.ID
	identity string
}

func (e *Executor) open(r *requestState, input codex.Request) (*codex.Stream, error) {
	snapshot, err := e.snapshot(r.ctx)
	if err != nil {
		return nil, err
	}
	candidates, err := requestCandidates(snapshot, input)
	if err != nil {
		return nil, err
	}
	tried := make(map[account.ID]bool)
	var lastErr error
	var previous account.ID
	for range 3 {
		selected, ok := e.selectAccount(input.Model(), candidates, tried)
		if !ok {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, ErrNoAccounts
		}
		e.mu.Lock()
		r.lastAttemptAccountID = selected.id
		e.mu.Unlock()
		tried[selected.id] = true
		if previous != "" {
			e.logger.LogAttrs(r.ctx, slog.LevelInfo, "trying next account", slog.String("request_id", r.id),
				slog.String("key_id", string(r.keyID)), slog.String("model", r.model),
				slog.String("previous_account_id", string(previous)), slog.String("account_id", string(selected.id)))
		}
		previous = selected.id
		a, err := e.prepare(r, selected)
		if err == nil {
			var stream *codex.Stream
			stream, err = e.client.Stream(r.ctx, a, input)
			if err == nil {
				return stream, nil
			}
		}
		e.noteFailure(r, selected.id, err)
		if r.ctx.Err() != nil {
			return nil, r.ctx.Err()
		}
		if !safeRetry(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func requestCandidates(snapshot codex.CatalogSnapshot, input codex.Request) ([]candidate, error) {
	var candidates []candidate
	var validationErr error = &codex.ValidationError{Field: "model"}
	for _, catalog := range snapshot.Accounts {
		for _, model := range catalog.Models {
			if model.ID != input.Model() {
				continue
			}
			if err := input.ValidateModel(model); err != nil {
				validationErr = err
				continue
			}
			candidates = append(candidates, candidate{id: catalog.AccountID, identity: catalog.ChatGPTAccountID})
		}
	}
	if len(candidates) == 0 {
		if len(snapshot.Accounts) == 0 {
			return nil, ErrNoAccounts
		}
		return nil, validationErr
	}
	return candidates, nil
}

func (e *Executor) selectAccount(model string, candidates []candidate, tried map[account.ID]bool) (candidate, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ids []account.ID
	for _, c := range candidates {
		if tried[c.id] || time.Now().Before(e.cooldowns[c.id]) {
			continue
		}
		delete(e.cooldowns, c.id)
		ids = append(ids, c.id)
	}
	id, ok := e.selector.Select(model, ids)
	if ok {
		for _, c := range candidates {
			if c.id == id {
				return c, true
			}
		}
	}
	return candidate{}, false
}

func (e *Executor) prepare(r *requestState, c candidate) (account.Account, error) {
	if err := e.registerAccount(r, c); err != nil {
		return account.Account{}, err
	}
	a, err := e.oauth.CurrentAccount(r.ctx, c.id)
	if err != nil {
		return account.Account{}, oauthFailure(r.ctx, err)
	}
	if a.Credentials().ChatGPTAccountID != c.identity {
		return account.Account{}, &codex.Failure{Category: codex.AccountProblem, SafeToRetry: true}
	}
	// Refresh may overlap a management change; recheck before dispatch.
	if err := e.registerAccount(r, c); err != nil {
		return account.Account{}, err
	}
	return a, nil
}

func (e *Executor) registerAccount(r *requestState, c candidate) error {
	e.gate.RLock()
	defer e.gate.RUnlock()
	record, err := e.store.GetAccount(r.ctx, c.id)
	if errors.Is(err, storage.ErrNotFound) {
		return &codex.Failure{Category: codex.AccountProblem, SafeToRetry: true}
	}
	if err != nil {
		return operationError(r.ctx, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if !record.Enabled || record.Account.Credentials().ChatGPTAccountID != c.identity {
		return &codex.Failure{Category: codex.AccountProblem, SafeToRetry: true}
	}
	r.accountID = c.id
	return nil
}

func safeRetry(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var failure *codex.Failure
	return errors.As(err, &failure) && failure.SafeToRetry &&
		(failure.Category == codex.ProviderLimit || failure.Category == codex.AccountProblem)
}

func (e *Executor) noteFailure(r *requestState, id account.ID, err error) {
	var failure *codex.Failure
	if errors.As(err, &failure) && failure.Category == codex.ProviderLimit && r.ctx.Err() == nil {
		until := time.Now().Add(time.Minute)
		if failure.RetryAfter.Kind == retry.RetryAt && failure.RetryAfter.Until.After(time.Now()) {
			until = failure.RetryAfter.Until
		}
		e.mu.Lock()
		if until.After(e.cooldowns[id]) {
			e.cooldowns[id] = until
		}
		e.mu.Unlock()
	}
	attrs := []slog.Attr{slog.String("request_id", r.id),
		slog.String("key_id", string(r.keyID)), slog.String("model", r.model),
		slog.String("account_id", string(id)), slog.String("result", errorCode(err))}
	if failure != nil {
		attrs = append(attrs, slog.Int("http_status", failure.HTTPStatus), slog.Bool("safe_to_retry", failure.SafeToRetry))
	}
	e.logger.LogAttrs(r.ctx, slog.LevelWarn, "attempt failed", attrs...)
}

func oauthFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, codexoauth.ErrDisabled) || errors.Is(err, codexoauth.ErrNeedsSignIn) ||
		errors.Is(err, codexoauth.ErrUnavailable) || errors.Is(err, storage.ErrNotFound) {
		return &codex.Failure{Category: codex.AccountProblem, SafeToRetry: true}
	}
	return operationError(ctx, err)
}
