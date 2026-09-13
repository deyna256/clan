package management_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/deyna256/clan/internal/storage"
)

func TestMutationWaitsForCleanup(t *testing.T) {
	for _, tt := range []struct {
		name, action, method, path, completion string
		status                                 int
	}{
		{name: "revoke completes", action: "revoke", method: "POST", path: "/api/client-keys/key/revoke", status: 204},
		{name: "disable completes", action: "disable", method: "POST", path: "/api/accounts/one/disable", status: 204},
		{name: "delete completes", action: "delete", method: "DELETE", path: "/api/accounts/one", status: 204},
		{name: "revoke deadline", action: "revoke", method: "POST", path: "/api/client-keys/key/revoke", completion: "deadline", status: 503},
		{name: "disable deadline", action: "disable", method: "POST", path: "/api/accounts/one/disable", completion: "deadline", status: 503},
		{name: "delete deadline", action: "delete", method: "DELETE", path: "/api/accounts/one", completion: "deadline", status: 503},
		{name: "revoke disconnect", action: "revoke", method: "POST", path: "/api/client-keys/key/revoke", completion: "disconnect", status: 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				body := &blockedBody{closing: make(chan struct{}), release: make(chan struct{})}
				unblock := sync.OnceFunc(func() { close(body.release) })
				defer unblock()
				f := newFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/models" {
						return response(`{"models":[{"slug":"model","visibility":"list"}]}`), nil
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
				}))
				f.addAccount(t, "one", "Main", true)
				key := f.addKey(t, "key", "Main", true)
				stream := f.open(t, key)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan *httptest.ResponseRecorder, 1)

				go func() {
					request := httptest.NewRequest(tt.method, tt.path, nil).WithContext(ctx)
					request.Header.Set("Authorization", authorization)
					response := httptest.NewRecorder()
					f.handler.ServeHTTP(response, request)
					done <- response
				}()
				<-body.closing
				synctest.Wait()

				select {
				case response := <-done:
					t.Fatalf("mutation returned before cleanup: %d", response.Code)
				default:
				}
				if tt.action == "revoke" {
					keys, err := f.store.ListAccessKeys(t.Context())
					if err != nil || len(keys) != 1 || keys[0].Key.Enabled() {
						t.Fatalf("revocation not persisted: %v", err)
					}
				} else {
					record, err := f.store.GetAccount(t.Context(), "one")
					if tt.action == "delete" && !errors.Is(err, storage.ErrNotFound) {
						t.Fatalf("deletion not persisted: %v", err)
					}
					if tt.action == "disable" && (err != nil || record.Enabled) {
						t.Fatalf("disable not persisted: %v", err)
					}
				}
				switch tt.completion {
				case "deadline":
					time.Sleep(31 * time.Second)
				case "disconnect":
					cancel()
				default:
					unblock()
				}
				response := <-done
				requireStatus(t, response, tt.status)
				if tt.status == 503 && (!strings.Contains(response.Body.String(), `"type":"/api/problems/completion-unconfirmed"`) ||
					!strings.Contains(response.Body.String(), "resource may already have changed")) {
					t.Fatalf("unconfirmed response = %s", response.Body)
				}
				unblock()
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := stream.Next(); !errors.Is(err, context.Canceled) {
					t.Fatalf("old stream survived mutation: %v", err)
				}
			})
		})
	}
}

func TestFailedMutationPreservesWorkAndRedactsDatabaseError(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/models" {
			return response(`{"models":[{"slug":"model","visibility":"list"}]}`), nil
		}
		return response("data: {\"type\":\"response.created\"}\n\n"), nil
	}))
	f.addAccount(t, "one", "Main", true)
	key := f.addKey(t, "key", "Main", true)
	stream := f.open(t, key)
	defer stream.Close()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TRIGGER reject_write BEFORE UPDATE OF enabled ON access_keys BEGIN SELECT RAISE(ABORT, 'credential-marker'); END`)
	if err != nil {
		t.Fatal(err)
	}

	response := call(f.handler, "POST", "/api/client-keys/key/revoke", authorization, "")

	requireStatus(t, response, 500)
	if event, err := stream.Next(); err != nil || !strings.Contains(string(event), "response.created") {
		t.Fatalf("failed persistence canceled work: %s, %v", event, err)
	}
	if strings.Contains(response.Body.String()+f.logs.String(), "credential-marker") {
		t.Fatal("database error leaked")
	}
}

type blockedBody struct {
	closing chan struct{}
	release chan struct{}
}

func (*blockedBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *blockedBody) Close() error           { close(b.closing); <-b.release; return nil }
