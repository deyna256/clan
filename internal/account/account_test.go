package account_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/account"
)

const (
	accessSecret  = "distinctive-access-token-secret"
	refreshSecret = "distinctive-refresh-token-secret"
)

func TestNewPreservesIdentityAndCredentials(t *testing.T) {
	identity := account.Identity{ID: " account-1 ", Name: " Primary account "}
	credentials := account.OAuthCredentials{
		AccessToken:  " access-token-secret ",
		RefreshToken: " refresh-token-secret ",
		ExpiresAt:    time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	got, err := account.New(identity, credentials)

	if err != nil {
		t.Fatal(err)
	}
	if got.Identity() != identity {
		t.Errorf("identity = %+v, want %+v", got.Identity(), identity)
	}
	if got.Credentials() != credentials {
		t.Errorf("credentials = %#v, want %#v", got.Credentials(), credentials)
	}
}

func TestNewRejectsInvalidIdentityWithoutExposingCredentials(t *testing.T) {
	tests := []struct {
		name      string
		identity  account.Identity
		wantField string
	}{
		{name: "missing id", identity: account.Identity{Name: "Primary"}, wantField: "id"},
		{name: "blank id", identity: account.Identity{ID: " \t\n", Name: "Primary"}, wantField: "id"},
		{name: "missing name", identity: account.Identity{ID: "account"}, wantField: "name"},
		{name: "blank name", identity: account.Identity{ID: "account", Name: " \t\n"}, wantField: "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credentials := oauthCredentials()

			got, err := account.New(tt.identity, credentials)

			if err == nil {
				t.Fatal("New() succeeded with invalid identity")
			}
			if got != (account.Account{}) {
				t.Error("New() returned a partial account on failure")
			}
			if !strings.Contains(" "+err.Error()+" ", " "+tt.wantField+" ") {
				t.Errorf("error %q does not identify field %q", err, tt.wantField)
			}
			assertNoSecrets(t, err.Error())
		})
	}
}

func TestNewRejectsInvalidCredentials(t *testing.T) {
	for _, token := range []string{"", " \t\n"} {
		t.Run(fmt.Sprintf("token %q", token), func(t *testing.T) {
			credentials := oauthCredentials()
			credentials.AccessToken = token

			got, err := account.New(testIdentity(), credentials)

			if err == nil {
				t.Fatal("New() succeeded with invalid credentials")
			}
			if got != (account.Account{}) {
				t.Error("New() returned a partial account on failure")
			}
			if !strings.Contains(err.Error(), "access token") {
				t.Errorf("error %q does not identify access token", err)
			}
			assertNoSecrets(t, err.Error())
		})
	}
}

func TestOAuthRecordDoesNotRequireFreshnessOrRefreshToken(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
	}{
		{name: "unknown expiry"},
		{name: "expired", expiresAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := testIdentity()
			credentials := account.OAuthCredentials{AccessToken: accessSecret, ExpiresAt: tt.expiresAt}

			got, err := account.New(identity, credentials)

			if err != nil {
				t.Fatalf("New() rejected a structurally valid OAuth record: %v", err)
			}
			if got.Credentials() != credentials {
				t.Errorf("credentials = %#v, want %#v", got.Credentials(), credentials)
			}
		})
	}
}

func TestIdentityDoesNotExposeCredentials(t *testing.T) {
	a, err := account.New(testIdentity(), oauthCredentials())
	if err != nil {
		t.Fatal(err)
	}

	identity := a.Identity()
	encoded, err := json.Marshal(identity)
	formatted := fmt.Sprintf("%#v", identity)

	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, string(encoded))
	assertNoSecrets(t, formatted)
}

func assertNoSecrets(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{accessSecret, refreshSecret} {
		if strings.Contains(text, secret) {
			t.Errorf("safe output contains credential secret %q: %s", secret, text)
		}
	}
}

func testIdentity() account.Identity {
	return account.Identity{ID: "account", Name: "Primary"}
}

func oauthCredentials() account.OAuthCredentials {
	return account.OAuthCredentials{AccessToken: accessSecret, RefreshToken: refreshSecret}
}
