package execution

import (
	"context"
	"errors"
	"log/slog"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/codexoauth"
)

type modelFetcher struct {
	oauth  *codexoauth.Manager
	client *codex.Client
	logger *slog.Logger
}

func (f modelFetcher) fetchModels(ctx context.Context, previous account.Account) (models []codex.Model, err error) {
	defer func() {
		if err != nil {
			f.logger.LogAttrs(ctx, slog.LevelWarn, "model discovery failed",
				slog.String("account_id", string(previous.Identity().ID)), slog.String("result", errorCode(err)))
		}
	}()
	a, err := f.oauth.CurrentAccount(ctx, previous.Identity().ID)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		category := codex.TransportFailure
		if errors.Is(err, codexoauth.ErrNeedsSignIn) || errors.Is(err, codexoauth.ErrDisabled) {
			category = codex.AccountProblem
		}
		return nil, &codex.Failure{Category: category}
	}
	if a.Credentials().ChatGPTAccountID != previous.Credentials().ChatGPTAccountID {
		return nil, &codex.Failure{Category: codex.AccountProblem}
	}
	return f.client.FetchModels(ctx, a)
}

// Models authenticates a key and returns the shared visible catalog without taking a generation slot.
func (e *Executor) Models(ctx context.Context, key string) ([]codex.Model, error) {
	if err := e.checkKey(ctx, key); err != nil {
		return nil, err
	}
	snapshot, err := e.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if err := e.checkKey(ctx, key); err != nil {
		return nil, err
	}
	return snapshot.Listed, nil
}

func (e *Executor) checkKey(ctx context.Context, key string) error {
	e.gate.RLock()
	defer e.gate.RUnlock()
	if e.closed {
		return ErrClosed
	}
	_, err := e.authenticate(ctx, key)
	return err
}

func (e *Executor) snapshot(ctx context.Context) (codex.CatalogSnapshot, error) {
	e.gate.Lock()
	err := e.syncCatalog(ctx)
	e.gate.Unlock()
	if err != nil {
		return codex.CatalogSnapshot{}, err
	}
	return e.catalog.Snapshot(ctx)
}

// The gate orders membership publication with account mutations and other snapshots.
func (e *Executor) syncCatalog(ctx context.Context) error {
	if e.closed {
		return ErrClosed
	}
	records, err := e.store.ListAccounts(ctx)
	if err != nil {
		return operationError(ctx, err)
	}
	accounts := make([]account.Account, 0, len(records))
	enabled := make(map[account.ID]bool, len(records))
	for _, record := range records {
		if record.Enabled {
			accounts = append(accounts, record.Account)
			enabled[record.Account.Identity().ID] = true
		}
	}
	e.mu.Lock()
	for id := range e.cooldowns {
		if !enabled[id] {
			delete(e.cooldowns, id)
		}
	}
	e.mu.Unlock()
	return e.catalog.SetAccounts(accounts)
}
