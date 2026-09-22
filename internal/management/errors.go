package management

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/execution"
	"github.com/deyna256/clan/internal/storage"
)

func problem(status int, kind, detail string) *huma.ErrorModel {
	return &huma.ErrorModel{Status: status, Title: http.StatusText(status), Type: "/api/problems/" + kind, Detail: detail}
}

func writeProblem(w http.ResponseWriter, value *huma.ErrorModel) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(value.Status)
	_ = json.NewEncoder(w).Encode(value) // A disconnected reader cannot receive a second response.
}

// Huma validation can reflect secrets in values, property names and messages.
// Keep only our fixed error descriptions, including for malformed JSON.
func safeErrors(_ huma.Context, _ string, body any) (any, error) {
	value, ok := body.(*huma.ErrorModel)
	if !ok {
		return body, nil
	}
	if value.Type != "" && value.Type != "about:blank" {
		value.Errors = nil
		return value, nil
	}
	kind, detail := "invalid-request", "The request is invalid."
	switch value.Status {
	case http.StatusBadRequest:
		detail = "The request could not be parsed."
	case http.StatusUnprocessableEntity:
		detail = "The request does not match the required fields or allowed values."
	case http.StatusRequestEntityTooLarge:
		kind, detail = "body-too-large", "The request body exceeds 64 KiB."
	case http.StatusUnsupportedMediaType:
		kind, detail = "unsupported-media-type", "Use Content-Type: application/json."
	case http.StatusRequestTimeout:
		kind, detail = "body-timeout", "The request body was not received in time."
	}
	safe := problem(value.Status, kind, detail)
	for _, entry := range value.Errors {
		location := "request"
		switch entry.Location {
		case "body", "body.name", "body.concurrency_limit", "body.login_id", "query.limit", "query.offset",
			"query.q", "query.state", "query.enabled", "path.id", "query.from", "query.to", "query.group_by",
			"query.key_id", "query.account_id", "query.model", "query.result", "query.cursor":
			location = entry.Location
		default:
			if strings.HasPrefix(entry.Location, "body.") {
				location = "body"
			}
			if strings.HasPrefix(entry.Location, "query.") {
				location = "query"
			}
		}
		safe.Errors = append(safe.Errors, &huma.ErrorDetail{Location: location, Message: "Invalid or missing value."})
	}
	return safe, nil
}

func (s *service) failure(ctx context.Context, err error, mutation bool) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		if mutation {
			return problem(503, "completion-unconfirmed", "Operation completion is unconfirmed; the resource may already have changed.")
		}
		return problem(503, "unavailable", "The operation could not finish in time.")
	case errors.Is(err, storage.ErrNotFound):
		return problem(404, "not-found", "The resource was not found.")
	case errors.Is(err, codexoauth.ErrLoginPending):
		return problem(409, "login-pending", "A login is already pending.")
	case errors.Is(err, codexoauth.ErrLoginMismatch):
		return problem(409, "login-mismatch", "The login ID does not match the latest attempt.")
	case errors.Is(err, codexoauth.ErrDisabled):
		return problem(409, "account-disabled", "The account is disabled.")
	case errors.Is(err, codexoauth.ErrNeedsSignIn):
		return problem(409, "needs-sign-in", "The account needs to sign in again.")
	case errors.Is(err, storage.ErrConflict):
		return problem(409, "state-conflict", "The resource changed or already exists.")
	case errors.Is(err, storage.ErrInvalid), errors.Is(err, codexoauth.ErrInvalidLogin):
		return problem(422, "invalid-request", "The request does not match the required fields or allowed values.")
	case errors.Is(err, codex.ErrCatalogUnavailable), errors.Is(err, execution.ErrClosed), errors.Is(err, execution.ErrUnavailable),
		errors.Is(err, codexoauth.ErrClosed), errors.Is(err, codexoauth.ErrUnavailable):
		return problem(503, "unavailable", "The service is temporarily unavailable.")
	default:
		s.logger.LogAttrs(ctx, slog.LevelError, "management operation failed", slog.String("result", "internal-error"))
		return problem(500, "internal-error", "The operation failed.")
	}
}
