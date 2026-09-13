package codexoauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/storage"
)

// LoginInstructions contains a secret-bearing URL for the administrator only.
// Remote administrators must forward localhost port 1455 to the callback listener.
type LoginInstructions struct {
	LoginID   string
	URL       string
	AccountID account.ID
	ExpiresAt time.Time
}

// LoginState describes the single most recent browser login.
type LoginState string

const (
	LoginWaiting    LoginState = "waiting"
	LoginExchanging LoginState = "exchanging"
	LoginSucceeded  LoginState = "succeeded"
	LoginFailed     LoginState = "failed"
	LoginCanceled   LoginState = "canceled"
	LoginExpired    LoginState = "expired"
)

// LoginStatus contains no authorization URL, callback state, code or tokens.
type LoginStatus struct {
	LoginID   string
	AccountID account.ID
	State     LoginState
	ExpiresAt time.Time
}

type pendingLogin struct {
	// Serializes cancellation with persistence without holding the manager lock.
	commit        chan struct{}
	authorization Authorization
	identity      account.Identity
	previous      *storage.AccountRecord
	status        LoginStatus
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
}

// StartLogin starts a new account connection with a securely generated local ID.
// Only one login may be pending; completion replaces the previous safe status.
func (m *Manager) StartLogin(ctx context.Context, name string) (LoginInstructions, error) {
	if strings.TrimSpace(name) == "" {
		return LoginInstructions{}, ErrInvalidLogin
	}
	return m.startLogin(ctx, account.Identity{ID: account.ID(rand.Text()), Name: name}, nil)
}

// StartReconnect signs in again for an existing enabled account. Its provider
// identity may change, but concurrently updated credentials cannot be overwritten.
func (m *Manager) StartReconnect(ctx context.Context, id account.ID) (LoginInstructions, error) {
	m.mu.Lock()
	closed := m.closed || m.lifetime.Err() != nil
	m.mu.Unlock()
	if closed {
		return LoginInstructions{}, ErrClosed
	}
	record, err := m.store.GetAccount(ctx, id)
	if err != nil {
		return LoginInstructions{}, err
	}
	if !record.Enabled {
		return LoginInstructions{}, ErrDisabled
	}
	return m.startLogin(ctx, record.Account.Identity(), &record)
}

func (m *Manager) startLogin(ctx context.Context, identity account.Identity, previous *storage.AccountRecord) (LoginInstructions, error) {
	if err := ctx.Err(); err != nil {
		return LoginInstructions{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.lifetime.Err() != nil {
		return LoginInstructions{}, ErrClosed
	}
	m.expireLoginLocked()
	if m.login != nil && (m.login.status.State == LoginWaiting || m.login.status.State == LoginExchanging) {
		return LoginInstructions{}, ErrLoginPending
	}
	expires := time.Now().Add(10 * time.Minute)
	loginCtx, cancel := context.WithDeadline(m.lifetime, expires)
	authorization := m.client.Begin()
	loginID := rand.Text()
	m.login = &pendingLogin{
		commit:        make(chan struct{}, 1),
		authorization: authorization, identity: identity, previous: previous, ctx: loginCtx, cancel: cancel,
		status: LoginStatus{LoginID: loginID, AccountID: identity.ID, State: LoginWaiting, ExpiresAt: expires},
	}
	return LoginInstructions{LoginID: loginID, URL: authorization.URL, AccountID: identity.ID, ExpiresAt: expires}, nil
}

// LoginStatus returns the bounded, safe completion record for the most recent login.
func (m *Manager) LoginStatus() LoginStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLoginLocked()
	if m.login == nil {
		return LoginStatus{}
	}
	return m.login.status
}

func (m *Manager) expireLoginLocked() {
	p := m.login
	if p != nil && p.status.State == LoginWaiting && !time.Now().Before(p.status.ExpiresAt) {
		p.status.State = LoginExpired
		p.previous = nil
		p.identity = account.Identity{}
		p.authorization = Authorization{}
		p.cancel()
	}
}

// CancelLogin cancels the matching latest login, or returns ErrLoginMismatch.
// It waits for an ongoing save subject to ctx; terminal states stay unchanged.
func (m *Manager) CancelLogin(ctx context.Context, loginID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	m.expireLoginLocked()
	p := m.login
	if p == nil || p.status.LoginID != loginID {
		m.mu.Unlock()
		return ErrLoginMismatch
	}
	if p.status.State != LoginWaiting && p.status.State != LoginExchanging {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.commit <- struct{}{}:
	}
	defer func() { <-p.commit }()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.login != p {
		return ErrLoginMismatch
	}
	m.expireLoginLocked()
	if p.status.State == LoginWaiting || p.status.State == LoginExchanging {
		p.status.State = LoginCanceled
		p.previous = nil
		p.identity = account.Identity{}
		p.authorization = Authorization{}
		p.cancel()
	}
	return nil
}

func (m *Manager) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	if r.URL.Path != "/auth/callback" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		http.Error(w, "Invalid login", http.StatusBadRequest)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["state"]) != 1 || len(query["code"]) > 1 || len(query["error"]) > 1 {
		http.Error(w, "Invalid login", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.expireLoginLocked()
	p := m.login
	validState := p != nil && p.status.State == LoginWaiting &&
		subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(p.authorization.State)) == 1
	if m.closed || m.lifetime.Err() != nil || !validState {
		m.mu.Unlock()
		http.Error(w, "Invalid or expired login", http.StatusBadRequest)
		return
	}
	// Claim before exchange: a code is never sent to the provider twice.
	verifier := p.authorization.Verifier
	p.authorization = Authorization{}
	p.status.State = LoginExchanging
	p.done = make(chan struct{})
	m.wg.Go(func() { m.finishLogin(p, query.Get("code"), verifier, query.Has("error")) })
	m.mu.Unlock()
	select {
	case <-r.Context().Done():
		return
	case <-p.done:
	}
	m.mu.Lock()
	succeeded := p.status.State == LoginSucceeded
	m.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !succeeded {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "<!doctype html><title>Sign-in failed</title><p>Sign-in failed. Return to CLAN and start again.</p>")
		return
	}
	_, _ = io.WriteString(w, "<!doctype html><title>Connected</title><p>Account connected. You can close this window.</p>")
}

func (m *Manager) finishLogin(p *pendingLogin, code, verifier string, denied bool) {
	defer close(p.done)
	defer p.cancel()
	defer func() {
		m.mu.Lock()
		p.previous = nil
		p.identity = account.Identity{}
		m.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()
	var credentials account.OAuthCredentials
	var err error
	if denied || strings.TrimSpace(code) == "" {
		err = ErrInvalidLogin
	} else {
		credentials, err = m.client.Exchange(ctx, code, verifier)
	}
	p.commit <- struct{}{}
	defer func() { <-p.commit }()
	if err == nil {
		err = ctx.Err()
	}
	m.mu.Lock()
	active := m.login == p && p.status.State == LoginExchanging
	m.mu.Unlock()
	if !active {
		return
	}
	if err == nil {
		if p.previous == nil {
			var value account.Account
			value, err = account.New(p.identity, credentials)
			if err == nil {
				err = m.store.CreateAccount(ctx, value, true)
			}
		} else {
			err = m.store.ReplaceAccountCredentialsIfUnchanged(ctx, *p.previous, credentials)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.status.State != LoginExchanging {
		return
	}
	p.status.State = LoginFailed
	if err == nil {
		p.status.State = LoginSucceeded
		m.invalidateLocked(p.identity.ID)
	}
}
