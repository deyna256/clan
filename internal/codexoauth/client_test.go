package codexoauth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/retry"
)

func TestBegin(t *testing.T) {
	client, err := codexoauth.NewClient(&http.Client{}, "")
	if err != nil {
		t.Fatal(err)
	}

	a, b := client.Begin(), client.Begin()
	u, err := url.Parse(a.URL)

	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/oauth/authorize" {
		t.Fatalf("unexpected authorization endpoint: %s", u.Redacted())
	}
	digest := sha256.Sum256([]byte(a.Verifier))
	want := url.Values{
		"client_id": {"app_EMoamEEZ73f0CkXaXp7hrann"}, "response_type": {"code"},
		"redirect_uri": {"http://localhost:1455/auth/callback"}, "scope": {"openid profile email offline_access"},
		"state": {a.State}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])},
		"code_challenge_method": {"S256"}, "id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow": {"true"}, "originator": {"clan"},
	}
	if u.Query().Encode() != want.Encode() {
		t.Errorf("authorization parameters do not match contract")
	}
	if a.State == a.Verifier || a.State == b.State || a.Verifier == b.Verifier {
		t.Error("state and verifier must be independent per login")
	}
	for _, value := range []string{a.State, a.Verifier} {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(decoded) < 32 {
			t.Error("insufficient random state or verifier")
		}
	}
}

func TestExchange(t *testing.T) {
	var form url.Values
	var authHeader, contentType, method, path string
	client := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		authHeader, contentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form = r.PostForm
		fmt.Fprintf(w, `{"access_token":"access","refresh_token":"refresh","id_token":%q,"expires_in":3600,"token_type":"Bearer"}`, identityToken("workspace", false))
	})
	before := time.Now()

	got, err := client.Exchange(t.Context(), "callback-code", "private-verifier")

	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/oauth/token" || authHeader != "" || contentType != "application/x-www-form-urlencoded" {
		t.Errorf("wrong exchange request: %s %s %q %q", method, path, authHeader, contentType)
	}
	want := url.Values{
		"client_id": {"app_EMoamEEZ73f0CkXaXp7hrann"}, "grant_type": {"authorization_code"},
		"code": {"callback-code"}, "code_verifier": {"private-verifier"},
		"redirect_uri": {"http://localhost:1455/auth/callback"},
	}
	if form.Encode() != want.Encode() {
		t.Errorf("wrong form: %v", form)
	}
	if got.ChatGPTAccountID != "workspace" || got.AccessToken != "access" || got.RefreshToken != "refresh" {
		t.Error("credentials differ from response")
	}
	if got.ExpiresAt.Before(before.Add(time.Hour)) || got.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Errorf("expiry = %v; want one hour from exchange", got.ExpiresAt)
	}
}

func TestRefreshRotations(t *testing.T) {
	previous := account.OAuthCredentials{
		ChatGPTAccountID: "old-workspace", AccessToken: "old-access", RefreshToken: "old-refresh",
		ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	cases := []struct {
		name, body, access, refresh, accountID string
		oldExpiry                              bool
	}{
		{name: "omitted refresh", body: `{"access_token":"new-access","expires_in":120}`, access: "new-access", refresh: "old-refresh", accountID: "old-workspace"},
		{name: "null refresh", body: `{"access_token":"new-access","refresh_token":null,"id_token":null,"expires_in":120}`, access: "new-access", refresh: "old-refresh", accountID: "old-workspace"},
		{name: "rotated refresh only", body: `{"refresh_token":"rotated"}`, access: "old-access", refresh: "rotated", accountID: "old-workspace", oldExpiry: true},
		{name: "expiry without access", body: `{"refresh_token":"rotated","expires_in":0}`, access: "old-access", refresh: "rotated", accountID: "old-workspace", oldExpiry: true},
		{name: "null access", body: `{"access_token":null,"refresh_token":"rotated"}`, access: "old-access", refresh: "rotated", accountID: "old-workspace", oldExpiry: true},
		{name: "changed workspace candidate", body: fmt.Sprintf(`{"id_token":%q}`, identityToken("new-workspace", false)), access: "old-access", refresh: "old-refresh", accountID: "new-workspace", oldExpiry: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var request map[string]string
			var contentType string
			client := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
				contentType = r.Header.Get("Content-Type")
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				io.WriteString(w, tt.body)
			})
			saved := previous
			before := time.Now()

			got, err := client.Refresh(t.Context(), previous)

			if err != nil {
				t.Fatal(err)
			}
			if contentType != "application/json" || len(request) != 3 || request["grant_type"] != "refresh_token" || request["client_id"] != "app_EMoamEEZ73f0CkXaXp7hrann" || request["refresh_token"] != "old-refresh" {
				t.Errorf("wrong refresh request: %s %v", contentType, request)
			}
			if got.AccessToken != tt.access || got.RefreshToken != tt.refresh || got.ChatGPTAccountID != tt.accountID {
				t.Error("unexpected candidate credentials")
			}
			if tt.oldExpiry && got.ExpiresAt != previous.ExpiresAt {
				t.Error("partial rotation changed retained access expiry")
			}
			if !tt.oldExpiry && (got.ExpiresAt.Before(before.Add(2*time.Minute)) || got.ExpiresAt.After(time.Now().Add(2*time.Minute))) {
				t.Errorf("expiry = %v; want two minutes from refresh", got.ExpiresAt)
			}
			if previous != saved {
				t.Error("previous credentials changed")
			}
		})
	}
}

func TestInvalidTokenResponses(t *testing.T) {
	id := identityToken("workspace", false)
	valid := fmt.Sprintf(`"access_token":"access","refresh_token":"refresh","id_token":%q`, id)
	cases := []struct{ name, body string }{
		{name: "unknown expiry", body: `{` + valid + `}`},
		{name: "missing identity", body: `{"access_token":"access","refresh_token":"refresh","expires_in":120}`},
		{name: "missing refresh", body: fmt.Sprintf(`{"access_token":"access","id_token":%q,"expires_in":120}`, id)},
		{name: "unsupported routing", body: fmt.Sprintf(`{"access_token":"access","refresh_token":"refresh","id_token":%q,"expires_in":120}`, identityToken("workspace", true))},
		{name: "unsupported type", body: `{` + valid + `,"expires_in":120,"token_type":"MAC"}`},
		{name: "invalid identity", body: `{"access_token":"access","refresh_token":"refresh","id_token":"bad","expires_in":120}`},
		{name: "header injection", body: fmt.Sprintf(`{"access_token":"access\r\nsecret","refresh_token":"refresh","id_token":%q,"expires_in":120}`, id)},
		{name: "NUL access", body: fmt.Sprintf(`{"access_token":"bad\u0000token","refresh_token":"refresh","id_token":%q,"expires_in":120}`, id)},
		{name: "tab access", body: fmt.Sprintf(`{"access_token":"bad\ttoken","refresh_token":"refresh","id_token":%q,"expires_in":120}`, id)},
		{name: "DEL access", body: fmt.Sprintf(`{"access_token":"bad\u007ftoken","refresh_token":"refresh","id_token":%q,"expires_in":120}`, id)},
		{name: "NUL account ID", body: fmt.Sprintf(`{"access_token":"access","refresh_token":"refresh","id_token":%q,"expires_in":120}`, jwt(`{"https://api.openai.com/auth":{"chatgpt_account_id":"bad\u0000account"}}`))},
		{name: "DEL account ID", body: fmt.Sprintf(`{"access_token":"access","refresh_token":"refresh","id_token":%q,"expires_in":120}`, jwt(`{"https://api.openai.com/auth":{"chatgpt_account_id":"bad\u007faccount"}}`))},
		{name: "trailing JSON", body: `{` + valid + `,"expires_in":120} {}`},
		{name: "oversized body", body: `{` + valid + `,"expires_in":120}` + strings.Repeat(" ", 1<<20)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			client := tokenServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tt.body) })

			got, err := client.Exchange(t.Context(), "code", "verifier")

			assertFailure(t, err, codexoauth.FailureInvalidResponse)
			if got != (account.OAuthCredentials{}) {
				t.Error("invalid response exposed candidate credentials")
			}
		})
	}
}

func TestRefreshRejectsMalformedRotations(t *testing.T) {
	previous := account.OAuthCredentials{
		ChatGPTAccountID: "workspace", AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	cases := []struct{ name, body string }{
		{name: "empty refresh", body: `{"refresh_token":""}`},
		{name: "blank refresh", body: `{"refresh_token":"  "}`},
		{name: "empty access", body: `{"access_token":""}`},
		{name: "missing access expiry", body: `{"access_token":"new"}`},
		{name: "zero access expiry", body: `{"access_token":"new","expires_in":0}`},
		{name: "invalid identity", body: `{"id_token":"bad"}`},
		{name: "empty identity", body: `{"id_token":""}`},
		{name: "NUL access", body: `{"access_token":"bad\u0000token","expires_in":120}`},
		{name: "DEL refresh", body: `{"refresh_token":"bad\u007ftoken"}`},
		{name: "error in successful response", body: `{"error":"invalid_grant"}`},
		{name: "numeric refresh", body: `{"refresh_token":123}`},
		{name: "null response", body: `null`},
		{name: "array response", body: `[]`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			client := tokenServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tt.body) })

			_, err := client.Refresh(t.Context(), previous)

			assertFailure(t, err, codexoauth.FailureInvalidResponse)
		})
	}
}

func TestAccessJWTExpiry(t *testing.T) {
	const expiry = 4102444800
	access := jwt(`{"exp":4102444800}`)
	client := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"refresh","id_token":%q}`, access, identityToken("workspace", false))
	})

	got, err := client.Exchange(t.Context(), "code", "verifier")

	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt != time.Unix(expiry, 0).UTC() {
		t.Errorf("expiry = %v; want access JWT expiry", got.ExpiresAt)
	}
}

func TestExchangeUnusableLifetimeFallsBackToAccessJWT(t *testing.T) {
	for _, tt := range unusableLifetimes() {
		t.Run(tt.name, func(t *testing.T) {
			client := lifetimeServer(t, jwt(`{"exp":4102444800}`), tt.value)

			got, err := client.Exchange(t.Context(), "code", "verifier")

			if err != nil {
				t.Fatal(err)
			}
			if got.ExpiresAt != time.Unix(4102444800, 0).UTC() {
				t.Errorf("expiry = %v; want access JWT expiry", got.ExpiresAt)
			}
		})
	}
}

func TestExchangeRejectsUnusableLifetimeWithoutAccessJWT(t *testing.T) {
	for _, tt := range unusableLifetimes() {
		t.Run(tt.name, func(t *testing.T) {
			client := lifetimeServer(t, "opaque-access", tt.value)

			got, err := client.Exchange(t.Context(), "code", "verifier")

			assertFailure(t, err, codexoauth.FailureInvalidResponse)
			if got != (account.OAuthCredentials{}) {
				t.Error("invalid expiry exposed candidate credentials")
			}
		})
	}
}

func TestRefreshUnusableLifetimeFallsBackToAccessJWT(t *testing.T) {
	for _, tt := range unusableLifetimes() {
		t.Run(tt.name, func(t *testing.T) {
			client := lifetimeServer(t, jwt(`{"exp":4102444800}`), tt.value)

			got, err := client.Refresh(t.Context(), account.OAuthCredentials{RefreshToken: "previous"})

			if err != nil {
				t.Fatal(err)
			}
			if got.ExpiresAt != time.Unix(4102444800, 0).UTC() {
				t.Errorf("expiry = %v; want access JWT expiry", got.ExpiresAt)
			}
		})
	}
}

func TestRefreshRejectsUnusableLifetimeWithoutAccessJWT(t *testing.T) {
	for _, tt := range unusableLifetimes() {
		t.Run(tt.name, func(t *testing.T) {
			client := lifetimeServer(t, "opaque-access", tt.value)

			got, err := client.Refresh(t.Context(), account.OAuthCredentials{RefreshToken: "previous"})

			assertFailure(t, err, codexoauth.FailureInvalidResponse)
			if got != (account.OAuthCredentials{}) {
				t.Error("invalid expiry exposed candidate credentials")
			}
		})
	}
}

func TestExpiryNormalizationPreservesResponseSize(t *testing.T) {
	access := jwt(`{"exp":4102444800}`)
	body := fmt.Sprintf(`{"access_token":%q,"refresh_token":"refresh","id_token":%q,"expires_in":0,"extra":%q}`,
		access, identityToken("workspace", false), strings.Repeat("<", 200000))
	for _, operation := range []string{"exchange", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			client := tokenServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })

			var got account.OAuthCredentials
			var err error
			if operation == "exchange" {
				got, err = client.Exchange(t.Context(), "code", "verifier")
			} else {
				got, err = client.Refresh(t.Context(), account.OAuthCredentials{RefreshToken: "previous"})
			}

			if err != nil {
				t.Fatal(err)
			}
			if got.AccessToken != access || got.ExpiresAt != time.Unix(4102444800, 0).UTC() {
				t.Error("normalization changed access credentials or lost expiry fallback")
			}
		})
	}
}

func TestTruncatedResponsesPreserveStatusAndCooldown(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		readError error
		wantCause error
		kind      codexoauth.FailureKind
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, readError: io.ErrUnexpectedEOF, kind: codexoauth.FailureRejected},
		{name: "rate limited", status: http.StatusTooManyRequests, readError: io.ErrUnexpectedEOF, kind: codexoauth.FailureTransient},
		{name: "server failure canceled", status: http.StatusServiceUnavailable, readError: context.Canceled, wantCause: context.Canceled, kind: codexoauth.FailureTransient},
		{name: "success truncated", status: http.StatusOK, readError: io.ErrUnexpectedEOF, kind: codexoauth.FailureTransient},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			for _, operation := range []string{"exchange", "refresh"} {
				t.Run(operation, func(t *testing.T) {
					client, err := codexoauth.NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: tt.status,
							Header:     http.Header{"Content-Type": {"application/json"}, "Retry-After": {"3600"}},
							Body:       io.NopCloser(errorReader{err: tt.readError}),
						}, nil
					})}, "")
					if err != nil {
						t.Fatal(err)
					}
					before := time.Now()

					if operation == "exchange" {
						_, err = client.Exchange(t.Context(), "code", "verifier")
					} else {
						_, err = client.Refresh(t.Context(), account.OAuthCredentials{RefreshToken: "previous"})
					}

					failure := assertFailure(t, err, tt.kind)
					if failure.StatusCode != tt.status {
						t.Errorf("status = %d; want %d", failure.StatusCode, tt.status)
					}
					if tt.status != http.StatusOK && (failure.RetryAfter.Kind != retry.RetryAt || failure.RetryAfter.Until.Before(before.Add(time.Hour))) {
						t.Error("lost authoritative Retry-After after body read error")
					}
					if cause := errors.Unwrap(err); cause != tt.wantCause {
						t.Errorf("unwrapped error = %v; want %v", cause, tt.wantCause)
					}
				})
			}
		})
	}
}

func TestProviderFailuresAreSafeAndSingleAttempt(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		kind   codexoauth.FailureKind
	}{
		{name: "invalid grant", status: 400, body: `{"error":"invalid_grant","error_description":"secret"}`, kind: codexoauth.FailureRejected},
		{name: "nested reused", status: 400, body: `{"error":{"code":"refresh_token_reused","message":"secret"}}`, kind: codexoauth.FailureRejected},
		{name: "top level invalidated", status: 400, body: `{"code":"refresh_token_invalidated"}`, kind: codexoauth.FailureRejected},
		{name: "null error top level code", status: 400, body: `{"error":null,"code":"refresh_token_invalidated"}`, kind: codexoauth.FailureRejected},
		{name: "empty error top level code", status: 400, body: `{"error":"","code":"refresh_token_expired"}`, kind: codexoauth.FailureRejected},
		{name: "unauthorized", status: 401, body: `secret`, kind: codexoauth.FailureRejected},
		{name: "rate limit", status: 429, body: `secret`, kind: codexoauth.FailureTransient},
		{name: "server failure", status: 503, body: `{"error":"invalid_grant"}`, kind: codexoauth.FailureTransient},
		{name: "other bad request", status: 400, body: `secret`, kind: codexoauth.FailureInvalidResponse},
		{name: "redirect", status: 307, body: `secret`, kind: codexoauth.FailureInvalidResponse},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			for _, operation := range []string{"exchange", "refresh"} {
				t.Run(operation, func(t *testing.T) {
					calls := 0
					client := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
						calls++
						w.Header().Set("Retry-After", "2")
						w.Header().Set("Location", "/secret")
						w.WriteHeader(tt.status)
						io.WriteString(w, tt.body)
					})

					var err error
					if operation == "exchange" {
						_, err = client.Exchange(t.Context(), "secret-code", "secret-verifier")
					} else {
						_, err = client.Refresh(t.Context(), account.OAuthCredentials{RefreshToken: "secret-refresh"})
					}

					failure := assertFailure(t, err, tt.kind)
					if failure.StatusCode != tt.status || calls != 1 {
						t.Errorf("status=%d calls=%d; want %d and 1", failure.StatusCode, calls, tt.status)
					}
					if failure.RetryAfter.Kind != retry.RetryAt {
						t.Error("missing Retry-After")
					}
					if errors.Unwrap(err) != nil || strings.Contains(fmt.Sprintf("%+v", err), "secret") {
						t.Error("error exposed provider details")
					}
				})
			}
		})
	}
}
func TestTransportFailuresAndCancellation(t *testing.T) {
	cases := []struct {
		name             string
		cause, wantCause error
	}{
		{name: "transport error", cause: errors.New("secret transport URL")},
		{name: "canceled", cause: context.Canceled, wantCause: context.Canceled},
		{name: "deadline exceeded", cause: context.DeadlineExceeded, wantCause: context.DeadlineExceeded},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, tt.cause })
			client, err := codexoauth.NewClient(&http.Client{Transport: transport}, "")
			if err != nil {
				t.Fatal(err)
			}

			_, err = client.Exchange(t.Context(), "code", "verifier")

			assertFailure(t, err, codexoauth.FailureTransient)
			if cause := errors.Unwrap(err); cause != tt.wantCause {
				t.Errorf("unwrapped error = %v; want %v", cause, tt.wantCause)
			}
		})
	}
}

func TestNewClientRejectsUnsafeIssuer(t *testing.T) {
	for _, issuer := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com?secret=value", "https://example.com#secret", "https://example.com/path", "://bad"} {
		_, err := codexoauth.NewClient(&http.Client{}, issuer)
		if err == nil {
			t.Errorf("accepted issuer %q", issuer)
		}
	}
	if _, err := codexoauth.NewClient(nil, ""); err == nil {
		t.Error("accepted nil HTTP client")
	}
}

func TestClientTimeoutPreservesDeadline(t *testing.T) {
	cases := []struct {
		name, operation string
		bodyStarted     bool
		status          int
		kind            codexoauth.FailureKind
	}{
		{name: "exchange stalled headers", operation: "exchange", kind: codexoauth.FailureTransient},
		{name: "exchange stalled body", operation: "exchange", bodyStarted: true, status: http.StatusOK, kind: codexoauth.FailureTransient},
		{name: "refresh stalled headers", operation: "refresh", kind: codexoauth.FailureTransient},
		{name: "refresh stalled body", operation: "refresh", bodyStarted: true, status: http.StatusOK, kind: codexoauth.FailureTransient},
		{name: "exchange unauthorized stalled body", operation: "exchange", bodyStarted: true, status: http.StatusUnauthorized, kind: codexoauth.FailureRejected},
		{name: "refresh unauthorized stalled body", operation: "refresh", bodyStarted: true, status: http.StatusUnauthorized, kind: codexoauth.FailureRejected},
		{name: "exchange rate limited stalled body", operation: "exchange", bodyStarted: true, status: http.StatusTooManyRequests, kind: codexoauth.FailureTransient},
		{name: "refresh rate limited stalled body", operation: "refresh", bodyStarted: true, status: http.StatusTooManyRequests, kind: codexoauth.FailureTransient},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			release, flushed, received := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.bodyStarted {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "3600")
					w.WriteHeader(tt.status)
					io.WriteString(w, `{"access_token":`)
					w.(http.Flusher).Flush()
					close(flushed)
				}
				<-release
			}))
			defer server.Close()
			defer close(release)
			httpClient := server.Client()
			transport := httpClient.Transport
			httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(r)
				if response != nil {
					close(received)
				}
				return response, err
			})
			httpClient.Timeout = 250 * time.Millisecond
			client, err := codexoauth.NewClient(httpClient, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			before := time.Now()

			if tt.operation == "exchange" {
				_, err = client.Exchange(t.Context(), "code", "verifier")
			} else {
				_, err = client.Refresh(t.Context(), account.OAuthCredentials{RefreshToken: "refresh"})
			}

			if tt.bodyStarted {
				select {
				case <-flushed:
				default:
					t.Fatal("timeout occurred before the response body started")
				}
				select {
				case <-received:
				default:
					t.Fatal("timeout occurred before the client received response headers")
				}
			}
			failure := assertFailure(t, err, tt.kind)
			if failure.StatusCode != tt.status {
				t.Errorf("status = %d; want %d", failure.StatusCode, tt.status)
			}
			if tt.bodyStarted && tt.status != http.StatusOK {
				if failure.RetryAfter.Kind != retry.RetryAt || failure.RetryAfter.Until.Before(before.Add(time.Hour)) {
					t.Error("body timeout lost authoritative Retry-After")
				}
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Error("HTTP client timeout lost deadline sentinel")
			}
		})
	}
}

func unusableLifetimes() []struct{ name, value string } {
	return []struct{ name, value string }{
		{name: "null", value: `null`},
		{name: "zero", value: `0`},
		{name: "negative", value: `-1`},
		{name: "fractional", value: `1.5`},
		{name: "text", value: `"bad"`},
		{name: "overflow", value: `9223372036854775807`},
		{name: "object", value: `{}`},
	}
}

func lifetimeServer(t *testing.T, access, lifetime string) *codexoauth.Client {
	t.Helper()
	return tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"refresh","id_token":%q,"expires_in":%s}`,
			access, identityToken("workspace", false), lifetime)
	})
}

func tokenServer(t *testing.T, handler http.HandlerFunc) *codexoauth.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client, err := codexoauth.NewClient(server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func identityToken(id string, fedramp bool) string {
	return jwt(fmt.Sprintf(`{"exp":4202444800,"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_account_is_fedramp":%t}}`, id, fedramp))
}

func jwt(payload string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}

func assertFailure(t *testing.T, err error, kind codexoauth.FailureKind) *codexoauth.Failure {
	t.Helper()
	failure, ok := errors.AsType[*codexoauth.Failure](err)
	if !ok || failure.Kind != kind {
		t.Fatalf("error = %v; want %s failure", err, kind)
	}
	return failure
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
