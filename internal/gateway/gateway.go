// Package gateway serves the client Responses API.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/execution"
)

const maxBody = 16 << 20

type requestIDKey struct{}

type requestMetadata struct {
	info     execution.RequestInfo
	keyID    accesskey.ID
	model    string
	original context.Context
}

func metadata(r *http.Request) *requestMetadata {
	m, _ := r.Context().Value(requestIDKey{}).(*requestMetadata)
	return m
}

func requestInfo(r *http.Request) execution.RequestInfo {
	if m := metadata(r); m != nil {
		return m.info
	}
	return execution.RequestInfo{}
}

type handler struct {
	executor *execution.Executor
	logger   *slog.Logger
}

// New serves /v1/responses and /v1/models, borrowing its dependencies.
func New(executor *execution.Executor, logger *slog.Logger) (http.Handler, error) {
	if executor == nil || logger == nil {
		return nil, errors.New("gateway: executor and logger are required")
	}
	h := &handler{executor: executor, logger: logger}
	router := chi.NewRouter()
	router.Use(h.authenticate)
	router.Post("/v1/responses", h.responses)
	router.Get("/v1/models", h.models)
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		h.reject(w, r, clientError{status: http.StatusNotFound, Type: "invalid_request_error", Code: "not_found", Message: "The resource was not found."})
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Allow", http.MethodGet)
		} else {
			w.Header().Set("Allow", http.MethodPost)
		}
		h.reject(w, r, clientError{status: http.StatusMethodNotAllowed, Type: "invalid_request_error", Code: "method_not_allowed", Message: "The method is not supported."})
	})
	return router, nil
}

func (h *handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := "req_" + rand.Text()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		meta := &requestMetadata{info: execution.RequestInfo{ID: id, Started: time.Now()}, original: r.Context()}
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, meta))
		key := bearer(r)
		keyID, err := h.executor.CheckKey(r.Context(), key)
		if err != nil {
			h.reject(w, r, classify(err))
			return
		}
		meta.keyID = keyID
		if r.URL.RawQuery != "" {
			h.reject(w, r, invalidRequest("Query parameters are not supported.", ""))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return ""
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func requestID(r *http.Request) string {
	return requestInfo(r).ID
}

func (h *handler) responses(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || !supportedEncoding(r.Header.Values("Content-Encoding")) {
		h.reject(w, r, clientError{status: http.StatusUnsupportedMediaType, Type: "invalid_request_error", Code: "unsupported_media_type", Message: "Use uncompressed application/json."})
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		// net/http cancels the request on upload errors, even when a reply is writable.
		// No generation was admitted; attempt only the bounded error response.
		r = r.WithContext(context.WithoutCancel(r.Context()))
		h.reject(w, r, classify(err))
		return
	}
	input, err := codex.ParseRequest(body)
	if err != nil {
		h.reject(w, r, classify(err))
		return
	}
	metadata(r).model = input.Model()
	if input.Streaming() {
		h.stream(w, r, input)
		return
	}
	h.generate(w, r, input)
}

func supportedEncoding(values []string) bool {
	return len(values) == 0 || (len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), "identity"))
}

func readBody(w http.ResponseWriter, r *http.Request) (body []byte, err error) {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(r.Context(), func() {
		// Best effort on cancellation; the upload deadline still bounds the read.
		_ = controller.SetReadDeadline(time.Now())
		close(done)
	})
	defer func() {
		if !stop() {
			<-done
		}
		if r.Context().Err() == nil {
			err = errors.Join(err, controller.SetReadDeadline(time.Time{}))
		}
	}()
	body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var limit *http.MaxBytesError
		var network net.Error
		if !errors.As(err, &limit) && !(errors.As(err, &network) && network.Timeout()) {
			err = errInvalidBody
		}
	}
	return body, err
}

func (h *handler) generate(w http.ResponseWriter, r *http.Request, input codex.Request) {
	result, err := h.executor.Generate(r.Context(), bearer(r), requestInfo(r), input)
	if !result.Admitted() {
		h.reject(w, r, classify(err))
		return
	}
	defer result.Close()
	ctx := result.Context()
	delivery := newDelivery(w, ctx)
	defer delivery.close()
	if err != nil {
		if result.Status != "failed" || ctx.Err() != nil || result.CleanupError() != nil {
			if !writeError(delivery, r, classify(err), result.ResponseStarted) {
				result.DeliveryFailed()
			}
			return
		}
		result.Response, err = normalizeFailedResponse(result.Response)
		if err != nil {
			if !writeError(delivery, r, classify(err), result.ResponseStarted) {
				result.DeliveryFailed()
			}
			return
		}
	}
	if !delivery.json(http.StatusOK, result.Response, result.ResponseStarted) {
		result.DeliveryFailed()
	}
}

func (h *handler) models(w http.ResponseWriter, r *http.Request) {
	closeUnreadConnection(w, r)
	models, err := h.executor.Models(r.Context(), bearer(r))
	if err != nil {
		h.reject(w, r, classify(err))
		return
	}
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	list := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list", Data: make([]model, 0, len(models))}
	for _, item := range models {
		list.Data = append(list.Data, model{ID: item.ID, Object: "model", OwnedBy: "openai"})
	}
	body, _ := json.Marshal(list)
	delivery := newDelivery(w, r.Context())
	defer delivery.close()
	if !delivery.json(http.StatusOK, body, nil) {
		h.logger.WarnContext(r.Context(), "model list delivery failed", "request_id", requestID(r))
	}
}

func closeUnreadConnection(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor == 1 && r.Body != nil && r.ContentLength != 0 {
		// Avoid net/http draining an unused upload before sending the response.
		w.Header().Set("Connection", "close")
	}
}
