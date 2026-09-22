package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/concurrency"
	"github.com/deyna256/clan/internal/execution"
)

var errInvalidBody = errors.New("gateway: invalid request body")

type clientError struct {
	status  int
	Type    string  `json:"type"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Param   *string `json:"param"`
}

func invalidRequest(message, field string) clientError {
	e := clientError{status: http.StatusBadRequest, Type: "invalid_request_error", Code: "invalid_request", Message: message}
	if field != "" {
		e.Param = &field
	}
	return e
}

func classify(err error) clientError {
	var validation *codex.ValidationError
	var tooLarge *http.MaxBytesError
	var network net.Error
	var failure *codex.Failure
	switch {
	case errors.Is(err, execution.ErrUnauthorized):
		return clientError{status: http.StatusUnauthorized, Type: "authentication_error", Code: "invalid_api_key", Message: "The client key is invalid or revoked."}
	case errors.Is(err, errInvalidBody):
		return invalidRequest("The request body is invalid or incomplete.", "")
	case errors.As(err, &validation):
		return invalidRequest("Invalid or unsupported request field.", safeField(validation.Field))
	case errors.As(err, &tooLarge):
		return clientError{status: http.StatusRequestEntityTooLarge, Type: "invalid_request_error", Code: "request_too_large", Message: "The request exceeds 16 MiB."}
	case errors.Is(err, concurrency.ErrLimitReached):
		return clientError{status: http.StatusTooManyRequests, Type: "rate_limit_error", Code: "concurrency_limit", Message: "The client key has no free request slots."}
	case errors.Is(err, context.DeadlineExceeded):
		return clientError{status: http.StatusGatewayTimeout, Type: "server_error", Code: "upstream_timeout", Message: "The upstream request timed out."}
	case errors.As(err, &failure):
		switch failure.Category {
		case codex.InvalidRequest:
			return invalidRequest("Codex rejected the request.", "")
		case codex.ProviderLimit:
			return clientError{status: http.StatusTooManyRequests, Type: "rate_limit_error", Code: "rate_limit_exceeded", Message: "The provider limit was reached."}
		case codex.AccountProblem:
			return unavailable()
		default:
			return clientError{status: http.StatusBadGateway, Type: "server_error", Code: "upstream_error", Message: "The upstream response could not be completed."}
		}
	case errors.Is(err, codex.ErrCatalogUnavailable), errors.Is(err, execution.ErrNoAccounts), errors.Is(err, execution.ErrClosed), errors.Is(err, execution.ErrUnavailable), errors.Is(err, context.Canceled):
		return unavailable()
	case errors.As(err, &network) && network.Timeout():
		return clientError{status: http.StatusRequestTimeout, Type: "invalid_request_error", Code: "request_timeout", Message: "The request body was not received in time."}
	default:
		return clientError{status: http.StatusInternalServerError, Type: "server_error", Code: "internal_error", Message: "The request could not be completed."}
	}
}

func unavailable() clientError {
	return clientError{status: http.StatusServiceUnavailable, Type: "server_error", Code: "service_unavailable", Message: "No account is currently available to serve the request."}
}

func safeField(field string) string {
	field = strings.TrimPrefix(field, "request.")
	root, _, _ := strings.Cut(field, ".")
	switch root {
	case "model", "input", "instructions", "stream", "store", "background", "max_output_tokens", "prompt_cache_key", "tools", "tool_choice", "parallel_tool_calls", "reasoning", "include", "text":
		return root
	default:
		return ""
	}
}

func writeError(d *delivery, r *http.Request, failure clientError, started func()) bool {
	if r.Method == http.MethodPost && r.URL.Path == "/v1/responses" {
		d.w.Header().Set("X-Should-Retry", "false")
	}
	if failure.status == http.StatusUnauthorized {
		d.w.Header().Set("WWW-Authenticate", "Bearer")
	}
	body, _ := json.Marshal(struct {
		Error clientError `json:"error"`
	}{failure})
	return d.json(failure.status, body, started)
}

func (h *handler) reject(w http.ResponseWriter, r *http.Request, failure clientError) {
	closeUnreadConnection(w, r)
	meta := metadata(r)
	record := r.Method == http.MethodPost && r.URL.Path == "/v1/responses" && meta != nil && meta.keyID != ""
	if !record {
		h.logger.InfoContext(r.Context(), "client request rejected", "request_id", requestID(r), "code", failure.Code, "http_status", failure.status)
	}
	d := newDelivery(w, r.Context())
	started := false
	delivered := writeError(d, r, failure, func() { started = true })
	d.close()
	if !record {
		if !delivered {
			h.logger.WarnContext(r.Context(), "client error delivery failed", "request_id", requestID(r))
		}
		return
	}
	outcome := failure.Code
	if !delivered {
		outcome = "delivery_failed"
	}
	if err := meta.original.Err(); err != nil {
		outcome = "canceled"
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = "timeout"
		}
	}
	h.executor.RecordRejection(meta.original, meta.info, meta.keyID, meta.model, outcome, started)
}
