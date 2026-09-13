// Package management exposes the authenticated administrative HTTP API.
package management

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/execution"
	"github.com/deyna256/clan/internal/storage"
)

// Config supplies a separate management credential, never a clan_ client key.
type Config struct {
	AdminToken string
}

type service struct {
	store    *storage.Store
	oauth    *codexoauth.Manager
	executor *execution.Executor
	logger   *slog.Logger
}

// New returns a handler serving /api, including its authenticated OpenAPI document.
// Dependencies are borrowed; their owner remains responsible for shutdown.
func New(
	config Config,
	store *storage.Store,
	oauth *codexoauth.Manager,
	executor *execution.Executor,
	logger *slog.Logger,
) (http.Handler, error) {
	if !validToken(config.AdminToken) || strings.HasPrefix(config.AdminToken, "clan_") {
		return nil, errors.New("management: a separate valid admin token is required")
	}
	if store == nil || oauth == nil || executor == nil || logger == nil {
		return nil, errors.New("management: dependencies are required")
	}
	s := &service{store: store, oauth: oauth, executor: executor, logger: logger}
	router := chi.NewRouter()
	router.Use(authenticate(config.AdminToken))
	router.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, problem(http.StatusNotFound, "not-found", "The resource was not found."))
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
			if router.Match(chi.NewRouteContext(), method, chi.RouteContext(r.Context()).RoutePath) {
				w.Header().Add("Allow", method)
			}
		}
		writeProblem(w, problem(http.StatusMethodNotAllowed, "method-not-allowed", "The method is not supported."))
	})
	humaConfig := huma.DefaultConfig("CLAN management", "1.0.0")
	humaConfig.OpenAPIPath, humaConfig.DocsPath, humaConfig.SchemasPath = "", "", ""
	humaConfig.CreateHooks = nil
	humaConfig.Servers = []*huma.Server{{URL: "/api"}}
	humaConfig.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"admin": {Type: "http", Scheme: "bearer"},
	}
	humaConfig.Security = []map[string][]string{{"admin": {}}}
	humaConfig.RejectUnknownQueryParameters = true
	humaConfig.Transformers = []huma.Transformer{safeErrors}
	api := humachi.New(router, humaConfig)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		requestURL := ctx.URL()
		query, err := url.ParseQuery(requestURL.RawQuery)
		if err != nil {
			_ = huma.WriteErr(api, ctx, http.StatusBadRequest, "Invalid query.")
			return
		}
		for name, values := range query {
			if len(values) != 1 || (values[0] == "" && name != "q") {
				_ = huma.WriteErr(api, ctx, http.StatusUnprocessableEntity, "Invalid query.")
				return
			}
		}
		if ctx.Operation().RequestBody != nil {
			mediaType, _, err := mime.ParseMediaType(ctx.Header("Content-Type"))
			if err != nil || mediaType != "application/json" {
				_ = huma.WriteErr(api, ctx, http.StatusUnsupportedMediaType, "Use application/json.")
				return
			}
		}
		next(ctx)
	})
	s.routes(api)
	spec, err := json.Marshal(api.OpenAPI())
	if err != nil {
		return nil, errors.New("management: generating OpenAPI failed")
	}
	router.Get("/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec) // A disconnected reader cannot receive a second response.
	})
	app := chi.NewRouter()
	app.Mount("/api", router)
	return app, nil
}

func validToken(token string) bool {
	if token == "" || token[0] == '=' {
		return false
	}
	padding := false
	for _, c := range token {
		if c == '=' {
			padding = true
			continue
		}
		letterOrDigit := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if padding || (!letterOrDigit && !strings.ContainsRune("-._~+/", c)) {
			return false
		}
	}
	return true
}

func authenticate(token string) func(http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			scheme, credential, found := strings.Cut(r.Header.Get("Authorization"), " ")
			credential = strings.TrimLeft(credential, " ")
			got := sha256.Sum256([]byte(credential))
			validScheme := found && strings.EqualFold(scheme, "Bearer") && len(r.Header.Values("Authorization")) == 1
			if !validScheme || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="management"`)
				writeProblem(w, problem(http.StatusUnauthorized, "unauthorized", "A valid admin token is required."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func register[I, O any](api huma.API, operation huma.Operation, handler func(context.Context, *I) (*O, error)) {
	operation.MaxBodyBytes = 64 << 10
	operation.BodyReadTimeout = 5 * time.Second
	operation.Errors = []int{400, 401, 404, 408, 409, 413, 415, 422, 500, 503}
	huma.Register(api, operation, func(ctx context.Context, input *I) (*O, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return handler(ctx, input)
	})
}
