package management_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"

	"github.com/go-chi/chi/v5"

	"github.com/deyna256/clan/internal/accesskey"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/execution"
	"github.com/deyna256/clan/internal/management"
	"github.com/deyna256/clan/internal/storage"
)

func TestConstructor(t *testing.T) {
	store, oauth, executor := &storage.Store{}, &codexoauth.Manager{}, &execution.Executor{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, token := range []string{"", " ", " token", "token ", "a\tb", "a\nb", "é", "a=b", "=", "clan_example"} {
		handler, err := management.New(management.Config{AdminToken: token}, store, oauth, executor, logger)
		if err == nil || handler != nil || strings.Contains(err.Error(), "clan_example") {
			t.Errorf("invalid admin token accepted or disclosed: handler=%v, err=%v", handler, err)
		}
	}
	for _, dependencies := range []struct {
		store    *storage.Store
		oauth    *codexoauth.Manager
		executor *execution.Executor
		logger   *slog.Logger
	}{
		{store: nil, oauth: oauth, executor: executor, logger: logger},
		{store: store, oauth: nil, executor: executor, logger: logger},
		{store: store, oauth: oauth, executor: nil, logger: logger},
		{store: store, oauth: oauth, executor: executor, logger: nil},
	} {
		_, err := management.New(management.Config{AdminToken: adminToken}, dependencies.store,
			dependencies.oauth, dependencies.executor, dependencies.logger)
		if err == nil {
			t.Fatal("missing dependency accepted")
		}
	}
	for _, token := range []string{adminToken, "Az09-._~+/=="} {
		_, err := management.New(management.Config{AdminToken: token}, store, oauth, executor, logger)
		if err != nil {
			t.Fatalf("valid token rejected: %v", err)
		}
	}
}

func validationHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := management.New(management.Config{AdminToken: adminToken}, &storage.Store{},
		&codexoauth.Manager{}, &execution.Executor{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestAuthenticationAndValidation(t *testing.T) {
	handler := validationHandler(t)
	for _, tt := range []struct {
		name, method, path, token, body string
		status                          int
	}{
		{name: "auth before parse", method: "POST", path: "/api/client-keys", body: "not JSON", status: 401},
		{name: "wrong token", method: "POST", path: "/api/oauth/login", token: "Bearer wrong", body: "not JSON", status: 401},
		{name: "client key", method: "GET", path: "/api/openapi.json", token: "Bearer clan_example", status: 401},
		{name: "schema auth", method: "GET", path: "/api/openapi.json", status: 401},
		{name: "missing limit", method: "POST", path: "/api/client-keys", token: authorization, body: `{"name":"test"}`, status: 422},
		{name: "null limit", method: "POST", path: "/api/client-keys", token: authorization, body: `{"name":"test","concurrency_limit":null}`, status: 422},
		{name: "blank name", method: "POST", path: "/api/client-keys", token: authorization, body: `{"name":" \t","concurrency_limit":0}`, status: 422},
		{name: "missing name", method: "POST", path: "/api/client-keys", token: authorization, body: `{"concurrency_limit":0}`, status: 422},
		{name: "negative limit", method: "PATCH", path: "/api/client-keys/id", token: authorization, body: `{"concurrency_limit":-2}`, status: 422},
		{name: "fractional limit", method: "PATCH", path: "/api/client-keys/id", token: authorization, body: `{"concurrency_limit":0.5}`, status: 422},
		{name: "overflow limit", method: "PATCH", path: "/api/client-keys/id", token: authorization, body: `{"concurrency_limit":9223372036854775808}`, status: 422},
		{name: "empty patch", method: "PATCH", path: "/api/client-keys/id", token: authorization, body: `{}`, status: 422},
		{name: "null patch", method: "PATCH", path: "/api/client-keys/id", token: authorization, body: `null`, status: 422},
		{name: "unknown property", method: "PATCH", path: "/api/client-keys/id", token: authorization, body: `{"concurrency_limit":1,"credential-marker":"credential-marker"}`, status: 422},
		{name: "uppercase fields", method: "POST", path: "/api/client-keys", token: authorization, body: `{"NAME":"test","CONCURRENCY_LIMIT":0}`, status: 422},
		{name: "case alias changes limit", method: "PATCH", path: "/api/client-keys/id", token: authorization, body: `{"concurrency_limit":0,"CONCURRENCY_LIMIT":-1}`, status: 422},
		{name: "null case alias", method: "PATCH", path: "/api/client-keys/id", token: authorization, body: `{"concurrency_limit":0,"CONCURRENCY_LIMIT":null}`, status: 422},
		{name: "duplicate limit", method: "PATCH", path: "/api/client-keys/id", token: authorization, body: `{"concurrency_limit":0,"concurrency_limit":-1}`, status: 400},
		{name: "escaped duplicate", method: "POST", path: "/api/oauth/login", token: authorization, body: `{"name":"test","\u006eame":"credential-marker"}`, status: 400},
		{name: "login case alias", method: "POST", path: "/api/oauth/login", token: authorization, body: `{"name":"test","NAME":"credential-marker"}`, status: 422},
		{name: "cancel case alias", method: "POST", path: "/api/oauth/login/cancel", token: authorization, body: `{"login_id":"old","LOGIN_ID":"credential-marker"}`, status: 422},
		{name: "missing cancel ID", method: "POST", path: "/api/oauth/login/cancel", token: authorization, body: `{}`, status: 422},
		{name: "null cancel ID", method: "POST", path: "/api/oauth/login/cancel", token: authorization, body: `{"login_id":null}`, status: 422},
		{name: "malformed JSON", method: "POST", path: "/api/oauth/login", token: authorization, body: `{"credential-marker`, status: 400},
		{name: "oversized body", method: "POST", path: "/api/oauth/login", token: authorization, body: strings.Repeat("x", 65537), status: 413},
		{name: "unknown query", method: "GET", path: "/api/accounts?credential-marker=credential-marker", token: authorization, status: 422},
		{name: "repeated query", method: "GET", path: "/api/accounts?limit=1&limit=2", token: authorization, status: 422},
		{name: "empty limit", method: "GET", path: "/api/accounts?limit=", token: authorization, status: 422},
		{name: "zero limit", method: "GET", path: "/api/accounts?limit=0", token: authorization, status: 422},
		{name: "large limit", method: "GET", path: "/api/accounts?limit=101", token: authorization, status: 422},
		{name: "negative offset", method: "GET", path: "/api/accounts?offset=-1", token: authorization, status: 422},
		{name: "overflow offset", method: "GET", path: "/api/accounts?offset=9223372036854775808", token: authorization, status: 422},
		{name: "state invalid", method: "GET", path: "/api/accounts?state=credential-marker", token: authorization, status: 422},
		{name: "enabled invalid", method: "GET", path: "/api/client-keys?enabled=1", token: authorization, status: 422},
		{name: "enabled empty", method: "GET", path: "/api/client-keys?enabled=", token: authorization, status: 422},
		{name: "query malformed", method: "GET", path: "/api/accounts?q=%zz", token: authorization, status: 400},
		{name: "UI disabled", method: "GET", path: "/api/docs", token: authorization, status: 404},
		{name: "schema routes disabled", method: "GET", path: "/api/schemas/accountView.json", token: authorization, status: 404},
		{name: "YAML disabled", method: "GET", path: "/api/openapi.yaml", token: authorization, status: 404},
		{name: "unsupported method", method: "PUT", path: "/api/accounts", token: authorization, status: 405},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := call(handler, tt.method, tt.path, tt.token, tt.body)

			requireStatus(t, response, tt.status)
			if response.Header().Get("Content-Type") != "application/problem+json" {
				t.Fatalf("error content type = %q", response.Header().Get("Content-Type"))
			}
			if strings.Contains(response.Body.String(), "credential-marker") || strings.Contains(response.Body.String(), `"value"`) {
				t.Fatalf("submitted data leaked: %s", response.Body)
			}
			var value struct {
				Type, Title, Detail string
				Status              int
			}
			if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || value.Type == "" || value.Detail == "" || value.Status != tt.status {
				t.Fatalf("invalid Problem Details: %s, %v", response.Body, err)
			}
			if tt.status == 405 && response.Header().Get("Allow") != "GET" {
				t.Fatalf("Allow = %q, want GET", response.Header().Get("Allow"))
			}
		})
	}
	for _, contentType := range []string{"", "text/plain", "application/json-patch+json", "application/json;invalid"} {
		r := httptest.NewRequest("POST", "/api/oauth/login", strings.NewReader(`{"name":"test"}`))
		r.Header.Set("Authorization", authorization)
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		requireStatus(t, w, 415)
	}
	for _, token := range []string{"bearer " + adminToken, "BEARER   " + adminToken} {
		requireStatus(t, call(handler, "GET", "/api/openapi.json", token, ""), 200)
	}
	for _, token := range []string{"Bearer\t" + adminToken, authorization + " ", authorization + " extra"} {
		requireStatus(t, call(handler, "GET", "/api/openapi.json", token, ""), 401)
	}
	r := httptest.NewRequest("GET", "/api/openapi.json", nil)
	r.Header.Add("Authorization", authorization)
	r.Header.Add("Authorization", authorization)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	requireStatus(t, w, 401)
}

func TestSchemaMountAndConcurrentReads(t *testing.T) {
	handler := validationHandler(t)
	mounted := chi.NewRouter()
	mounted.Handle("/api/*", handler)
	mounted.Get("/v1/responses", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	if response := call(mounted, "GET", "/v1/responses", "", ""); response.Code != 204 {
		t.Fatalf("management mount intercepted sibling: %d", response.Code)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			response := call(mounted, "GET", "/api/openapi.json", authorization, "")
			if response.Code != 200 {
				t.Errorf("schema status = %d", response.Code)
			}
		})
	}
	wg.Wait()
	response := call(mounted, "GET", "/api/openapi.json", authorization, "")
	var schema struct {
		OpenAPI    string
		Servers    []struct{ URL string }
		Paths      map[string]map[string]json.RawMessage
		Components struct {
			SecuritySchemes map[string]any
			Schemas         map[string]json.RawMessage
		}
		Security []map[string][]string
	}
	if err := json.Unmarshal(response.Body.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"/usage": {"get"}, "/requests": {"get"}, "/requests/{id}": {"get"},
		"/oauth/login": {"get", "post"}, "/oauth/login/cancel": {"post"}, "/accounts": {"get"},
		"/accounts/{id}": {"get", "delete"}, "/accounts/{id}/reconnect": {"post"},
		"/accounts/{id}/enable": {"post"}, "/accounts/{id}/disable": {"post"},
		"/client-keys": {"get", "post"}, "/client-keys/{id}": {"patch"},
		"/client-keys/{id}/revoke": {"post"}, "/models": {"get"},
	}
	if schema.OpenAPI != "3.1.0" || len(schema.Servers) != 1 || schema.Servers[0].URL != "/api" || len(schema.Paths) != len(want) {
		t.Fatalf("unexpected schema metadata or routes: %+v", schema)
	}
	for path, methods := range want {
		for _, method := range methods {
			if schema.Paths[path][method] == nil {
				t.Errorf("missing schema operation %s %s", method, path)
			}
		}
	}
	if schema.Components.SecuritySchemes["admin"] == nil || len(schema.Security) != 1 {
		t.Fatal("missing admin security contract")
	}
	if strings.Contains(response.Body.String(), `"$schema"`) || strings.Contains(response.Body.String(), "verification_hash") {
		t.Fatal("schema contains decoration or private metadata")
	}
	var creation struct {
		Responses map[string]struct {
			Content map[string]struct {
				Schema struct {
					Ref string `json:"$ref"`
				}
			}
		}
	}
	if err := json.Unmarshal(schema.Paths["/client-keys"]["post"], &creation); err != nil {
		t.Fatal(err)
	}
	ref := creation.Responses["201"].Content["application/json"].Schema.Ref
	var bodySchema struct{ Properties map[string]json.RawMessage }
	if err := json.Unmarshal(schema.Components.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")], &bodySchema); err != nil {
		t.Fatal(err)
	}
	for _, property := range []string{"id", "name", "enabled", "concurrency_limit", "key"} {
		if bodySchema.Properties[property] == nil {
			t.Errorf("created key schema missing %s", property)
		}
	}
	for _, path := range []string{"/accounts", "/client-keys", "/models"} {
		var operation struct{ Parameters []struct{ Name string } }
		if err := json.Unmarshal(schema.Paths[path]["get"], &operation); err != nil {
			t.Fatal(err)
		}
		parameters := make(map[string]bool)
		for _, parameter := range operation.Parameters {
			parameters[parameter.Name] = true
		}
		for _, name := range []string{"limit", "offset", "q"} {
			if !parameters[name] {
				t.Errorf("%s missing parameter %s", path, name)
			}
		}
	}
}

func TestHumaConfigurationIsLocal(t *testing.T) {
	_ = validationHandler(t)
	router := chi.NewRouter()
	config := huma.DefaultConfig("Unrelated API", "1")
	config.CreateHooks = nil
	api := humachi.New(router, config)
	huma.Register(api, huma.Operation{OperationID: "unrelated", Method: "POST", Path: "/probe"},
		func(_ context.Context, _ *struct {
			Body struct {
				Name string `json:"name"`
			}
		}) (*struct{}, error) {
			return nil, nil
		})

	response := call(router, "POST", "/probe", "", `{"name":"test","credential-marker":"credential-marker"}`)

	if response.Code != 422 || !strings.Contains(response.Body.String(), "credential-marker") {
		t.Fatalf("management changed unrelated Huma errors: %s", response.Body)
	}
}

func TestBodyReadDeadline(t *testing.T) {
	handler := validationHandler(t)
	request := httptest.NewRequest("POST", "/api/oauth/login", timeoutReader{})
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Type", "application/json")
	response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()

	handler.ServeHTTP(response, request)

	requireStatus(t, response.ResponseRecorder, 408)
	if response.deadline.Before(before.Add(5*time.Second)) || response.deadline.After(time.Now().Add(5*time.Second)) {
		t.Fatalf("body read deadline = %v, want five seconds", response.deadline)
	}
}

func TestBodySizeBoundary(t *testing.T) {
	f := newFixture(t, nil)
	for _, tt := range []struct {
		name   string
		size   int
		status int
	}{
		{name: "below", size: 65535, status: 409},
		{name: "at", size: 65536, status: 409},
		{name: "above", size: 65537, status: 413},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"login_id":"unknown"}`
			body += strings.Repeat(" ", tt.size-len(body))

			response := call(f.handler, "POST", "/api/oauth/login/cancel", authorization, body)

			requireStatus(t, response, tt.status)
		})
	}
}

type timeoutReader struct{}

func (timeoutReader) Read([]byte) (int, error) { return 0, os.ErrDeadlineExceeded }

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (r *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		r.deadline = deadline
	}
	return nil
}

func TestKeyCreationUpdatesAndRevocation(t *testing.T) {
	f := newFixture(t, nil)
	response := call(f.handler, "POST", "/api/client-keys", authorization, `{"name":"My key","concurrency_limit":0}`)
	requireStatus(t, response, 201)
	var created struct {
		ID, Name, Key    string
		Enabled          bool
		ConcurrencyLimit int `json:"concurrency_limit"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "My key" || !created.Enabled || created.ConcurrencyLimit != 0 {
		t.Fatalf("created key = %+v", created)
	}
	hash, ok := accesskey.Hash(created.Key)
	if !ok {
		t.Fatal("created secret is not a valid client key")
	}
	stored, err := f.store.FindAccessKeyByHash(t.Context(), hash)
	if err != nil || string(stored.Key.Identity().ID) != created.ID {
		t.Fatalf("created key not persisted: %v", err)
	}
	requireStatus(t, call(f.handler, "GET", "/api/openapi.json", "Bearer "+created.Key, ""), 401)
	for _, body := range []string{`{"concurrency_limit":-1}`, `{"concurrency_limit":-1}`} {
		response = call(f.handler, "PATCH", "/api/client-keys/"+created.ID, authorization, body)
		requireStatus(t, response, 204)
		if response.Body.Len() != 0 {
			t.Fatal("204 has body")
		}
	}
	requireStatus(t, call(f.handler, "POST", "/api/client-keys/"+created.ID+"/revoke", authorization, ""), 204)
	requireStatus(t, call(f.handler, "PATCH", "/api/client-keys/"+created.ID, authorization, `{"concurrency_limit":2}`), 204)
	response = call(f.handler, "GET", "/api/client-keys", authorization, "")
	requireStatus(t, response, 200)
	var listed struct {
		Items                []map[string]any
		Total, Limit, Offset int
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Total != 1 || listed.Limit != 50 || listed.Offset != 0 || len(listed.Items) != 1 {
		t.Fatalf("list envelope = %+v", listed)
	}
	want := map[string]any{"id": created.ID, "name": "My key", "enabled": false, "concurrency_limit": float64(2)}
	if !reflect.DeepEqual(listed.Items[0], want) {
		t.Fatalf("key metadata = %v, want %v", listed.Items[0], want)
	}
	if strings.Contains(response.Body.String(), created.Key) || strings.Contains(f.logs.String(), created.Key) {
		t.Fatal("key secret retained in response or logs")
	}
}
