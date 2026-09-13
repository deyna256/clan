package codexoauth_test

import (
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/codexoauth"
)

func TestManagerDisableEnablePreservesRequiredSignIn(t *testing.T) {
	var calls atomic.Int32
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return tokenResponseJSON(400, `{"error":"invalid_grant"}`), nil
		}
		return tokenResponseJSON(503, `{}`), nil
	}), nil)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrNeedsSignIn) {
		t.Fatalf("initial refresh = %v, want needs sign-in", err)
	}

	for range 2 {
		for range 2 {
			if err := f.manager.Disable(t.Context(), "one"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrDisabled) {
				t.Fatalf("disabled account = %v, want disabled", err)
			}
		}
		if err := f.manager.Enable(t.Context(), "one"); err != nil {
			t.Fatal(err)
		}
		status, err := f.manager.Status(t.Context(), "one")
		if err != nil || status.State != codexoauth.StateNeedsSignIn {
			t.Fatalf("reenabled status = %s, %v; want needs_sign_in", status.State, err)
		}
		statuses, err := f.manager.List(t.Context())
		if err != nil || len(statuses) != 1 || statuses[0].State != codexoauth.StateNeedsSignIn {
			t.Fatalf("reenabled statuses = %+v, %v; want needs_sign_in", statuses, err)
		}
		if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrNeedsSignIn) {
			t.Fatalf("reenabled account = %v, want needs sign-in", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestManagerDisabledCredentialReplacementClearsRequiredSignIn(t *testing.T) {
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return tokenResponseJSON(400, `{"error":"invalid_grant"}`), nil
	}), nil)
	old := saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrNeedsSignIn) {
		t.Fatalf("initial refresh = %v, want needs sign-in", err)
	}
	if err := f.manager.Disable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	credentials := old.Credentials()
	credentials.AccessToken = "replacement-access"
	credentials.RefreshToken = "replacement-refresh"
	credentials.ExpiresAt = time.Now().Add(time.Hour)

	if err := f.store.ReplaceAccountCredentials(t.Context(), "one", credentials); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Enable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	status, err := f.manager.Status(t.Context(), "one")
	if err != nil || status.State != codexoauth.StateConnected {
		t.Fatalf("replacement status = %s, %v; want connected", status.State, err)
	}
	got, err := f.manager.CurrentAccount(t.Context(), "one")
	if err != nil || got.Credentials().AccessToken != "replacement-access" {
		t.Fatalf("replacement account error = %v; replacement credentials returned = %t", err,
			got.Credentials().AccessToken == "replacement-access")
	}
}

func TestManagerDeleteClearsRequiredSignIn(t *testing.T) {
	var calls atomic.Int32
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return tokenResponseJSON(400, `{"error":"invalid_grant"}`), nil
		}
		return tokenResponseJSON(200, `{"access_token":"replacement","expires_in":3600}`), nil
	}), nil)
	old := saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrNeedsSignIn) {
		t.Fatalf("initial refresh = %v, want needs sign-in", err)
	}
	if err := f.manager.Disable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}

	if err := f.manager.Delete(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateAccount(t.Context(), old, true); err != nil {
		t.Fatal(err)
	}
	got, err := f.manager.CurrentAccount(t.Context(), "one")
	if err != nil || got.Credentials().AccessToken != "replacement" || calls.Load() != 2 {
		t.Fatalf("recreated account error = %v, refresh calls = %d; want refreshed credentials", err, calls.Load())
	}
}

func TestManagerReconnectClearsRequiredSignInAfterReenable(t *testing.T) {
	var calls atomic.Int32
	listener, callback := callbackListener(t)
	f := managerFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return tokenResponseJSON(400, `{"error":"invalid_grant"}`), nil
		}
		return loginTokenResponse("provider"), nil
	}), listener)
	saveOAuthAccount(t, f.store, "one", time.Now().Add(time.Minute))
	if _, err := f.manager.CurrentAccount(t.Context(), "one"); !errors.Is(err, codexoauth.ErrNeedsSignIn) {
		t.Fatalf("initial refresh = %v, want needs sign-in", err)
	}
	if err := f.manager.Disable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Enable(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}

	instructions, err := f.manager.StartReconnect(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationState(t, instructions)
	response := getCallback(t, callback+"?state="+url.QueryEscape(state)+"&code=code")
	if response.status != http.StatusOK {
		t.Fatalf("reconnect callback = %d, want HTTP 200", response.status)
	}
	status, err := f.manager.Status(t.Context(), "one")
	if err != nil || status.State != codexoauth.StateConnected {
		t.Fatalf("reconnected status = %s, %v; want connected", status.State, err)
	}
	got, err := f.manager.CurrentAccount(t.Context(), "one")
	if err != nil || got.Credentials().AccessToken != "login-access" || calls.Load() != 2 {
		t.Fatalf("reconnected account error = %v, OAuth calls = %d; want login credentials", err, calls.Load())
	}
}
