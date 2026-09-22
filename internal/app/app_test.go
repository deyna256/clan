package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/credentialcipher"
	"github.com/deyna256/clan/internal/storage"
)

const terminalResponse = `{"id":"resp_e2e","object":"response","status":"completed",` +
	`"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],` +
	`"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`
const createdEvent = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_e2e","status":"in_progress"}}`

func TestApplicationLoginGenerationAndRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("HTTP and temporary SQLite integration")
	}
	firstEvent, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var generation atomic.Int32
	provider := fakeProvider(t, func(w http.ResponseWriter, r *http.Request) {
		number := generation.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+createdEvent+"\n\n")
		if number == 2 {
			_ = http.NewResponseController(w).Flush()
			close(firstEvent)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":"+terminalResponse+"}\n\n")
	})
	c := testConfig(t)
	running := startApplication(t, c, provider)
	accountID := connectAccount(t, running, c.adminToken)
	key := createKey(t, running.address, c.adminToken)
	assertModels(t, running.address, key)
	response := request(t, "POST", running.address+"/v1/responses", key, `{"model":"model","input":"hello"}`, 200)
	var result struct {
		Status string
		Output []struct{ Content []struct{ Text string } }
	}
	err := json.Unmarshal(response, &result)
	if err != nil || result.Status != "completed" || len(result.Output) != 1 ||
		len(result.Output[0].Content) != 1 || result.Output[0].Content[0].Text != "Hello" {
		t.Fatalf("ordinary response = %s, error = %v", response, err)
	}

	req, err := http.NewRequest("POST", running.address+"/v1/responses", strings.NewReader(`{"model":"model","input":"hello","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	stream, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	reader := bufio.NewReader(stream.Body)
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		event.WriteString(line)
		if line == "\n" {
			break
		}
	}
	if stream.StatusCode != 200 || !strings.Contains(event.String(), "response.created") {
		t.Fatalf("first streamed event = %d %s", stream.StatusCode, event.String())
	}
	<-firstEvent
	unblock()
	rest, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(rest), "response.completed") || strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("remaining SSE = %s, error = %v", rest, err)
	}
	if err := stream.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := running.stop(); err != nil {
		t.Fatal(err)
	}

	restarted := startApplication(t, c, provider)
	assertModels(t, restarted.address, key)
	response = request(t, "GET", restarted.address+"/api/accounts/"+accountID, c.adminToken, "", 200)
	if !strings.Contains(string(response), `"state":"connected"`) || strings.Contains(string(response), "provider-access") {
		t.Fatalf("restarted account status = %s", response)
	}
}

func TestApplicationShutdownCancelsActiveGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("HTTP and temporary SQLite integration")
	}
	entered, canceled := make(chan struct{}), make(chan struct{})
	provider := fakeProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+createdEvent+"\n\n")
		_ = http.NewResponseController(w).Flush()
		close(entered)
		<-r.Context().Done()
		close(canceled)
	})
	c := testConfig(t)
	running := startApplication(t, c, provider)
	connectAccount(t, running, c.adminToken)
	key := createKey(t, running.address, c.adminToken)
	req, err := http.NewRequest("POST", running.address+"/v1/responses", strings.NewReader(`{"model":"model","input":"hello","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	stream, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	<-entered

	if err := running.stop(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown left provider generation active")
	}
	if connection, err := net.DialTimeout("tcp", running.app.listener.Addr().String(), time.Second); err == nil {
		connection.Close()
		t.Fatal("shutdown left gateway listener open")
	}
	cipher, err := credentialcipher.New(c.encryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(t.Context(), c.dbPath, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	row, err := reopened.GetRequest(t.Context(), stream.Header.Get("X-Request-ID"))
	if err != nil || row.Result != "canceled" || !row.ResponseStarted {
		t.Fatalf("shutdown record=%+v err=%v", row, err)
	}
}

func TestApplicationShutdownPersistsPendingOAuthExchange(t *testing.T) {
	if testing.Short() {
		t.Skip("HTTP and temporary SQLite integration")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			writeTokens(w)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(provider.Close)
	t.Cleanup(provider.CloseClientConnections)
	c := testConfig(t)
	running := startApplication(t, c, provider)
	accountID, callback := beginLogin(t, running, c.adminToken)
	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		response, _ := http.Get(callback)
		if response != nil {
			response.Body.Close()
		}
	}()
	<-entered
	stopping := make(chan struct{})
	running.app.server.RegisterOnShutdown(func() { close(stopping) })
	stopped := make(chan error, 1)

	go func() { stopped <- running.stop() }()
	<-stopping
	select {
	case err := <-stopped:
		t.Fatalf("shutdown returned before OAuth exchange finished: %v", err)
	default:
	}
	unblock()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	<-callbackDone

	restarted := startApplication(t, c, provider)
	body := request(t, "GET", restarted.address+"/api/accounts/"+accountID, c.adminToken, "", 200)
	if !strings.Contains(string(body), `"state":"connected"`) {
		t.Fatalf("shutdown lost successful OAuth persistence: %s", body)
	}
}

func TestShutdownDeadlineLeavesDatabaseOpenUntilHTTPJoins(t *testing.T) {
	if testing.Short() {
		t.Skip("HTTP and temporary SQLite integration")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := newApplication(t.Context(), testConfig(t), logger, &http.Client{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	a.server.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	served := make(chan error, 1)
	go func() { served <- a.server.Serve(a.listener) }()
	read := make(chan struct{})
	go func() {
		defer close(read)
		response, _ := http.Get("http://" + a.listener.Addr().String())
		if response != nil {
			response.Body.Close()
		}
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err = a.shutdown(ctx)

	if err == nil {
		t.Fatal("shutdown claimed completion while HTTP handler was blocked")
	}
	if _, err := a.store.ListAccounts(t.Context()); err != nil {
		t.Fatalf("deadline closed SQLite while a handler still owned it: %v", err)
	}
	if connection, err := net.DialTimeout("tcp", a.callback.Addr().String(), time.Second); err == nil {
		connection.Close()
		t.Fatal("blocked HTTP cleanup left OAuth callback intake open")
	}
	unblock()
	<-read
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("HTTP serving ended with %v", err)
	}
}

func TestStartupFailureReleasesBoundListener(t *testing.T) {
	if testing.Short() {
		t.Skip("HTTP and temporary SQLite integration")
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	available, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := available.Addr().String()
	available.Close()
	c := testConfig(t)
	c.listenAddr, c.callbackAddr = address, occupied.Addr().String()

	_, err = newApplication(t.Context(), c, slog.New(slog.NewTextHandler(io.Discard, nil)), &http.Client{}, "", "")

	if err == nil {
		t.Fatal("occupied callback address was accepted")
	}
	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("startup leaked main listener: %v", err)
	}
	rebound.Close()
}

func TestStartupReportsUnderlyingCause(t *testing.T) {
	if testing.Short() {
		t.Skip("HTTP and temporary SQLite integration")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("missing database directory", func(t *testing.T) {
		c := testConfig(t)
		c.dbPath = filepath.Join(t.TempDir(), "nonexistent", "dir", "clan.db")

		_, err := newApplication(t.Context(), c, logger, &http.Client{}, "", "")

		if err == nil {
			t.Fatal("startup accepted missing database directory")
		}
		if !strings.HasPrefix(err.Error(), "app: opening the database: ") {
			t.Fatalf("startup error = %q, want prefix %q", err, "app: opening the database: ")
		}
		if errors.Unwrap(err) == nil {
			t.Fatalf("startup error %q dropped underlying cause", err)
		}
		if strings.Contains(err.Error(), c.adminToken) || strings.Contains(err.Error(), string(c.encryptionKey)) {
			t.Fatalf("startup error exposed secrets: %v", err)
		}
	})

	t.Run("busy listen address", func(t *testing.T) {
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()

		c := testConfig(t)
		c.listenAddr = occupied.Addr().String()

		_, err = newApplication(t.Context(), c, logger, &http.Client{}, "", "")

		if err == nil {
			t.Fatal("startup accepted busy listen address")
		}
		if !strings.HasPrefix(err.Error(), "app: binding CLAN_LISTEN_ADDR: ") {
			t.Fatalf("startup error = %q, want prefix %q", err, "app: binding CLAN_LISTEN_ADDR: ")
		}
		var opErr *net.OpError
		if !errors.As(err, &opErr) {
			t.Fatalf("startup error %q did not wrap net.OpError", err)
		}
		if strings.Contains(err.Error(), c.adminToken) || strings.Contains(err.Error(), string(c.encryptionKey)) {
			t.Fatalf("startup error exposed secrets: %v", err)
		}
	})

	t.Run("busy callback address", func(t *testing.T) {
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()

		c := testConfig(t)
		c.callbackAddr = occupied.Addr().String()

		_, err = newApplication(t.Context(), c, logger, &http.Client{}, "", "")

		if err == nil {
			t.Fatal("startup accepted busy callback address")
		}
		if !strings.HasPrefix(err.Error(), "app: binding CLAN_OAUTH_CALLBACK_ADDR: ") {
			t.Fatalf("startup error = %q, want prefix %q", err, "app: binding CLAN_OAUTH_CALLBACK_ADDR: ")
		}
		var opErr *net.OpError
		if !errors.As(err, &opErr) {
			t.Fatalf("startup error %q did not wrap net.OpError", err)
		}
		if strings.Contains(err.Error(), c.adminToken) || strings.Contains(err.Error(), string(c.encryptionKey)) {
			t.Fatalf("startup error exposed secrets: %v", err)
		}
	})
}

func TestServingFailureReportsUnderlyingCause(t *testing.T) {
	if testing.Short() {
		t.Skip("HTTP and temporary SQLite integration")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := testConfig(t)
	a, err := newApplication(t.Context(), c, logger, &http.Client{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.run(t.Context(), logger) }()

	if err := a.listener.Close(); err != nil {
		t.Fatalf("closing listener: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("run succeeded after listener was closed")
		}
		if !strings.HasPrefix(err.Error(), "app: HTTP serving: ") {
			t.Fatalf("run error = %q, want prefix %q", err, "app: HTTP serving: ")
		}
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("run error %q did not wrap net.ErrClosed", err)
		}
		if strings.Contains(err.Error(), c.adminToken) || strings.Contains(err.Error(), string(c.encryptionKey)) {
			t.Fatalf("run error exposed secrets: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop within five seconds after listener was closed")
	}
}

type runningApplication struct {
	app     *application
	address string
	stop    func() error
}

func startApplication(t *testing.T, c config, provider *httptest.Server) runningApplication {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := newApplication(t.Context(), c, logger, provider.Client(), provider.URL, provider.URL+"/backend-api/codex")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, logger) }()
	stop := sync.OnceValue(func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			return errors.New("application shutdown did not finish within five seconds")
		}
	})
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	})
	return runningApplication{app: a, address: "http://" + a.listener.Addr().String(), stop: stop}
}

func testConfig(t *testing.T) config {
	t.Helper()
	key := make([]byte, 32)
	return config{
		usageRetention: 90 * 24 * time.Hour,
		listenAddr:     "127.0.0.1:0", callbackAddr: "127.0.0.1:0",
		dbPath: filepath.Join(t.TempDir(), "clan.db"), adminToken: "admin-test-token", encryptionKey: key,
	}
}

func fakeProvider(t *testing.T, generate http.HandlerFunc) *httptest.Server {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			if r.FormValue("code") == "" || r.FormValue("code_verifier") == "" {
				t.Error("OAuth exchange omitted code or PKCE verifier")
			}
			writeTokens(w)
		case "/backend-api/codex/models":
			if r.URL.Query().Get("client_version") != "0.154.0" || r.Header.Get("Authorization") != "Bearer provider-access" {
				t.Error("model discovery omitted protocol version or OAuth credentials")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"models":[{"slug":"model","display_name":"Model","visibility":"list"}]}`)
		case "/backend-api/codex/responses":
			if r.Header.Get("ChatGPT-Account-Id") != "provider-account" {
				t.Error("generation omitted provider account identity")
			}
			generate(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)
	t.Cleanup(provider.CloseClientConnections)
	return provider
}

func connectAccount(t *testing.T, running runningApplication, adminToken string) string {
	t.Helper()
	accountID, callback := beginLogin(t, running, adminToken)
	request(t, "GET", callback, "", "", 200)
	return accountID
}

func beginLogin(t *testing.T, running runningApplication, adminToken string) (string, string) {
	t.Helper()
	body := request(t, "POST", running.address+"/api/oauth/login", adminToken, `{"name":"Primary"}`, 200)
	var login struct {
		AccountID string `json:"account_id"`
		URL       string `json:"authorization_url"`
	}
	if err := json.Unmarshal(body, &login); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(login.URL)
	if err != nil {
		t.Fatal(err)
	}
	callback := "http://" + running.app.callback.Addr().String() + "/auth/callback?state=" +
		url.QueryEscape(parsed.Query().Get("state")) + "&code=login-code"
	return login.AccountID, callback
}

func writeTokens(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	claims := `{"https://api.openai.com/auth":{"chatgpt_account_id":"provider-account"}}`
	identity := "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "provider-access", "refresh_token": "provider-refresh",
		"expires_in": 3600, "id_token": identity,
	})
}

func createKey(t *testing.T, address, adminToken string) string {
	t.Helper()
	body := request(t, "POST", address+"/api/client-keys", adminToken, `{"name":"Client","concurrency_limit":1}`, 201)
	var created struct{ Key string }
	if err := json.Unmarshal(body, &created); err != nil || created.Key == "" {
		t.Fatalf("key creation failed: %v", err)
	}
	return created.Key
}

func assertModels(t *testing.T, address, key string) {
	t.Helper()
	body := request(t, "GET", address+"/v1/models", key, "", 200)
	var models struct {
		Object string
		Data   []struct{ ID string }
	}
	if err := json.Unmarshal(body, &models); err != nil || models.Object != "list" || len(models.Data) != 1 || models.Data[0].ID != "model" {
		t.Fatalf("models = %s, error = %v", body, err)
	}
}

func request(t *testing.T, method, address, token, body string, status int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, address, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s = %d, want %d; body=%s", method, address, response.StatusCode, status, data)
	}
	return data
}
