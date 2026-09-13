package codexoauth_test

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/deyna256/clan/internal/codexoauth"
)

func TestCancelLoginWaitsForPersistenceAndPreservesSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("temporary SQLite integration")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	function := "hold_login_" + rand.Text()
	holdSave := func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-release
		return nil, nil
	}
	if err := sqlite.RegisterScalarFunction(function, 0, holdSave); err != nil {
		t.Fatal(err)
	}
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return loginTokenResponse("provider"), nil
	}), listener)
	db := openFixtureSQL(t, f.path)
	query := "CREATE TRIGGER hold_login BEFORE INSERT ON accounts BEGIN SELECT " + function + "(); END"
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
	instructions, err := f.manager.StartLogin(t.Context(), "Primary")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)
	type callbackResult struct {
		status int
		err    error
	}
	result := make(chan callbackResult, 1)
	go func() {
		response, err := http.Get(callback + "?state=" + url.QueryEscape(state) + "&code=code")
		got := callbackResult{err: err}
		if response != nil {
			got.status = response.StatusCode
			got.err = errors.Join(got.err, response.Body.Close())
		}
		result <- got
	}()
	select {
	case <-entered:
	case got := <-result:
		t.Fatalf("callback ended before persistence: %+v", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	cancellation := make(chan error, 1)
	go func() { cancellation <- f.manager.CancelLogin(ctx, instructions.LoginID) }()
	select {
	case err = <-cancellation:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not return after its deadline")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel during persistence = %v, want deadline exceeded", err)
	}
	if got := f.manager.LoginStatus(); got.State != codexoauth.LoginExchanging {
		t.Fatalf("cancel interrupted persistence: %+v", got)
	}
	unblock()
	if got := <-result; got.err != nil || got.status != http.StatusOK {
		t.Fatalf("callback after persistence = %+v, want HTTP 200", got)
	}
	record, err := f.store.GetAccount(t.Context(), instructions.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Enabled || record.Account.Credentials().RefreshToken != "login-refresh" {
		t.Fatal("successful login credentials were not persisted")
	}
	completed := f.manager.LoginStatus()
	if completed.State != codexoauth.LoginSucceeded || completed.LoginID != instructions.LoginID {
		t.Fatalf("persisted login status = %+v, want succeeded", completed)
	}
	if err := f.manager.CancelLogin(t.Context(), instructions.LoginID); err != nil {
		t.Fatal(err)
	}
	if got := f.manager.LoginStatus(); got != completed {
		t.Fatalf("cancel changed successful login: %+v", got)
	}
}
