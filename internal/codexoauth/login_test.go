package codexoauth_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/storage"
)

func TestLoginIDsIdentifyLatestAttemptWithoutExposingOAuthState(t *testing.T) {
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected exchange")
	}), nil)
	if got := f.manager.LoginStatus(); got != (codexoauth.LoginStatus{}) {
		t.Fatalf("initial status = %+v, want zero status", got)
	}
	if err := f.manager.CancelLogin(t.Context(), "missing"); !errors.Is(err, codexoauth.ErrLoginMismatch) {
		t.Fatalf("cancel without attempt = %v, want ErrLoginMismatch", err)
	}
	first, err := f.manager.StartLogin(t.Context(), "First")
	if err != nil {
		t.Fatal(err)
	}
	if first.LoginID == "" || first.LoginID == string(first.AccountID) || first.LoginID == authorizationState(t, first) {
		t.Fatal("public login ID is missing or shares an account ID or OAuth state")
	}
	if err := f.manager.CancelLogin(t.Context(), "wrong"); !errors.Is(err, codexoauth.ErrLoginMismatch) {
		t.Fatalf("mismatched cancel = %v, want ErrLoginMismatch", err)
	}
	if got := f.manager.LoginStatus(); got.State != codexoauth.LoginWaiting || got.LoginID != first.LoginID {
		t.Fatalf("status after mismatched cancel = %+v", got)
	}
	if err := f.manager.CancelLogin(t.Context(), first.LoginID); err != nil {
		t.Fatal(err)
	}
	terminal := f.manager.LoginStatus()
	if err := f.manager.CancelLogin(t.Context(), first.LoginID); err != nil {
		t.Fatal(err)
	}
	if got := f.manager.LoginStatus(); got != terminal || got.State != codexoauth.LoginCanceled {
		t.Fatalf("terminal cancel changed status: %+v", got)
	}
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Hour))
	second, err := f.manager.StartReconnect(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}

	err = f.manager.CancelLogin(t.Context(), first.LoginID)

	if !errors.Is(err, codexoauth.ErrLoginMismatch) {
		t.Fatalf("stale cancel = %v, want ErrLoginMismatch", err)
	}
	if got := f.manager.LoginStatus(); got.LoginID != second.LoginID || got.State != codexoauth.LoginWaiting {
		t.Fatalf("stale cancel changed latest attempt: %+v", got)
	}
	if second.LoginID == first.LoginID || second.LoginID == "one" || second.LoginID == authorizationState(t, second) {
		t.Fatal("reconnect did not generate an independent public login ID")
	}
}

func TestCancelLoginHonorsCanceledContext(t *testing.T) {
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected exchange")
	}), nil)
	instructions, err := f.manager.StartLogin(t.Context(), "Primary")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = f.manager.CancelLogin(ctx, instructions.LoginID)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v, want context.Canceled", err)
	}
	if got := f.manager.LoginStatus(); got.State != codexoauth.LoginWaiting {
		t.Fatalf("canceled request changed login: %+v", got)
	}
}

func TestLoginPersistsBeforeSuccessAndRejectsStateReplay(t *testing.T) {
	var calls atomic.Int32
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return loginTokenResponse("provider"), nil
	}), listener)
	instructions, err := f.manager.StartLogin(t.Context(), "Primary")
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := url.Parse(instructions.URL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorization.Query().Get("state")
	if _, err := f.manager.StartLogin(t.Context(), "Other"); !errors.Is(err, codexoauth.ErrLoginPending) {
		t.Fatalf("second login error = %v", err)
	}

	bad := getCallback(t, callback+"?state=wrong&code=secret-code")
	if bad.status != 400 || calls.Load() != 0 {
		t.Fatal("bad state reached token exchange")
	}
	success := getCallback(t, callback+"?state="+url.QueryEscape(state)+"&code=secret-code")

	if success.status != 200 {
		t.Fatalf("callback status = %d", success.status)
	}
	record, err := f.store.GetAccount(t.Context(), instructions.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if !record.Enabled || record.Account.Credentials().RefreshToken != "login-refresh" {
		t.Fatal("success was not persisted")
	}
	if status := f.manager.LoginStatus(); status.State != codexoauth.LoginSucceeded || status.AccountID != instructions.AccountID {
		t.Fatalf("login status = %+v", status)
	}
	if err := f.manager.CancelLogin(t.Context(), instructions.LoginID); err != nil {
		t.Fatal(err)
	}
	if got := f.manager.LoginStatus(); got.State != codexoauth.LoginSucceeded || got.LoginID != instructions.LoginID {
		t.Fatalf("cancel after persistence changed successful login: %+v", got)
	}
	if success.cache != "no-store" || success.referrer != "no-referrer" {
		t.Fatal("missing callback secrecy headers")
	}
	for _, secret := range []string{state, "secret-code", "login-access", "login-refresh"} {
		if strings.Contains(success.body, secret) {
			t.Fatal("callback leaked a secret")
		}
	}
	replay := getCallback(t, callback+"?state="+url.QueryEscape(state)+"&code=secret-code")
	if replay.status != 400 || calls.Load() != 1 {
		t.Fatal("callback replay exchanged a code twice")
	}
}

func TestMalformedCallbackDoesNotConsumeLogin(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{name: "duplicate state", query: "&state=duplicate&code=code"},
		{name: "duplicate code", query: "&code=one&code=two"},
		{name: "duplicate error", query: "&error=one&error=two"},
		{name: "invalid query", query: "&code=one;two"},
		{name: "oversized query", query: "&code=" + strings.Repeat("x", 8193)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			listener, callback := callbackListener(t)
			f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return loginTokenResponse("provider"), nil
			}), listener)
			instructions, err := f.manager.StartLogin(t.Context(), "Primary")
			if err != nil {
				t.Fatal(err)
			}
			callback += "?state=" + url.QueryEscape(authorizationState(t, instructions))

			response := getCallback(t, callback+tt.query)

			if response.status != http.StatusBadRequest || calls.Load() != 0 {
				t.Fatalf("malformed callback status = %d, exchanges = %d", response.status, calls.Load())
			}
			response = getCallback(t, callback+"&code=code")
			if response.status != http.StatusOK || calls.Load() != 1 {
				t.Fatal("malformed callback consumed the pending login")
			}
		})
	}
}

func TestLoginClaimsStateBeforeExchangeAndCancellationPreventsSave(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		<-release
		return loginTokenResponse("provider"), nil
	}), listener)
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	instructions, err := f.manager.StartLogin(t.Context(), "Primary")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)
	callback += "?state=" + url.QueryEscape(state) + "&code=code"
	result := make(chan error, 1)
	go func() {
		response, err := http.Get(callback)
		if response != nil {
			response.Body.Close()
		}
		result <- err
	}()
	<-entered

	replay := getCallback(t, callback)
	if replay.status != 400 {
		t.Fatal("concurrent callback was not rejected")
	}
	if err := f.manager.CancelLogin(t.Context(), instructions.LoginID); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 1 {
		t.Fatalf("exchange calls = %d, want 1", calls.Load())
	}
	if status := f.manager.LoginStatus(); status.State != codexoauth.LoginCanceled {
		t.Fatalf("state = %s, want canceled", status.State)
	}
	if _, err := f.store.GetAccount(t.Context(), instructions.AccountID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("canceled account error = %v", err)
	}
}

func TestLoginExchangeFailureDoesNotSaveOrReportSuccess(t *testing.T) {
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return tokenResponseJSON(400, `{"error":"invalid_grant","error_description":"secret-provider-body"}`), nil
	}), listener)
	instructions, err := f.manager.StartLogin(t.Context(), "Primary")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)

	response := getCallback(t, callback+"?state="+url.QueryEscape(state)+"&code=secret-code")

	if response.status != 400 || strings.Contains(response.body, "secret-") {
		t.Fatal("failure response was successful or unsafe")
	}
	if status := f.manager.LoginStatus(); status.State != codexoauth.LoginFailed {
		t.Fatalf("state = %s, want failed", status.State)
	}
	records, err := f.store.ListAccounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatal("failed login saved an account")
	}
}

func TestLoginPersistenceFailureDoesNotSaveOrReportSuccess(t *testing.T) {
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return loginTokenResponse("provider"), nil }), listener)
	db := openFixtureSQL(t, f.path)
	if _, err := db.Exec(`CREATE TRIGGER reject_login BEFORE INSERT ON accounts BEGIN SELECT RAISE(FAIL, 'secret-database-error'); END`); err != nil {
		t.Fatal(err)
	}
	instructions, err := f.manager.StartLogin(t.Context(), "Primary")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)

	response := getCallback(t, callback+"?state="+url.QueryEscape(state)+"&code=secret-code")

	if response.status != 400 || strings.Contains(response.body, "secret-") {
		t.Fatal("failure response was successful or unsafe")
	}
	if status := f.manager.LoginStatus(); status.State != codexoauth.LoginFailed {
		t.Fatalf("state = %s, want failed", status.State)
	}
	records, err := f.store.ListAccounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatal("failed login saved an account")
	}
}

func TestReconnectCanChangeProviderIdentity(t *testing.T) {
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return loginTokenResponse("different-provider"), nil }), listener)
	old := saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	instructions, err := f.manager.StartReconnect(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)

	response := getCallback(t, callback+"?state="+url.QueryEscape(state)+"&code=code")

	if response.status != 200 {
		t.Fatalf("callback status = %d", response.status)
	}
	got, err := f.manager.CurrentAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if got.Identity() != old.Identity() || got.Credentials().ChatGPTAccountID != "different-provider" {
		t.Fatal("reconnect did not preserve local identity and replace provider identity")
	}
}

func TestLateReconnectCannotUndoDisable(t *testing.T) {
	listener, callback := callbackListener(t)
	f, entered, release := blockedOAuthFixture(t, loginTokenResponse("different-provider"), listener)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Hour))
	instructions, err := f.manager.StartReconnect(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)
	result := make(chan error, 1)
	go func() {
		response, err := http.Get(callback + "?state=" + url.QueryEscape(state) + "&code=code")
		if response != nil {
			response.Body.Close()
		}
		result <- err
	}()
	<-entered

	if err := f.manager.Disable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	if f.manager.LoginStatus().State == codexoauth.LoginSucceeded {
		t.Fatal("stale reconnect reported success")
	}
	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if record.Enabled {
		t.Fatal("disabled account enabled again")
	}
}

func TestLateReconnectCannotWriteAfterReenable(t *testing.T) {
	listener, callback := callbackListener(t)
	f, entered, release := blockedOAuthFixture(t, loginTokenResponse("different-provider"), listener)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Hour))
	instructions, err := f.manager.StartReconnect(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)
	result := make(chan error, 1)
	go func() {
		response, err := http.Get(callback + "?state=" + url.QueryEscape(state) + "&code=code")
		if response != nil {
			response.Body.Close()
		}
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

	if got := f.manager.LoginStatus(); got.State != codexoauth.LoginCanceled || got.LoginID != instructions.LoginID {
		t.Fatalf("late reconnect changed canceled status: %+v", got)
	}
	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if !record.Enabled || record.Account.Credentials().RefreshToken != "old-refresh" {
		t.Fatal("stale reconnect changed credentials after re-enablement")
	}
}

func TestLateReconnectCannotUndoDelete(t *testing.T) {
	listener, callback := callbackListener(t)
	f, entered, release := blockedOAuthFixture(t, loginTokenResponse("different-provider"), listener)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Hour))
	instructions, err := f.manager.StartReconnect(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)
	result := make(chan error, 1)
	go func() {
		response, err := http.Get(callback + "?state=" + url.QueryEscape(state) + "&code=code")
		if response != nil {
			response.Body.Close()
		}
		result <- err
	}()
	<-entered

	if err := f.manager.Delete(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	if f.manager.LoginStatus().State == codexoauth.LoginSucceeded {
		t.Fatal("stale reconnect reported success")
	}
	if _, err := f.store.GetAccount(t.Context(), "one"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("deleted account recreated")
	}
}

func TestLateReconnectCannotUndoReplacement(t *testing.T) {
	listener, callback := callbackListener(t)
	f, entered, release := blockedOAuthFixture(t, loginTokenResponse("different-provider"), listener)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Hour))
	instructions, err := f.manager.StartReconnect(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)
	result := make(chan error, 1)
	go func() {
		response, err := http.Get(callback + "?state=" + url.QueryEscape(state) + "&code=code")
		if response != nil {
			response.Body.Close()
		}
		result <- err
	}()
	<-entered

	previous, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReplaceAccountCredentialsIfUnchanged(t.Context(), previous, account.OAuthCredentials{ChatGPTAccountID: "newer-provider", AccessToken: "newer-access", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	if f.manager.LoginStatus().State == codexoauth.LoginSucceeded {
		t.Fatal("stale reconnect reported success")
	}
	record, err := f.store.GetAccount(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if record.Account.Credentials().AccessToken != "newer-access" {
		t.Fatal("late reconnect overwrote newer credentials")
	}
}

func TestPendingLoginExpiresAndCancelAllowsAnother(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("unexpected exchange") }), nil)
		first, err := f.manager.StartLogin(t.Context(), "Primary")
		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(10 * time.Minute)
		if status := f.manager.LoginStatus(); status.State != codexoauth.LoginExpired {
			t.Fatalf("state = %s, want expired", status.State)
		}
		second, err := f.manager.StartLogin(t.Context(), "Secondary")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.manager.CancelLogin(t.Context(), second.LoginID); err != nil {
			t.Fatal(err)
		}
		if f.manager.LoginStatus().State != codexoauth.LoginCanceled {
			t.Fatal("login was not canceled")
		}
		third, err := f.manager.StartLogin(t.Context(), "Third")
		if err != nil {
			t.Fatal(err)
		}

		if first.AccountID == second.AccountID || second.AccountID == third.AccountID {
			t.Fatal("new logins reused local IDs")
		}
	})
}

func TestLoginDenialConsumesStateWithoutExchange(t *testing.T) {
	var calls atomic.Int32
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return loginTokenResponse("provider"), nil }), listener)
	instructions, err := f.manager.StartLogin(t.Context(), "Primary")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)
	callback += "?state=" + url.QueryEscape(state)

	denial := getCallback(t, callback+"&error=access_denied&error_description=secret-provider-error")
	replay := getCallback(t, callback+"&code=code")

	if denial.status != 400 || replay.status != 400 || calls.Load() != 0 {
		t.Fatal("denial or replay reached token exchange")
	}
	if strings.Contains(denial.body, "secret-provider-error") {
		t.Fatal("callback exposed provider error")
	}
	if f.manager.LoginStatus().State != codexoauth.LoginFailed {
		t.Fatal("denial did not finish login")
	}
}

func TestCanceledLoginCannotOverwriteNewLogin(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return loginTokenResponse("old-provider"), nil
		}
		return loginTokenResponse("new-provider"), nil
	}), listener)
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	first, err := f.manager.StartLogin(t.Context(), "Old")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, first)
	oldDone := make(chan error, 1)
	go func() {
		r, err := http.Get(callback + "?state=" + url.QueryEscape(state) + "&code=old")
		if r != nil {
			r.Body.Close()
		}
		oldDone <- err
	}()
	<-entered

	if err := f.manager.CancelLogin(t.Context(), first.LoginID); err != nil {
		t.Fatal(err)
	}
	next, err := f.manager.StartLogin(t.Context(), "New")
	if err != nil {
		t.Fatal(err)
	}
	nextState := authorizationState(t, next)
	response := getCallback(t, callback+"?state="+url.QueryEscape(nextState)+"&code=new")
	if response.status != 200 {
		t.Fatalf("new callback=%d", response.status)
	}
	unblock()
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}

	status := f.manager.LoginStatus()
	if status.State != codexoauth.LoginSucceeded || status.AccountID != next.AccountID {
		t.Fatalf("latest status=%+v", status)
	}
	if _, err := f.store.GetAccount(t.Context(), first.AccountID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("old account error=%v", err)
	}
	record, err := f.store.GetAccount(t.Context(), next.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Account.Credentials().ChatGPTAccountID != "new-provider" {
		t.Fatal("old login replaced new identity")
	}
}

func TestCloseJoinsReplacedCanceledLoginJob(t *testing.T) {
	entered, canceled, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release
		close(exited)
		return nil, r.Context().Err()
	}), listener)
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	first, err := f.manager.StartLogin(t.Context(), "Old")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, first)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		r, _ := http.Get(callback + "?state=" + url.QueryEscape(state) + "&code=old")
		if r != nil {
			r.Body.Close()
		}
	}()
	<-entered
	if err := f.manager.CancelLogin(t.Context(), first.LoginID); err != nil {
		t.Fatal(err)
	}
	<-canceled
	if _, err := f.manager.StartLogin(t.Context(), "New"); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- f.manager.Close() }()

	select {
	case err := <-closed:
		t.Fatalf("Close returned before old job cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Close did not join removed job")
	}
	<-requestDone
}

func callbackListener(t *testing.T) (net.Listener, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener, "http://" + listener.Addr().String() + "/auth/callback"
}

func loginTokenResponse(id string) *http.Response {
	return tokenResponseJSON(200, fmt.Sprintf(`{"access_token":"login-access","refresh_token":"login-refresh","expires_in":3600,"id_token":%q}`, identityToken(id, false)))
}

type callbackResponse struct {
	status                int
	body, cache, referrer string
}

func getCallback(t *testing.T, address string) callbackResponse {
	t.Helper()
	response, err := http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return callbackResponse{
		status: response.StatusCode, body: string(body),
		cache: response.Header.Get("Cache-Control"), referrer: response.Header.Get("Referrer-Policy"),
	}
}

func authorizationState(t *testing.T, instructions codexoauth.LoginInstructions) string {
	t.Helper()
	parsed, err := url.Parse(instructions.URL)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("authorization URL has no state")
	}
	return state
}
