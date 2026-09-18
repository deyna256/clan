package codexoauth_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/credentialcipher"
	"github.com/deyna256/clan/internal/storage"
)

func TestManagerSharesRefreshAndPersistsAfterWaiterCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		signal := sync.OnceFunc(func() { close(entered) })
		var calls atomic.Int32
		f := managerFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			signal()
			<-release
			return tokenResponseJSON(http.StatusOK, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","expires_in":3600}`), nil
		}), nil)
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		old := saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
		ctx, cancel := context.WithCancel(t.Context())
		first := make(chan error, 1)
		go func() {
			_, err := f.manager.CurrentAccount(ctx, "one")
			first <- err
		}()
		<-entered

		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter error = %v", err)
		}
		results := make([]struct {
			value account.Account
			err   error
		}, 8)
		var workers sync.WaitGroup
		for i := range results {
			workers.Go(func() {
				results[i].value, results[i].err = f.manager.CurrentAccount(t.Context(), "one")
			})
		}
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("overlapping refresh calls = %d, want 1", calls.Load())
		}
		unblock()
		workers.Wait()
		for _, result := range results {
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.value.Credentials().AccessToken != "rotated-access" {
				t.Error("waiter did not receive persisted replacement")
			}
		}
		record, err := f.store.GetAccount(t.Context(), "one")
		if err != nil {
			t.Fatal(err)
		}
		if record.Account.Credentials().RefreshToken != "rotated-refresh" || record.Account.Identity() != old.Identity() {
			t.Error("rotation was not durably saved under the original identity")
		}
		if calls.Load() != 1 {
			t.Fatalf("refresh calls = %d, want 1", calls.Load())
		}
	})
}

func TestManagerRefreshFailures(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		body      string
		wantErr   error
		wantState codexoauth.AccountState
	}{
		{name: "transient uses unexpired token", status: 503, body: `{}`, wantState: codexoauth.StateConnected},
		{name: "rate limit uses unexpired token", status: 429, body: `{}`, wantState: codexoauth.StateConnected},
		{name: "terminal immediately blocks old token", status: 400, body: `{"error":"invalid_grant"}`, wantErr: codexoauth.ErrNeedsSignIn, wantState: codexoauth.StateNeedsSignIn},
		{name: "identity change requires reconnect", status: 200, body: fmt.Sprintf(`{"access_token":"new","expires_in":3600,"id_token":%q}`, identityToken("different", false)), wantErr: codexoauth.ErrNeedsSignIn, wantState: codexoauth.StateNeedsSignIn},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return tokenResponseJSON(tt.status, tt.body), nil
			}), nil)
			old := saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))

			for range 2 {
				if err := f.manager.Enable(t.Context(), "one"); err != nil {
					t.Fatal(err)
				}
				got, err := f.manager.CurrentAccount(t.Context(), "one")
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("CurrentAccount error = %v, want %v", err, tt.wantErr)
				}
				if err == nil && (got.Identity() != old.Identity() || got.Credentials().AccessToken != old.Credentials().AccessToken) {
					t.Error("transient refresh did not preserve old snapshot")
				}
			}
			status, err := f.manager.Status(t.Context(), "one")
			if err != nil {
				t.Fatal(err)
			}
			if status.State != tt.wantState {
				t.Errorf("state = %s, want %s", status.State, tt.wantState)
			}
			if calls.Load() != 1 {
				t.Errorf("refresh calls = %d, want 1", calls.Load())
			}
			record, err := f.store.GetAccount(t.Context(), "one")
			if err != nil {
				t.Fatal(err)
			}
			if record.Account.Credentials().AccessToken != "old-access" || record.Account.Credentials().ChatGPTAccountID != "provider" {
				t.Fatal("failed refresh changed stored credentials")
			}
		})
	}
}

func TestManagerTransientRetryRespectsExpiryAndRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			response := tokenResponseJSON(429, `{}`)
			response.Header.Set("Retry-After", "120")
			return response, nil
		}), nil)
		saveOAuthAccount(t, f.store, "one", time.Now().Add(30*time.Second))

		if _, err := f.manager.CurrentAccount(t.Context(), "one"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrUnavailable) {
			t.Fatalf("expired old token error = %v", err)
		}
		if calls.Load() != 1 {
			t.Fatal("retried before Retry-After")
		}
		status, err := f.manager.Status(t.Context(), "one")
		if err != nil || status.State != codexoauth.StateUnavailable {
			t.Fatalf("expired account status = %s, error = %v", status.State, err)
		}
		time.Sleep(time.Minute)
		if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrUnavailable) {
			t.Fatalf("retry error = %v", err)
		}
		if calls.Load() != 2 {
			t.Fatalf("refresh calls = %d, want 2", calls.Load())
		}
	})
}

func TestManagerRetriesRetainedRotationAfterCanceledReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return tokenResponseJSON(200, `{"access_token":"replacement","refresh_token":"rotation","expires_in":3600}`), nil
		}), nil)
		saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
		db := openFixtureSQL(t, f.path)
		if _, err := db.Exec(`CREATE TRIGGER reject_rotation BEFORE UPDATE OF credentials ON accounts BEGIN SELECT RAISE(FAIL, 'unavailable'); END`); err != nil {
			t.Fatal(err)
		}

		if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrUnavailable) {
			t.Fatalf("persistence failure error = %v", err)
		}
		status, err := f.manager.Status(t.Context(), "one")
		if err != nil || status.State != codexoauth.StateUnavailable {
			t.Fatalf("unsaved rotation status = %s, error = %v", status.State, err)
		}
		if _, err := db.Exec(`DROP TRIGGER reject_rotation`); err != nil {
			t.Fatal(err)
		}
		instructions, err := f.manager.StartReconnect(t.Context(), "one")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.manager.CancelLogin(t.Context(), instructions.LoginID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrUnavailable) {
			t.Fatalf("retry delay error = %v", err)
		}
		time.Sleep(time.Minute)
		got, err := f.manager.CurrentAccount(t.Context(), "one")

		if err != nil {
			t.Fatal(err)
		}
		if got.Credentials().RefreshToken != "rotation" {
			t.Error("replacement was not persisted")
		}
		if calls.Load() != 1 {
			t.Fatalf("refresh calls = %d, want 1", calls.Load())
		}
	})
}

func TestManagerLateRefreshCannotUndoDisable(t *testing.T) {
	f, entered, release := blockedOAuthFixture(t, tokenResponseJSON(200, `{"access_token":"late","refresh_token":"late-refresh","expires_in":3600}`), nil)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	result := make(chan error, 1)
	go func() {
		_, err := f.manager.CurrentAccount(t.Context(), "one")
		result <- err
	}()
	<-entered

	if err := f.manager.Disable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; !errors.Is(err, codexoauth.ErrDisabled) {
		t.Fatalf("CurrentAccount error = %v", err)
	}
	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if record.Enabled {
		t.Fatal("disabled account enabled again")
	}
}

func TestManagerEnablePreservesCredentialsWithoutRefreshing(t *testing.T) {
	var calls atomic.Int32
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected refresh")
	}), nil)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(-time.Minute))
	previous, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Disable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}

	err = f.manager.Enable(t.Context(), "one")

	if err != nil {
		t.Fatal(err)
	}
	got, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Account != previous.Account {
		t.Fatal("enable changed stored identity or credentials")
	}
	status, err := f.manager.Status(t.Context(), "one")
	if err != nil || status.State != codexoauth.StateUnavailable {
		t.Fatalf("enabled expired account state = %s, error = %v", status.State, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("enable made %d provider calls, want 0", calls.Load())
	}
}

func TestManagerRepeatedEnablePreservesActiveRefresh(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := managerFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		return tokenResponseJSON(200, `{"access_token":"new","refresh_token":"rotation","expires_in":3600}`), nil
	}), nil)
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	result := make(chan error, 1)
	go func() {
		_, err := f.manager.CurrentAccount(t.Context(), "one")
		result <- err
	}()
	<-entered

	if err := f.manager.Enable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || record.Account.Credentials().RefreshToken != "rotation" {
		t.Fatal("repeated enable abandoned a valid refresh")
	}
}

func TestManagerLateRefreshCannotWriteAfterReenable(t *testing.T) {
	f, entered, release := blockedOAuthFixture(t, tokenResponseJSON(200, `{"access_token":"late","refresh_token":"late-refresh","expires_in":3600}`), nil)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	result := make(chan error, 1)
	go func() {
		_, err := f.manager.CurrentAccount(t.Context(), "one")
		result <- err
	}()
	<-entered

	if err := f.manager.Disable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Enable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if !record.Enabled || record.Account.Credentials().RefreshToken != "old-refresh" {
		t.Fatal("stale refresh changed credentials after re-enablement")
	}
}

func TestManagerLateRefreshCannotUndoDelete(t *testing.T) {
	f, entered, release := blockedOAuthFixture(t, tokenResponseJSON(200, `{"access_token":"late","refresh_token":"late-refresh","expires_in":3600}`), nil)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	result := make(chan error, 1)
	go func() {
		_, err := f.manager.CurrentAccount(t.Context(), "one")
		result <- err
	}()
	<-entered

	if err := f.manager.Delete(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("CurrentAccount error = %v", err)
	}
	if _, err := f.store.GetAccount(t.Context(), "one"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("deleted account recreated")
	}
}

func TestManagerLateRefreshCannotUndoReplacement(t *testing.T) {
	f, entered, release := blockedOAuthFixture(t, tokenResponseJSON(200, `{"access_token":"late","refresh_token":"late-refresh","expires_in":3600}`), nil)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	result := make(chan error, 1)
	go func() {
		_, err := f.manager.CurrentAccount(t.Context(), "one")
		result <- err
	}()
	<-entered

	previous, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReplaceAccountCredentialsIfUnchanged(t.Context(), previous, account.OAuthCredentials{ChatGPTAccountID: "provider", AccessToken: "newer", RefreshToken: "newer-refresh", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if record.Account.Credentials().AccessToken != "newer" {
		t.Fatal("late refresh overwrote newer credentials")
	}
}

func TestManagerCloseCancelsAndJoinsRefresh(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	f := managerFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		close(exited)
		return nil, r.Context().Err()
	}), nil)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	result := make(chan error, 1)
	go func() {
		_, err := f.manager.CurrentAccount(t.Context(), "one")
		result <- err
	}()
	<-entered

	if err := f.manager.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Close returned before refresh cleanup")
	}
	if err := <-result; !errors.Is(err, codexoauth.ErrClosed) {
		t.Fatalf("waiter error = %v", err)
	}
	if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrClosed) {
		t.Fatalf("closed manager error = %v", err)
	}
}

func TestManagerRefreshesAtExactlyFiveMinutesRemaining(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return tokenResponseJSON(200, `{"access_token":"new","expires_in":3600}`), nil
		}), nil)
		saveOAuthAccount(t, f.store, "one", time.Now().Add(5*time.Minute+time.Second))

		if _, err := f.manager.CurrentAccount(t.Context(), "one"); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 0 {
			t.Fatal("refreshed before five-minute boundary")
		}
		time.Sleep(time.Second)
		got, err := f.manager.CurrentAccount(t.Context(), "one")

		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 || got.Credentials().AccessToken != "new" {
			t.Fatal("did not refresh at five-minute boundary")
		}
	})
}

func TestManagerRetriesTransientFailureAfterOneMinute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return tokenResponseJSON(503, `{}`), nil
			}
			return tokenResponseJSON(200, `{"access_token":"recovered","expires_in":3600}`), nil
		}), nil)
		saveOAuthAccount(t, f.store, "one", time.Now().Add(3*time.Minute))

		if _, err := f.manager.CurrentAccount(t.Context(), "one"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute - time.Second)
		if _, err := f.manager.CurrentAccount(t.Context(), "one"); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 {
			t.Fatal("retried before one minute")
		}
		time.Sleep(time.Second)
		got, err := f.manager.CurrentAccount(t.Context(), "one")

		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 2 || got.Credentials().AccessToken != "recovered" {
			t.Fatal("did not recover at retry boundary")
		}
	})
}

type oauthFixture struct {
	manager *codexoauth.Manager
	store   *storage.Store
	path    string
}

func managerFixture(t *testing.T, transport http.RoundTripper, listener net.Listener) oauthFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("temporary SQLite integration")
	}
	cipher, err := credentialcipher.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "oauth.db")
	store, err := storage.Open(t.Context(), path, cipher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	client, err := codexoauth.NewClient(&http.Client{Transport: transport}, "")
	if err != nil {
		t.Fatal(err)
	}
	if listener == nil {
		listener = &idleListener{closed: make(chan struct{})}
	}
	manager, err := codexoauth.NewManager(t.Context(), client, store, listener)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Error(err)
		}
	})
	return oauthFixture{manager: manager, store: store, path: path}
}

func saveOAuthAccount(t *testing.T, store *storage.Store, id account.ID, expires time.Time) account.Account {
	t.Helper()
	value, err := account.New(account.Identity{ID: id, Name: "Primary"}, account.OAuthCredentials{
		ChatGPTAccountID: "provider", AccessToken: "old-access",
		RefreshToken: "old-refresh", ExpiresAt: expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAccount(t.Context(), value, true); err != nil {
		t.Fatal(err)
	}
	return value
}

func tokenResponseJSON(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func openFixtureSQL(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

// An unused listener keeps refresh and virtual-time tests independent of sockets.
type idleListener struct{ closed chan struct{} }

func (l *idleListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *idleListener) Close() error              { close(l.closed); return nil }
func (l *idleListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1455} }

func blockedOAuthFixture(t *testing.T, response *http.Response, listener net.Listener) (oauthFixture, <-chan struct{}, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	signal := sync.OnceFunc(func() { close(entered) })
	unblock := sync.OnceFunc(func() { close(release) })
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		signal()
		<-release
		return response, nil
	}), listener)
	t.Cleanup(unblock)
	return f, entered, unblock
}
