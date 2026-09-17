// Package app composes the gateway and owns application startup and shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/deyna256/clan/internal/codex"
	"github.com/deyna256/clan/internal/codexoauth"
	"github.com/deyna256/clan/internal/credentialcipher"
	"github.com/deyna256/clan/internal/execution"
	"github.com/deyna256/clan/internal/gateway"
	"github.com/deyna256/clan/internal/management"
	"github.com/deyna256/clan/internal/storage"
)

const shutdownTimeout = 30 * time.Second

type application struct {
	server         *http.Server
	listener       net.Listener
	callback       net.Listener
	cancelRequests context.CancelFunc
	executor       *execution.Executor
	oauth          *codexoauth.Manager
	store          *storage.Store
	httpClient     *http.Client
	codexClient    *codex.Client
}

// Run reads environment configuration and serves until ctx is canceled or serving
// fails. A shutdown error requires process termination: owned work may still run.
func Run(ctx context.Context, logger *slog.Logger) error {
	c, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	defer clear(c.encryptionKey)
	client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	a, err := newApplication(ctx, c, logger, client, "", "")
	if err != nil {
		client.CloseIdleConnections()
		return err
	}
	return a.run(ctx, logger)
}

// Provider addresses are private test seams; production uses the protocol defaults.
func newApplication(
	ctx context.Context,
	c config,
	logger *slog.Logger,
	client *http.Client,
	issuer, codexURL string,
) (_ *application, err error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		return nil, errors.New("app: logger is required")
	}
	oauthClient, err := codexoauth.NewClient(client, issuer)
	if err != nil {
		return nil, fmt.Errorf("app: configuring OAuth client: %w", err)
	}
	codexClient, err := codex.NewClient(client, codexURL, "0.154.0")
	if err != nil {
		return nil, fmt.Errorf("app: configuring Codex client: %w", err)
	}
	a := &application{httpClient: client, codexClient: codexClient}
	defer func() {
		if err != nil {
			a.closeStartup()
		}
	}()
	cipher, err := credentialcipher.New(c.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("app: configuring credential cipher: %w", err)
	}
	store, err := storage.Open(ctx, c.dbPath, cipher)
	if err != nil {
		return nil, fmt.Errorf("app: opening the database: %w", err)
	}
	a.store = store
	listen := net.ListenConfig{}
	a.listener, err = listen.Listen(ctx, "tcp", c.listenAddr)
	if err != nil {
		return nil, fmt.Errorf("app: binding CLAN_LISTEN_ADDR: %w", err)
	}
	a.callback, err = listen.Listen(ctx, "tcp", c.callbackAddr)
	if err != nil {
		return nil, fmt.Errorf("app: binding CLAN_OAUTH_CALLBACK_ADDR: %w", err)
	}
	// OAuth saves must survive the signal that cancels inbound generation requests.
	a.oauth, err = codexoauth.NewManager(context.Background(), oauthClient, store, a.callback)
	if err != nil {
		return nil, fmt.Errorf("app: starting OAuth callback serving: %w", err)
	}
	a.executor, err = execution.New(store, a.oauth, codexClient, logger)
	if err != nil {
		return nil, fmt.Errorf("app: starting execution: %w", err)
	}
	admin, err := management.New(management.Config{AdminToken: c.adminToken}, store, a.oauth, a.executor, logger)
	if err != nil {
		return nil, fmt.Errorf("app: starting management: %w", err)
	}
	clients, err := gateway.New(a.executor, logger)
	if err != nil {
		return nil, fmt.Errorf("app: starting client API: %w", err)
	}
	router := chi.NewRouter()
	router.Handle("/api/*", admin)
	router.Handle("/api", admin)
	router.Handle("/v1/*", clients)
	router.Handle("/v1", clients)
	router.NotFound(clients.ServeHTTP)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("app: startup canceled: %w", ctx.Err())
	}
	requests, cancel := context.WithCancel(context.Background())
	a.cancelRequests = cancel
	a.server = &http.Server{
		Handler: router, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10,
		IdleTimeout: 60 * time.Second,
		BaseContext: func(net.Listener) context.Context { return requests },
		ErrorLog:    slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	return a, nil
}

func (a *application) closeStartup() {
	if a.executor != nil {
		a.executor.Close()
	}
	if a.oauth != nil {
		_ = a.oauth.Close()
	} else if a.callback != nil {
		_ = a.callback.Close()
	}
	if a.listener != nil {
		_ = a.listener.Close()
	}
	a.codexClient.CloseIdleConnections()
	a.httpClient.CloseIdleConnections()
	if a.store != nil {
		_ = a.store.Close()
	}
}

func (a *application) run(ctx context.Context, logger *slog.Logger) error {
	served := make(chan error, 1)
	go func() { served <- a.server.Serve(a.listener) }()
	logger.Info("gateway listening", "address", a.listener.Addr().String())
	var serveErr error
	servingStopped := false
	select {
	case <-ctx.Done():
	case err := <-served:
		servingStopped = true
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = fmt.Errorf("app: HTTP serving: %w", err)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.shutdown(shutdown) }()
	select {
	case err := <-done:
		if err == nil && !servingStopped {
			select {
			case err := <-served:
				if !errors.Is(err, http.ErrServerClosed) {
					serveErr = fmt.Errorf("app: HTTP serving: %w", err)
				}
			case <-shutdown.Done():
				return errors.New("app: shutdown deadline exceeded; cleanup is incomplete")
			}
		}
		return errors.Join(serveErr, err)
	case <-shutdown.Done():
		return errors.New("app: shutdown deadline exceeded; cleanup is incomplete")
	}
}

func (a *application) shutdown(ctx context.Context) error {
	a.cancelRequests()
	oauthStopped := make(chan error, 1)
	go func() { oauthStopped <- a.oauth.Shutdown(ctx) }()
	executed := make(chan struct{})
	go func() {
		a.executor.Close()
		close(executed)
	}()
	if err := a.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("app: HTTP shutdown: %w", err)
	}
	select {
	case <-executed:
	case <-ctx.Done():
		return errors.New("app: execution shutdown did not complete")
	}
	select {
	case err := <-oauthStopped:
		if err != nil {
			return fmt.Errorf("app: OAuth shutdown: %w", err)
		}
	case <-ctx.Done():
		return errors.New("app: OAuth shutdown did not complete")
	}
	a.codexClient.CloseIdleConnections()
	a.httpClient.CloseIdleConnections()
	if err := a.store.Close(); err != nil {
		return fmt.Errorf("app: closing the database: %w", err)
	}
	return nil
}
