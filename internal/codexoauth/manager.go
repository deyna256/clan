package codexoauth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/retry"
	"github.com/deyna256/clan/internal/storage"
)

var (
	ErrClosed       = errors.New("codex oauth: manager closed")
	ErrDisabled     = errors.New("codex oauth: account disabled")
	ErrNeedsSignIn  = errors.New("codex oauth: account needs sign-in")
	ErrUnavailable  = errors.New("codex oauth: account temporarily unavailable")
	ErrLoginPending = errors.New("codex oauth: login already pending")
	ErrInvalidLogin = errors.New("codex oauth: invalid login")
)

// AccountState describes availability without exposing credentials.
type AccountState string

const (
	StateConnected   AccountState = "connected"
	StateRefreshing  AccountState = "refreshing"
	StateUnavailable AccountState = "temporarily_unavailable"
	StateNeedsSignIn AccountState = "needs_sign_in"
	StateDisabled    AccountState = "disabled"
)

// AccountStatus is safe to return through the authenticated management API.
type AccountStatus struct {
	Identity  account.Identity
	State     AccountState
	ExpiresAt time.Time
	RetryAt   time.Time
}

type refreshState struct {
	previous     storage.AccountRecord
	done         chan struct{}
	cancel       context.CancelFunc
	replacement  *account.OAuthCredentials
	needsSignIn  bool
	retryAt      time.Time
	retryBlocked bool
}

// Manager owns callback serving and bounded OAuth jobs. Close it before closing
// its store. Account snapshots returned by CurrentAccount contain secrets.
type Manager struct {
	client *Client
	store  *storage.Store
	// lifetime belongs to the manager, never to an individual waiting request.
	lifetime  context.Context
	cancel    context.CancelFunc
	server    *http.Server
	mu        sync.Mutex
	closed    bool
	revision  uint64
	refreshes map[account.ID]*refreshState
	login     *pendingLogin
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// NewManager takes ownership of an already-bound callback listener on success.
// Startup selects its bind address; the provider redirect remains RedirectURI.
func NewManager(ctx context.Context, client *Client, store *storage.Store, listener net.Listener) (*Manager, error) {
	if client == nil || store == nil || listener == nil {
		return nil, errors.New("codex oauth: manager dependencies are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	m := &Manager{
		client: client, store: store, lifetime: lifetime, cancel: cancel,
		refreshes: make(map[account.ID]*refreshState),
	}
	m.server = &http.Server{
		Handler:           http.HandlerFunc(m.callback),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	m.wg.Go(func() {
		if err := m.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.cancel()
		}
	})
	return m, nil
}

// Close cancels and joins all manager jobs and closes the callback listener.
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		m.mu.Unlock()
		m.closeErr = m.server.Close()
		m.wg.Wait()
		m.mu.Lock()
		clear(m.refreshes)
		if m.login != nil {
			m.login.authorization = Authorization{}
			m.login.previous = nil
			m.login.identity = account.Identity{}
			if m.login.status.State == LoginWaiting || m.login.status.State == LoginExchanging {
				m.login.status.State = LoginCanceled
			}
		}
		m.mu.Unlock()
	})
	return m.closeErr
}

// CurrentAccount refreshes on demand and returns only persisted, unexpired
// credentials. Canceling a waiter does not abandon a rotated refresh token.
func (m *Manager) CurrentAccount(ctx context.Context, id account.ID) (account.Account, error) {
	waited := false
	for {
		record, state, err := m.current(ctx, id)
		if err != nil {
			return account.Account{}, err
		}
		credentials := record.Account.Credentials()
		m.mu.Lock()
		if m.closed || m.lifetime.Err() != nil {
			m.mu.Unlock()
			return account.Account{}, ErrClosed
		}
		if m.refreshes[id] != state {
			m.mu.Unlock()
			continue
		}
		if state.needsSignIn {
			m.mu.Unlock()
			return account.Account{}, ErrNeedsSignIn
		}
		fresh := credentials.ExpiresAt.After(time.Now().Add(5 * time.Minute))
		if fresh || waited || state.retryBlocked || time.Now().Before(state.retryAt) {
			replacement := state.replacement != nil
			m.mu.Unlock()
			if replacement || !credentials.ExpiresAt.After(time.Now()) {
				return account.Account{}, ErrUnavailable
			}
			return record.Account, nil
		}
		if state.done == nil {
			jobCtx, cancel := context.WithTimeout(m.lifetime, 30*time.Second)
			state.cancel = cancel
			state.done = make(chan struct{})
			m.wg.Go(func() { m.refresh(jobCtx, id, state) })
		}
		done := state.done
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return account.Account{}, ctx.Err()
		case <-m.lifetime.Done():
			return account.Account{}, ErrClosed
		case <-done:
			waited = true
		}
	}
}

func (m *Manager) current(ctx context.Context, id account.ID) (storage.AccountRecord, *refreshState, error) {
	for {
		if err := ctx.Err(); err != nil {
			return storage.AccountRecord{}, nil, err
		}
		m.mu.Lock()
		if m.closed || m.lifetime.Err() != nil {
			m.mu.Unlock()
			return storage.AccountRecord{}, nil, ErrClosed
		}
		before := m.refreshes[id]
		revision := m.revision
		m.mu.Unlock()
		record, err := m.store.GetAccount(ctx, id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				m.mu.Lock()
				if m.refreshes[id] == before && m.revision == revision {
					m.invalidateLocked(id)
				}
				m.mu.Unlock()
			}
			return storage.AccountRecord{}, nil, err
		}
		m.mu.Lock()
		if m.closed || m.lifetime.Err() != nil {
			m.mu.Unlock()
			return storage.AccountRecord{}, nil, ErrClosed
		}
		if m.refreshes[id] != before || m.revision != revision {
			m.mu.Unlock()
			continue
		}
		if !record.Enabled {
			m.invalidateLocked(id)
			m.mu.Unlock()
			return storage.AccountRecord{}, nil, ErrDisabled
		}
		if before == nil || before.previous != record {
			m.invalidateLocked(id)
			before = &refreshState{previous: record}
			m.refreshes[id] = before
		}
		m.mu.Unlock()
		return record, before, nil
	}
}

func (m *Manager) refresh(ctx context.Context, id account.ID, state *refreshState) {
	defer state.cancel()
	var credentials account.OAuthCredentials
	var err error
	if state.replacement != nil {
		credentials = *state.replacement
		if !credentials.ExpiresAt.After(time.Now()) {
			err = ErrNeedsSignIn
		}
	} else {
		credentials, err = m.client.Refresh(ctx, state.previous.Account.Credentials())
	}
	if err == nil && credentials.ChatGPTAccountID != state.previous.Account.Credentials().ChatGPTAccountID {
		err = ErrNeedsSignIn
	}
	var replacement *account.OAuthCredentials
	if err == nil {
		err = m.store.ReplaceAccountCredentialsIfUnchanged(ctx, state.previous, credentials)
		if err != nil && !errors.Is(err, storage.ErrConflict) {
			replacement = &credentials
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	done := state.done
	state.done = nil
	defer close(done)
	if m.refreshes[id] != state {
		return
	}
	if err == nil {
		m.invalidateLocked(id)
		return
	}
	state.replacement = replacement
	state.retryAt = time.Time{}
	state.retryBlocked = false
	var failure *Failure
	state.needsSignIn = errors.Is(err, ErrNeedsSignIn) || (errors.As(err, &failure) && failure.Kind != FailureTransient)
	if state.needsSignIn {
		return
	}
	state.retryAt = time.Now().Add(time.Minute)
	if failure != nil {
		state.retryBlocked = failure.RetryAfter.Kind == retry.RetryBlocked
		if failure.RetryAfter.Kind == retry.RetryAt && failure.RetryAfter.Until.After(state.retryAt) {
			state.retryAt = failure.RetryAfter.Until
		}
	}
}

// Status reports stored and runtime state without initiating a refresh.
func (m *Manager) Status(ctx context.Context, id account.ID) (AccountStatus, error) {
	record, err := m.store.GetAccount(ctx, id)
	if err != nil {
		return AccountStatus{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked(record), nil
}

// List returns safe account statuses in storage order without initiating refreshes.
func (m *Manager) List(ctx context.Context) ([]AccountStatus, error) {
	records, err := m.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	statuses := make([]AccountStatus, 0, len(records))
	for _, record := range records {
		statuses = append(statuses, m.statusLocked(record))
	}
	return statuses, nil
}

func (m *Manager) statusLocked(record storage.AccountRecord) AccountStatus {
	credentials := record.Account.Credentials()
	status := AccountStatus{Identity: record.Account.Identity(), State: StateConnected, ExpiresAt: credentials.ExpiresAt}
	if !record.Enabled {
		status.State = StateDisabled
		return status
	}
	if !credentials.ExpiresAt.After(time.Now()) {
		status.State = StateUnavailable
	}
	state := m.refreshes[status.Identity.ID]
	if state == nil || state.previous != record {
		return status
	}
	status.RetryAt = state.retryAt
	switch {
	case state.needsSignIn:
		status.State = StateNeedsSignIn
	case state.done != nil:
		status.State = StateRefreshing
	case state.replacement != nil:
		status.State = StateUnavailable
	}
	return status
}

// Disable persists disablement before canceling in-flight work for the account.
func (m *Manager) Disable(ctx context.Context, id account.ID) error {
	if err := m.store.DisableAccount(ctx, id); err != nil {
		return err
	}
	m.invalidate(id)
	return nil
}

// Delete removes stored credentials before canceling in-flight work.
func (m *Manager) Delete(ctx context.Context, id account.ID) error {
	if err := m.store.DeleteAccount(ctx, id); err != nil {
		return err
	}
	m.invalidate(id)
	return nil
}

func (m *Manager) invalidate(id account.ID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidateLocked(id)
	if m.login != nil && m.login.previous != nil && m.login.previous.Account.Identity().ID == id {
		m.login.cancel()
		if m.login.status.State == LoginWaiting || m.login.status.State == LoginExchanging {
			m.login.status.State = LoginCanceled
			m.login.authorization = Authorization{}
		}
	}
}

func (m *Manager) invalidateLocked(id account.ID) {
	m.revision++
	if state := m.refreshes[id]; state != nil && state.cancel != nil {
		state.cancel()
	}
	delete(m.refreshes, id)
}
