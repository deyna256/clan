// Package openai implements public OpenAI Responses calls with API-key credentials.
package openai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/deyna256/clan/internal/account"
	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/transport"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/deyna256/clan/internal/retry"
	"github.com/deyna256/clan/internal/upstream"
	"github.com/deyna256/clan/internal/usage"
)

type Config struct {
	BaseURL          string
	UpstreamID       upstream.ID
	MaxResponseBytes int64
	MaxEventBytes    int
}

// InputError names an invalid or unsupported request field without its contents.
type InputError = wire.InputError

// Attempt supplies credentials and correlation IDs, after execution authorizes it.
// Credentials must never be logged. Resource ownership stays with execution.
type Attempt struct {
	ID, RequestID string
	Credentials   account.APIKeyCredentials
}

// Client must be created with New and may serve concurrent calls.
type Client struct {
	transport *transport.Client
	config    Config
	logger    *slog.Logger
}

func New(config Config, client *http.Client, logger *slog.Logger) (*Client, error) {
	if config.MaxResponseBytes <= 0 || config.MaxResponseBytes == math.MaxInt64 || config.MaxEventBytes <= 0 || logger == nil {
		return nil, errors.New("openai: positive body/event limits and logger are required")
	}
	t, err := transport.New(config.BaseURL, client)
	if err != nil {
		return nil, err
	}
	return &Client{transport: t, config: config, logger: logger.With("upstream_id", string(config.UpstreamID))}, nil
}

// Generate sends one request and closes its body before returning. Usage and
// response identity remain available on failure. It never retries a generation.
func (c *Client) Generate(ctx context.Context, attempt Attempt, request generation.Request) (generation.Result, error) {
	payload, err := wire.EncodeRequest(request, false)
	if err != nil {
		return generation.Result{}, err
	}
	response, err := c.transport.Create(ctx, attempt.Credentials.Key, payload, false)
	if err != nil {
		return generation.Result{}, transportFailure(err)
	}
	defer response.Body.Close()
	body, readErr := readBounded(response.Body, c.config.MaxResponseBytes)
	diagnostics := c.diagnostics(attempt)
	defer diagnostics.finish()
	envelope, decodeErr := wire.DecodeEnvelope(body)
	metadata := responseMetadata(envelope, diagnostics)
	if readErr != nil {
		return metadata, transportFailure(readErr)
	}
	if response.StatusCode != http.StatusOK {
		return metadata, classifyHTTPFailure(response.StatusCode, response.Header, envelope.Error)
	}
	if decodeErr != nil {
		return metadata, decodeErr
	}
	result, err := envelope.Result(diagnostics.report)
	result.Usage = metadata.Usage
	if err != nil {
		return result, err
	}
	for _, item := range result.Response.Output {
		if err := validateStateItemCompletion(item); err != nil {
			result.Response = generation.Response{}
			return result, err
		}
		if err := validateImageCompletion(item); err != nil {
			result.Response = generation.Response{}
			return result, err
		}
	}
	return result, nil
}

// HTTPError adds an upstream status and parsed cooldown to a classified failure.
type HTTPError struct {
	StatusCode int
	Cooldown   retry.Cooldown
	Failure    *generation.Failure
}

func (e *HTTPError) Error() string { return e.Failure.Error() }
func (e *HTTPError) Unwrap() error { return e.Failure }

func decodeHTTPFailure(response *http.Response, body []byte, diagnostics *diagnostics) (generation.Result, error) {
	envelope, _ := wire.DecodeEnvelope(body)
	return responseMetadata(envelope, diagnostics), classifyHTTPFailure(response.StatusCode, response.Header, envelope.Error)
}

func responseMetadata(envelope wire.ResponseEnvelope, diagnostics *diagnostics) generation.Result {
	state, invalid := wire.NormalizeUsage(envelope.Usage, usage.State{})
	if invalid {
		diagnostics.warn("invalid_usage", "response", "kept_valid_counters")
	}
	return generation.Result{Identity: generation.Identity{ID: envelope.ID, Model: envelope.Model}, Usage: state.Usage}
}

func classifyHTTPFailure(status int, headers http.Header, providerError wire.ProviderError) *HTTPError {
	failure := providerError.Failure()
	if providerError.Code == "" {
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden:
			failure.Kind = generation.Authentication
		case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
			failure.Kind = generation.InvalidRequest
		}
	}
	cooldown, _ := retry.ParseRetryAfter(headers.Get("Retry-After"), time.Now())
	return &HTTPError{StatusCode: status, Cooldown: cooldown, Failure: failure}
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if int64(len(body)) > limit {
		return nil, &generation.Failure{Kind: generation.ProtocolError, OutcomeUnknown: true}
	}
	return body, err
}

func transportFailure(err error) error {
	if errors.Is(err, transport.ErrInvalidPath) {
		return &InputError{Field: "resource_id", Problem: "must be a valid path segment"}
	}
	if errors.Is(err, transport.ErrInvalidKey) {
		return &generation.Failure{Kind: generation.Authentication}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			return cause
		}
	}
	var failure *generation.Failure
	if errors.As(err, &failure) {
		return failure
	}
	var network *net.OpError
	if errors.As(err, &network) && network.Op == "dial" {
		return &generation.Failure{Kind: generation.TransportError, Retryable: true}
	}
	return &generation.Failure{Kind: generation.TransportError, OutcomeUnknown: true}
}
