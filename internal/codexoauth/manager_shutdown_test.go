package codexoauth_test

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"modernc.org/sqlite"

	"github.com/deyna256/clan/internal/codexoauth"
)

func TestManagerShutdownDrainsRotatedCredentialSave(t *testing.T) {
	if testing.Short() {
		t.Skip("temporary SQLite integration")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	function := "hold_refresh_" + rand.Text()
	if err := sqlite.RegisterScalarFunction(function, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-release
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	listener := &idleListener{closed: make(chan struct{})}
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return tokenResponseJSON(200, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","expires_in":3600}`), nil
	}), listener)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	db := openFixtureSQL(t, f.path)
	if _, err := db.Exec("CREATE TRIGGER hold_refresh BEFORE UPDATE OF credentials ON accounts BEGIN SELECT " + function + "(); END"); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.manager.CurrentAccount(t.Context(), "one")
		result <- err
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("refresh ended before persistence: %v", err)
	}
	shutdown := make(chan error, 1)

	go func() { shutdown <- f.manager.Shutdown(t.Context()) }()
	<-listener.closed

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := f.manager.StartLogin(ctx, "New"); !errors.Is(err, codexoauth.ErrClosed) {
		t.Fatalf("new login during shutdown = %v, want closed", err)
	}
	if _, err := f.manager.StartReconnect(ctx, "one"); !errors.Is(err, codexoauth.ErrClosed) {
		t.Fatalf("new reconnect during shutdown = %v, want closed", err)
	}
	if _, err := f.manager.CurrentAccount(ctx, "one"); !errors.Is(err, codexoauth.ErrClosed) {
		t.Fatalf("new refresh during shutdown = %v, want closed", err)
	}
	select {
	case err := <-shutdown:
		t.Fatalf("shutdown returned before persistence completed: %v", err)
	default:
	}
	unblock()
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, codexoauth.ErrClosed) {
		t.Fatalf("stopped waiter = %v, want closed", err)
	}
	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if record.Account.Credentials().RefreshToken != "rotated-refresh" {
		t.Fatal("shutdown discarded rotated credentials before persistence")
	}
	if err := f.manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("repeated shutdown = %v", err)
	}
	if err := f.manager.Close(); err != nil {
		t.Fatalf("close after shutdown = %v", err)
	}
}

func TestManagerShutdownDrainsClaimedLoginExchange(t *testing.T) {
	listener, callback := callbackListener(t)
	stopped := &stopListener{Listener: listener, closed: make(chan struct{})}
	f, entered, release := blockedOAuthFixture(t, loginTokenResponse("provider"), stopped)
	instructions, err := f.manager.StartLogin(t.Context(), "Primary")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)
	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		response, _ := http.Get(callback + "?state=" + url.QueryEscape(state) + "&code=code")
		if response != nil {
			response.Body.Close()
		}
	}()
	<-entered
	shutdown := make(chan error, 1)

	go func() { shutdown <- f.manager.Shutdown(t.Context()) }()
	<-stopped.closed
	release()
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	<-callbackDone

	status := f.manager.LoginStatus()
	if status.State != codexoauth.LoginSucceeded || status.LoginID != instructions.LoginID {
		t.Fatalf("drained login status = %+v, want succeeded", status)
	}
	record, err := f.store.GetAccount(t.Context(), instructions.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Account.Credentials().RefreshToken != "login-refresh" {
		t.Fatal("shutdown discarded completed login credentials")
	}
	response, err := http.Get(callback)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("callback listener still accepted requests after shutdown")
	}
}

func TestManagerShutdownDrainsRefreshBeforePersistence(t *testing.T) {
	listener := &idleListener{closed: make(chan struct{})}
	f, entered, release := blockedOAuthFixture(t,
		tokenResponseJSON(200, `{"access_token":"rotated-access","refresh_token":"rotated-refresh","expires_in":3600}`), listener)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	result := make(chan error, 1)
	go func() {
		_, err := f.manager.CurrentAccount(t.Context(), "one")
		result <- err
	}()
	<-entered
	shutdown := make(chan error, 1)

	go func() { shutdown <- f.manager.Shutdown(t.Context()) }()
	<-listener.closed
	release()
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, codexoauth.ErrClosed) {
		t.Fatalf("stopped waiter = %v, want closed", err)
	}

	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if record.Account.Credentials().RefreshToken != "rotated-refresh" {
		t.Fatal("shutdown discarded credentials returned by an in-flight refresh")
	}
}

func TestManagerShutdownCancellationDoesNotWaitForUncooperativeJob(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "canceled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer unblock()
				f := managerFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					close(entered)
					<-r.Context().Done()
					close(canceled)
					<-release
					return nil, r.Context().Err()
				}), nil)
				saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
				result := make(chan error, 1)
				go func() {
					_, err := f.manager.CurrentAccount(t.Context(), "one")
					result <- err
				}()
				<-entered
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				want := context.DeadlineExceeded
				if !deadline {
					cancel()
					want = context.Canceled
				}

				err := f.manager.Shutdown(ctx)

				if !errors.Is(err, want) {
					t.Fatalf("shutdown = %v, want %v", err, want)
				}
				<-canceled
				closed := make(chan error, 1)
				go func() { closed <- f.manager.Close() }()
				synctest.Wait()
				select {
				case err := <-closed:
					t.Fatalf("close returned before job cleanup: %v", err)
				default:
				}
				unblock()
				if err := <-closed; err != nil {
					t.Fatal(err)
				}
				if err := <-result; !errors.Is(err, codexoauth.ErrClosed) {
					t.Fatalf("canceled waiter = %v, want closed", err)
				}
				if err := f.manager.Shutdown(t.Context()); err != nil {
					t.Fatalf("shutdown after joined close = %v", err)
				}
			})
		})
	}
}

func TestManagerCloseInterruptsConcurrentGracefulShutdowns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		listener := &idleListener{closed: make(chan struct{})}
		f := managerFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			<-release
			return nil, r.Context().Err()
		}), listener)
		saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
		result := make(chan error, 1)
		go func() {
			_, err := f.manager.CurrentAccount(t.Context(), "one")
			result <- err
		}()
		<-entered
		shutdowns := make(chan error, 2)
		for range 2 {
			go func() { shutdowns <- f.manager.Shutdown(t.Context()) }()
		}
		<-listener.closed
		closed := make(chan error, 1)

		go func() { closed <- f.manager.Close() }()
		if err := <-result; !errors.Is(err, codexoauth.ErrClosed) {
			t.Fatalf("close did not cancel waiter: %v", err)
		}
		synctest.Wait()
		select {
		case err := <-shutdowns:
			t.Fatalf("shutdown returned before canceled job completed: %v", err)
		case err := <-closed:
			t.Fatalf("close returned before canceled job completed: %v", err)
		default:
		}
		unblock()
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := <-shutdowns; err != nil {
				t.Fatal(err)
			}
		}
	})
}

type stopListener struct {
	net.Listener
	closed chan struct{}
}

func (l *stopListener) Close() error {
	err := l.Listener.Close()
	close(l.closed)
	return err
}
