package execution

import (
	"context"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/account"
)

// RevokeKey persists revocation and waits for active requests to clean up.
// Cancellation of this call after persistence does not undo revocation or stop cleanup.
func (e *Executor) RevokeKey(ctx context.Context, id accesskey.ID) error {
	return e.change(ctx, func() error { return e.store.RevokeAccessKey(ctx, id) },
		func(r *requestState) bool { return r.keyID == id }, false)
}

// SetConcurrency changes new admissions only; existing requests keep their slots.
func (e *Executor) SetConcurrency(ctx context.Context, id accesskey.ID, limit int) error {
	return e.change(ctx, func() error { return e.store.UpdateAccessKeyConcurrency(ctx, id, limit) },
		func(*requestState) bool { return false }, false)
}

// DisableAccount persists disablement and waits for affected request cleanup.
func (e *Executor) DisableAccount(ctx context.Context, id account.ID) error {
	return e.change(ctx, func() error { return e.oauth.Disable(ctx, id) },
		func(r *requestState) bool { return r.accountID == id }, true)
}

// EnableAccount restores account eligibility without restarting canceled work.
func (e *Executor) EnableAccount(ctx context.Context, id account.ID) error {
	return e.change(ctx, func() error { return e.oauth.Enable(ctx, id) },
		func(*requestState) bool { return false }, true)
}

// DeleteAccount removes stored credentials and waits for affected request cleanup.
func (e *Executor) DeleteAccount(ctx context.Context, id account.ID) error {
	return e.change(ctx, func() error { return e.oauth.Delete(ctx, id) },
		func(r *requestState) bool { return r.accountID == id }, true)
}

func (e *Executor) change(ctx context.Context, persist func() error, matches func(*requestState) bool, accounts bool) error {
	// ponytail: one gate orders local writes and admission; split only if SQLite contention requires it.
	e.gate.Lock()
	if e.closed {
		e.gate.Unlock()
		return ErrClosed
	}
	if err := persist(); err != nil {
		e.gate.Unlock()
		return err
	}
	e.mu.Lock()
	var waits []<-chan struct{}
	for r := range e.active {
		if matches(r) {
			r.cancel()
			waits = append(waits, r.done)
		}
	}
	e.mu.Unlock()
	var err error
	if accounts {
		err = e.syncCatalog(ctx)
	}
	e.gate.Unlock()
	for _, done := range waits {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// Close rejects new work, cancels requests and joins execution and catalog cleanup.
// The application closes the borrowed OAuth manager and store afterward.
func (e *Executor) Close() {
	e.gate.Lock()
	e.closed = true
	e.mu.Lock()
	waits := make([]<-chan struct{}, 0, len(e.active))
	for r := range e.active {
		r.cancel()
		waits = append(waits, r.done)
	}
	e.mu.Unlock()
	e.gate.Unlock()
	e.catalog.Close()
	for _, done := range waits {
		<-done
	}
}
