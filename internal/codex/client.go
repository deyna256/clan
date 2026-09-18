package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/retry"
	"github.com/deyna256/clan/internal/usage"
)

// DefaultBaseURL is the ChatGPT OAuth Codex API, not the public OpenAI API.
const DefaultBaseURL = "https://chatgpt.com/backend-api/codex"

// Category describes a failure without exposing provider messages or credentials.
type Category string

const (
	InvalidRequest   Category = "invalid_request"
	AccountProblem   Category = "account_problem"
	ProviderLimit    Category = "provider_limit"
	ProviderFailure  Category = "provider_failure"
	TransportFailure Category = "transport_failure"
	InvalidResponse  Category = "invalid_response"
)

// Failure contains facts about one failed attempt. Execution owns retry decisions.
// A false SafeToRetry means safety is not established, including for arbitrary 5xx errors.
type Failure struct {
	Category    Category
	HTTPStatus  int
	SafeToRetry bool
	RetryAfter  retry.Cooldown
	cause       error
}

func (e *Failure) Error() string { return "codex: " + string(e.Category) }

// Unwrap exposes only context cancellation/deadline errors, never transport URLs or provider bodies.
func (e *Failure) Unwrap() error { return e.cause }

// Result contains a terminal Responses object when received, and the latest observed usage.
// Usage remains available when the attempt fails; unknown counts are not zero counts.
type Result struct {
	Response json.RawMessage
	Status   string // Empty until a valid terminal response is received.
	Usage    usage.Snapshot
}

// Client performs one HTTP attempt, without generation retries or redirects.
// Construct it with NewClient. It is safe for concurrent use.
type Client struct {
	http    http.Client
	baseURL string
	version string
}

// NewClient copies the HTTP client settings, disables redirects and requires a
// protocol client version for model discovery. Empty baseURL uses DefaultBaseURL.
// Opening a response, including sending the request, takes at most five minutes.
// Standard transports are cloned; shorter transport timeouts remain in effect.
// The default transport bounds TCP connection setup to 30 seconds and TLS to ten.
// An explicit client Timeout or caller deadline still bounds the whole operation.
// The caller must not mutate the supplied transport or cookie jar concurrently.
func NewClient(client *http.Client, baseURL, clientVersion string) (*Client, error) {
	if client == nil {
		return nil, errors.New("codex: HTTP client is required")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("codex: invalid base URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("codex: invalid base URL")
	}
	if strings.TrimSpace(clientVersion) == "" || strings.ContainsAny(clientVersion, "\r\n") {
		return nil, errors.New("codex: client version is required")
	}
	c := &Client{http: *client, baseURL: strings.TrimRight(baseURL, "/"), version: clientVersion}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if standard, ok := transport.(*http.Transport); ok {
		cloned := standard.Clone()
		// DialContext takes precedence over Dial. Keep the legacy check so
		// callers with a custom Dial are not silently switched to our dialer.
		//lint:ignore SA1019 Preserving caller-supplied legacy dialers requires checking Dial.
		if cloned.DialContext == nil && cloned.Dial == nil {
			cloned.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		}
		if cloned.TLSHandshakeTimeout <= 0 {
			cloned.TLSHandshakeTimeout = 10 * time.Second
		}
		transport = cloned
	}
	c.http.Transport = transport
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

// CloseIdleConnections releases pooled connections without interrupting active requests.
// The application calls it after execution and catalog shutdown.
func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }

// Generate consumes the same stream as Stream and returns its terminal result.
// The caller must first validate enabled-account model membership and ValidateModel.
func (c *Client) Generate(ctx context.Context, a account.Account, request Request) (Result, error) {
	stream, err := c.Stream(ctx, a, request)
	if err != nil {
		return Result{}, err
	}
	defer stream.Close()
	for {
		_, err = stream.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return stream.Result(), err
		}
	}
}

// Stream opens one upstream attempt. The caller owns Close, including on early return.
// The caller must first validate enabled-account model membership and ValidateModel.
func (c *Client) Stream(ctx context.Context, a account.Account, request Request) (*Stream, error) {
	if len(request.body) == 0 {
		return nil, invalid("request")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	resp, err := c.do(ctx, a, http.MethodPost, "/responses", request.body)
	if err != nil {
		cancel(nil)
		return nil, err
	}
	contentType := resp.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	// Codex may omit this header; the stream parser still validates its body.
	if contentType != "" && (err != nil || mediaType != "text/event-stream") {
		failure := httpFailure(resp)
		resp.Body.Close()
		cancel(nil)
		return nil, failure
	}
	return newStream(ctx, cancel, resp), nil
}

func (c *Client) do(ctx context.Context, a account.Account, method, path string, body []byte) (*http.Response, error) {
	if c == nil || c.baseURL == "" {
		return nil, errors.New("codex: client is not initialized")
	}
	credentials := a.Credentials()
	if account.ValidateCredentials(credentials) != nil {
		return nil, &Failure{Category: AccountProblem, SafeToRetry: true}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, safeFailure(ctx, TransportFailure, 0, err)
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("ChatGPT-Account-Id", credentials.ChatGPTAccountID)
	req.Header.Set("Originator", "clan")
	req.Header.Set("User-Agent", "clan/"+c.version)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := c.send(req)
	if err != nil {
		return nil, safeFailure(ctx, TransportFailure, 0, err)
	}
	if resp.StatusCode != http.StatusOK {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		closeBody := sync.OnceFunc(func() { resp.Body.Close() })
		done := make(chan struct{})
		stop := context.AfterFunc(readCtx, func() {
			closeBody()
			close(done)
		})
		defer func() {
			closeBody()
			if !stop() {
				<-done
			}
		}()
		failure := httpFailure(resp)
		failure.cause = safeFailure(readCtx, failure.Category, failure.HTTPStatus, nil).cause
		return nil, failure
	}
	return resp, nil
}

func (c *Client) send(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	done := make(chan struct{})
	timer := time.AfterFunc(5*time.Minute, func() {
		cancel(context.DeadlineExceeded)
		close(done)
	})
	resp, err := c.http.Do(req.WithContext(ctx))
	if !timer.Stop() {
		<-done
	}
	if ctx.Err() != nil {
		if resp != nil {
			resp.Body.Close()
		}
		err = context.Cause(ctx)
	}
	if err != nil {
		cancel(nil)
		return nil, err
	}
	resp.Body = &responseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type responseBody struct {
	io.ReadCloser
	cancel context.CancelCauseFunc
}

func (b *responseBody) Close() error {
	b.cancel(nil)
	return b.ReadCloser.Close()
}

func safeFailure(ctx context.Context, category Category, status int, err error) *Failure {
	cause := ctx.Err()
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	}
	if cause == nil {
		var timeout net.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			cause = context.DeadlineExceeded
		case errors.Is(err, context.Canceled):
			cause = context.Canceled
		case errors.As(err, &timeout) && timeout.Timeout():
			cause = context.DeadlineExceeded
		}
	}
	return &Failure{Category: category, HTTPStatus: status, cause: cause}
}

func httpFailure(response *http.Response) *Failure {
	status := response.StatusCode
	f := &Failure{Category: ProviderFailure, HTTPStatus: status}
	receivedAt := time.Now()
	f.RetryAfter, _ = retry.ParseRetryAfter(response.Header.Get("Retry-After"), receivedAt)
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		f.Category = InvalidRequest
		f.SafeToRetry = true
	case http.StatusUnauthorized, http.StatusForbidden:
		f.Category = AccountProblem
		f.SafeToRetry = true
	case http.StatusTooManyRequests:
		f.Category = ProviderLimit
		f.SafeToRetry = true
		if f.RetryAfter.Kind == retry.NoCooldown {
			f.RetryAfter = quotaReset(response.Body, receivedAt)
		}
	default:
		if status < 400 {
			f.Category = InvalidResponse
		}
	}
	return f
}

func quotaReset(body io.Reader, receivedAt time.Time) retry.Cooldown {
	const maxErrorBody = 64 << 10
	const lastUnixSecond = 253402300799 // End of year 9999, the last RFC3339 year.
	data, err := io.ReadAll(io.LimitReader(body, maxErrorBody+1))
	if err != nil || len(data) > maxErrorBody {
		return retry.Cooldown{}
	}
	var wire struct {
		Error struct {
			Type     string          `json:"type"`
			ResetsAt json.RawMessage `json:"resets_at"`
			ResetsIn json.RawMessage `json:"resets_in_seconds"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &wire) != nil || wire.Error.Type != "usage_limit_reached" {
		return retry.Cooldown{}
	}
	var seconds int64
	if json.Unmarshal(wire.Error.ResetsAt, &seconds) == nil && seconds > receivedAt.Unix() && seconds <= lastUnixSecond {
		return retry.Cooldown{Kind: retry.RetryAt, Until: time.Unix(seconds, 0)}
	}
	if json.Unmarshal(wire.Error.ResetsIn, &seconds) == nil && seconds > 0 {
		cooldown, _ := retry.ParseRetryAfter(string(wire.Error.ResetsIn), receivedAt)
		return cooldown
	}
	return retry.Cooldown{}
}

func responseFailure(raw json.RawMessage, status int) *Failure {
	var detail struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &detail)
	f := &Failure{Category: ProviderFailure, HTTPStatus: status}
	switch detail.Code {
	case "context_length_exceeded", "invalid_prompt", "bio_policy", "cyber_policy", "misalignment_policy_violation":
		f.Category = InvalidRequest
	case "insufficient_quota", "rate_limit_exceeded", "server_is_overloaded", "slow_down":
		f.Category = ProviderLimit
	case "usage_not_included":
		f.Category = AccountProblem
	}
	// A terminal failure may follow generated output. Its code alone does not prove replay safety.
	return f
}

var errPayloadTooLarge = errors.New("codex: response body too large")

func readPayload(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxPayload+1))
	if len(data) > maxPayload {
		return nil, errPayloadTooLarge
	}
	return data, err
}
