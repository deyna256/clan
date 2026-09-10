package account_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/account"
)

const (
	apiKeySecret   = "distinctive-api-key-secret"
	accessSecret   = "distinctive-access-token-secret"
	refreshSecret  = "distinctive-refresh-token-secret"
	providerSecret = "distinctive-provider-secret"
)

func TestNewPreservesIdentityAndCredentials(t *testing.T) {
	tests := []struct {
		name        string
		credentials account.Credentials
	}{
		{name: "API key", credentials: account.APIKeyCredentials{Key: " api-key-secret "}},
		{
			name: "OAuth",
			credentials: account.OAuthCredentials{
				AccessToken:  " access-token-secret ",
				RefreshToken: " refresh-token-secret ",
				ExpiresAt:    time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
				ProviderData: map[string]string{"tenant": "provider-value"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := account.Identity{ID: " account-1 ", Name: " Primary account ", UpstreamID: " upstream-1 "}

			got, err := account.New(identity, tt.credentials)

			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			if got.Identity() != identity {
				t.Errorf("identity = %+v, want %+v", got.Identity(), identity)
			}
			if !reflect.DeepEqual(got.Credentials(), tt.credentials) {
				t.Errorf("credentials = %#v, want %#v", got.Credentials(), tt.credentials)
			}
		})
	}
}

func TestNewRejectsInvalidIdentityWithoutExposingCredentials(t *testing.T) {
	identities := []struct {
		name      string
		identity  account.Identity
		wantField string
	}{
		{name: "missing id", identity: account.Identity{Name: "Primary", UpstreamID: "upstream"}, wantField: "id"},
		{name: "blank id", identity: account.Identity{ID: " \t\n", Name: "Primary", UpstreamID: "upstream"}, wantField: "id"},
		{name: "missing name", identity: account.Identity{ID: "account", UpstreamID: "upstream"}, wantField: "name"},
		{name: "blank name", identity: account.Identity{ID: "account", Name: " \t\n", UpstreamID: "upstream"}, wantField: "name"},
		{name: "missing upstream", identity: account.Identity{ID: "account", Name: "Primary"}, wantField: "upstream"},
		{name: "blank upstream", identity: account.Identity{ID: "account", Name: "Primary", UpstreamID: " \t\n"}, wantField: "upstream"},
	}
	variants := []struct {
		name        string
		credentials account.Credentials
	}{
		{name: "API key", credentials: account.APIKeyCredentials{Key: apiKeySecret}},
		{name: "OAuth", credentials: account.OAuthCredentials{
			AccessToken: accessSecret, RefreshToken: refreshSecret,
			ProviderData: map[string]string{"secret": providerSecret},
		}},
	}
	for _, variant := range variants {
		for _, tt := range identities {
			t.Run(variant.name+"/"+tt.name, func(t *testing.T) {
				got, err := account.New(tt.identity, variant.credentials)

				if err == nil {
					t.Fatal("New() succeeded with invalid identity")
				}
				if got.Identity() != (account.Identity{}) || got.Credentials() != nil {
					t.Error("New() returned a partial account on failure")
				}
				if !strings.Contains(" "+err.Error()+" ", " "+tt.wantField+" ") {
					t.Errorf("error %q does not identify field %q", err, tt.wantField)
				}
				assertNoSecrets(t, err.Error())
			})
		}
	}
}

func TestNewRejectsInvalidCredentials(t *testing.T) {
	tests := []struct {
		name        string
		credentials account.Credentials
		wantField   string
	}{
		{name: "no variant", wantField: "credentials"},
		{name: "empty API key", credentials: account.APIKeyCredentials{}, wantField: "API key"},
		{name: "blank API key", credentials: account.APIKeyCredentials{Key: " \t\n"}, wantField: "API key"},
		{name: "empty access token", credentials: account.OAuthCredentials{
			RefreshToken: refreshSecret, ProviderData: map[string]string{"secret": providerSecret},
		}, wantField: "access token"},
		{name: "blank access token", credentials: account.OAuthCredentials{AccessToken: " \t\n", RefreshToken: refreshSecret}, wantField: "access token"},
		{name: "nil API key pointer", credentials: (*account.APIKeyCredentials)(nil), wantField: "credentials"},
		{name: "nil OAuth pointer", credentials: (*account.OAuthCredentials)(nil), wantField: "credentials"},
		{name: "API key pointer", credentials: &account.APIKeyCredentials{Key: apiKeySecret}, wantField: "credentials"},
		{name: "OAuth pointer", credentials: &account.OAuthCredentials{AccessToken: accessSecret}, wantField: "credentials"},
		{
			name: "embedded interface",
			credentials: struct{ account.Credentials }{
				Credentials: account.APIKeyCredentials{Key: apiKeySecret},
			},
			wantField: "credentials",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := account.Identity{ID: "account", Name: "Primary", UpstreamID: "upstream"}

			got, err := account.New(identity, tt.credentials)

			if err == nil {
				t.Fatal("New() succeeded with invalid credentials")
			}
			if got.Identity() != (account.Identity{}) || got.Credentials() != nil {
				t.Error("New() returned a partial account on failure")
			}
			if !strings.Contains(" "+err.Error()+" ", " "+tt.wantField+" ") {
				t.Errorf("error %q does not identify field %q", err, tt.wantField)
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
			identity := account.Identity{ID: "account", Name: "Imported account", UpstreamID: "upstream"}
			credentials := account.OAuthCredentials{AccessToken: accessSecret, ExpiresAt: tt.expiresAt}

			got, err := account.New(identity, credentials)

			if err != nil {
				t.Fatalf("New() rejected a structurally valid OAuth record: %v", err)
			}
			if !reflect.DeepEqual(got.Credentials(), credentials) {
				t.Errorf("credentials = %#v, want %#v", got.Credentials(), credentials)
			}
		})
	}
}

func TestIdentityDoesNotExposeCredentials(t *testing.T) {
	for _, credentials := range []account.Credentials{
		account.APIKeyCredentials{Key: apiKeySecret},
		account.OAuthCredentials{AccessToken: accessSecret, RefreshToken: refreshSecret,
			ProviderData: map[string]string{"secret": providerSecret}},
	} {
		t.Run(fmt.Sprintf("%T", credentials), func(t *testing.T) {
			a, err := account.New(account.Identity{ID: "account", Name: "Primary", UpstreamID: "upstream"}, credentials)
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
		})
	}
}

func TestNewOwnsCredentialData(t *testing.T) {
	identity := account.Identity{ID: "account", Name: "Primary", UpstreamID: "upstream"}
	credentials := account.OAuthCredentials{
		AccessToken: accessSecret, RefreshToken: refreshSecret,
		ProviderData: map[string]string{"secret": providerSecret},
	}
	a, err := account.New(identity, credentials)
	if err != nil {
		t.Fatal(err)
	}

	identity.Name = "Changed"
	credentials.AccessToken = "replacement"
	credentials.RefreshToken = "replacement"
	credentials.ProviderData["secret"] = "replacement"
	credentials.ProviderData["extra"] = "unexpected"

	wantIdentity := account.Identity{ID: "account", Name: "Primary", UpstreamID: "upstream"}
	if a.Identity() != wantIdentity {
		t.Errorf("identity = %+v, want %+v", a.Identity(), wantIdentity)
	}
	wantCredentials := account.OAuthCredentials{
		AccessToken: accessSecret, RefreshToken: refreshSecret,
		ProviderData: map[string]string{"secret": providerSecret},
	}
	if !reflect.DeepEqual(a.Credentials(), wantCredentials) {
		t.Errorf("credentials changed with input: got %#v, want %#v", a.Credentials(), wantCredentials)
	}
}

func TestCredentialReadsAreIndependent(t *testing.T) {
	a, err := account.New(
		account.Identity{ID: "account", Name: "Primary", UpstreamID: "upstream"},
		account.OAuthCredentials{AccessToken: accessSecret, ProviderData: map[string]string{"secret": providerSecret}},
	)
	if err != nil {
		t.Fatal(err)
	}
	first := a.Credentials().(account.OAuthCredentials)
	second := a.Credentials().(account.OAuthCredentials)

	delete(first.ProviderData, "secret")
	first.ProviderData["extra"] = "unexpected"
	latest := a.Credentials().(account.OAuthCredentials)

	want := map[string]string{"secret": providerSecret}
	if !maps.Equal(second.ProviderData, want) {
		t.Errorf("another credential copy changed: got %v, want %v", second.ProviderData, want)
	}
	if !maps.Equal(latest.ProviderData, want) {
		t.Errorf("stored credentials changed: got %v, want %v", latest.ProviderData, want)
	}
}

func assertNoSecrets(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{apiKeySecret, accessSecret, refreshSecret, providerSecret} {
		if strings.Contains(text, secret) {
			t.Errorf("safe output contains credential secret %q: %s", secret, text)
		}
	}
}
