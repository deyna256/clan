// Package codexoauth implements the browser OAuth protocol for Codex accounts.
package codexoauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/retry"
	"golang.org/x/oauth2"
)

const (
	DefaultIssuer = "https://auth.openai.com"
	RedirectURI   = "http://localhost:1455/auth/callback"
	clientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	maxTokenBody  = 1 << 20
)

// FailureKind classifies an attempt without exposing provider text or secrets.
type FailureKind string

const (
	FailureRejected        FailureKind = "rejected"
	FailureTransient       FailureKind = "transient"
	FailureInvalidResponse FailureKind = "invalid_response"
)

// Failure reports one attempt; its classification does not authorize replay.
type Failure struct {
	Kind       FailureKind
	StatusCode int
	RetryAfter retry.Cooldown
	cause      error
}

func (e *Failure) Error() string { return "codex oauth: " + string(e.Kind) }

// Unwrap exposes only context cancellation or deadline sentinels.
func (e *Failure) Unwrap() error { return e.cause }

// Authorization contains a browser URL and private callback validation values.
// Keep State and Verifier in the pending login; never log this value.
type Authorization struct {
	URL      string
	State    string
	Verifier string
}

// Client performs bounded token operations without redirects or business retries.
// Construct it with NewClient; it is safe for concurrent use.
type Client struct {
	transport http.RoundTripper
	config    oauth2.Config
	timeout   time.Duration
}

// NewClient uses the client's transport and enforces its timeout through operation
// contexts, defaulting and capping it at 30 seconds. Cookies and redirects are not
// used. An empty issuer uses DefaultIssuer. HTTP is allowed only for loopback tests.
// The caller must not mutate the shared transport or supply a retrying transport.
func NewClient(client *http.Client, issuer string) (*Client, error) {
	if client == nil {
		return nil, errors.New("codex oauth: HTTP client is required")
	}
	if issuer == "" {
		issuer = DefaultIssuer
	}
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return nil, errors.New("codex oauth: invalid issuer")
	}
	loopback := u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("codex oauth: issuer requires HTTPS")
	}
	hasSuffix := u.RawQuery != "" || u.ForceQuery || strings.Contains(issuer, "#")
	if u.User != nil || hasSuffix || strings.Trim(u.Path, "/") != "" {
		return nil, errors.New("codex oauth: invalid issuer")
	}
	issuer = strings.TrimRight(issuer, "/")
	c := &Client{transport: client.Transport, config: oauth2.Config{
		ClientID: clientID, RedirectURL: RedirectURI,
		Scopes: []string{"openid", "profile", "email", "offline_access"},
		Endpoint: oauth2.Endpoint{
			AuthURL: issuer + "/oauth/authorize", TokenURL: issuer + "/oauth/token",
		},
	}}
	if c.transport == nil {
		c.transport = http.DefaultTransport
	}
	c.timeout = client.Timeout
	if c.timeout <= 0 || c.timeout > 30*time.Second {
		c.timeout = 30 * time.Second
	}
	return c, nil
}

// Begin creates independent random state and PKCE values for one pending login.
// The caller owns callback state validation and single-use session handling.
func (c *Client) Begin() Authorization {
	a := Authorization{State: oauth2.GenerateVerifier(), Verifier: oauth2.GenerateVerifier()}
	a.URL = c.config.AuthCodeURL(a.State,
		oauth2.S256ChallengeOption(a.Verifier),
		oauth2.SetAuthURLParam("id_token_add_organizations", "true"),
		oauth2.SetAuthURLParam("codex_cli_simplified_flow", "true"),
		oauth2.SetAuthURLParam("originator", "clan"),
	)
	return a
}

// Exchange exchanges a callback code after the caller has validated its state.
func (c *Client) Exchange(ctx context.Context, code, verifier string) (account.OAuthCredentials, error) {
	if !validField(code) || !validField(verifier) {
		return account.OAuthCredentials{}, &Failure{Kind: FailureRejected}
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {c.config.ClientID},
		"code": {code}, "code_verifier": {verifier}, "redirect_uri": {c.config.RedirectURL},
	}
	wire, err := c.requestToken(ctx, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return account.OAuthCredentials{}, err
	}
	return wire.credentials(account.OAuthCredentials{}, time.Now())
}

// Refresh returns a candidate replacement without changing previous. Optional
// fields retain previous values; the manager decides account identity policy.
func (c *Client) Refresh(ctx context.Context, previous account.OAuthCredentials) (account.OAuthCredentials, error) {
	if !validField(previous.RefreshToken) {
		return account.OAuthCredentials{}, &Failure{Kind: FailureRejected}
	}
	body, err := json.Marshal(map[string]string{
		"grant_type": "refresh_token", "client_id": clientID, "refresh_token": previous.RefreshToken,
	})
	if err != nil {
		return account.OAuthCredentials{}, &Failure{Kind: FailureRejected}
	}
	wire, err := c.requestToken(ctx, "application/json", bytes.NewReader(body))
	if err != nil {
		return account.OAuthCredentials{}, err
	}
	return wire.credentials(previous, time.Now())
}

func (c *Client) requestToken(ctx context.Context, contentType string, body io.Reader) (*tokenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint.TokenURL, body)
	if err != nil {
		return nil, &Failure{Kind: FailureInvalidResponse}
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		return nil, &Failure{Kind: FailureTransient, cause: contextCause(ctx, err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody+1))
	if resp.StatusCode != http.StatusOK {
		failure := responseFailure(resp, data)
		failure.cause = contextCause(ctx, err)
		return nil, failure
	}
	if err != nil {
		return nil, &Failure{Kind: FailureTransient, StatusCode: resp.StatusCode, cause: contextCause(ctx, err)}
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if len(data) > maxTokenBody || err != nil || mediaType != "application/json" {
		return nil, &Failure{Kind: FailureInvalidResponse, StatusCode: resp.StatusCode}
	}
	var wire *tokenResponse
	if json.Unmarshal(data, &wire) != nil || wire == nil {
		return nil, &Failure{Kind: FailureInvalidResponse, StatusCode: resp.StatusCode}
	}
	return wire, nil
}

type tokenResponse struct {
	AccessToken  *string         `json:"access_token"`
	RefreshToken *string         `json:"refresh_token"`
	IDToken      *string         `json:"id_token"`
	TokenType    string          `json:"token_type"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
	Error        json.RawMessage `json:"error"`
}

func (wire tokenResponse) credentials(previous account.OAuthCredentials, now time.Time) (account.OAuthCredentials, error) {
	invalid := &Failure{Kind: FailureInvalidResponse, StatusCode: http.StatusOK}
	if len(wire.Error) > 0 && string(wire.Error) != "null" {
		return account.OAuthCredentials{}, invalid
	}
	if wire.TokenType != "" && !strings.EqualFold(wire.TokenType, "Bearer") {
		return account.OAuthCredentials{}, invalid
	}
	credentials := previous
	if wire.IDToken != nil {
		var metadata struct {
			Auth struct {
				AccountID string `json:"chatgpt_account_id"`
				FedRAMP   bool   `json:"chatgpt_account_is_fedramp"`
			} `json:"https://api.openai.com/auth"`
		}
		if !decodeMetadata(*wire.IDToken, &metadata) || metadata.Auth.FedRAMP || !validField(metadata.Auth.AccountID) {
			return account.OAuthCredentials{}, invalid
		}
		credentials.ChatGPTAccountID = metadata.Auth.AccountID
	}
	if wire.RefreshToken != nil {
		credentials.RefreshToken = *wire.RefreshToken
	}
	if wire.AccessToken != nil {
		credentials.AccessToken = *wire.AccessToken
		if seconds, ok := expirySeconds(wire.ExpiresIn); ok {
			credentials.ExpiresAt = now.Add(time.Duration(seconds) * time.Second).UTC()
		} else {
			var access struct {
				Exp int64 `json:"exp"`
			}
			if !decodeMetadata(*wire.AccessToken, &access) || access.Exp <= now.Unix() || access.Exp > 253402300799 {
				return account.OAuthCredentials{}, invalid
			}
			credentials.ExpiresAt = time.Unix(access.Exp, 0).UTC()
		}
	}
	for _, value := range []string{credentials.ChatGPTAccountID, credentials.AccessToken, credentials.RefreshToken} {
		if !validField(value) {
			return account.OAuthCredentials{}, invalid
		}
	}
	if credentials.ExpiresAt.IsZero() {
		return account.OAuthCredentials{}, invalid
	}
	return credentials, nil
}

func expirySeconds(raw json.RawMessage) (int64, bool) {
	var seconds int64
	err := json.Unmarshal(raw, &seconds)
	return seconds, err == nil && seconds > 0 && seconds <= math.MaxInt64/int64(time.Second)
}

func validField(value string) bool {
	return strings.TrimSpace(value) != "" && strings.IndexFunc(value, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	}) == -1
}

// Metadata comes only from the authenticated token endpoint. Decoding these
// provider fields is not signature verification of arbitrary supplied JWTs.
func decodeMetadata(token string, dst any) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	return err == nil && json.Unmarshal(payload, dst) == nil
}

func responseFailure(resp *http.Response, body []byte) *Failure {
	f := &Failure{Kind: FailureInvalidResponse, StatusCode: resp.StatusCode}
	f.RetryAfter, _ = retry.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		f.Kind = FailureTransient
		return f
	}
	var detail struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if len(body) <= maxTokenBody && json.Unmarshal(body, &detail) == nil {
		var code string
		if json.Unmarshal(detail.Error, &code) == nil && code != "" {
			detail.Code = code
		} else {
			var nested struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(detail.Error, &nested) == nil && nested.Code != "" {
				detail.Code = nested.Code
			}
		}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		f.Kind = FailureRejected
	}
	switch detail.Code {
	case "refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated":
		f.Kind = FailureRejected
	case "invalid_grant":
		if resp.StatusCode == http.StatusBadRequest {
			f.Kind = FailureRejected
		}
	}
	return f
}

func contextCause(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return nil
}
