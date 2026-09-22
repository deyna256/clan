package gateway_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/storage"
	"github.com/deyna256/clan/internal/usage"
)

func TestAccountingGenerationAndEarlyRejections(t *testing.T) {
	for _, tt := range []struct {
		name, path, body, media, encoding, result, model, account string
		limited                                                   bool
	}{
		{name: "JSON", body: input, result: "completed", model: "model", account: "one"},
		{name: "SSE", body: `{"model":"model","input":"private-marker","stream":true}`, result: "completed", model: "model", account: "one"},
		{name: "invalid JSON", body: "{private-marker", result: "invalid_request"},
		{name: "media", media: "text/plain", result: "unsupported_media_type"},
		{name: "encoding", encoding: "gzip", result: "unsupported_media_type"},
		{name: "query", path: "/v1/responses?bad=private-marker", result: "invalid_request"},
		{name: "JSON concurrency", limited: true, body: input, result: "concurrency_limit", model: "model"},
		{name: "SSE concurrency", limited: true, body: `{"model":"model","input":"x","stream":true}`, result: "concurrency_limit", model: "model"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "data: %s\n\n", completed) })
			if tt.limited {
				if err := f.executor.SetConcurrency(t.Context(), "key", 0); err != nil {
					t.Fatal(err)
				}
			}
			path, media := tt.path, tt.media
			if path == "" {
				path = "/v1/responses"
			}
			if media == "" {
				media = "application/json"
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(tt.body))
			r.Header.Set("Authorization", "Bearer "+f.key)
			r.Header.Set("Content-Type", media)
			if tt.encoding != "" {
				r.Header.Set("Content-Encoding", tt.encoding)
			}
			w := &accountingWriter{ResponseRecorder: httptest.NewRecorder()}

			f.handler.ServeHTTP(w, r)

			rows, err := f.store.ListRequests(t.Context(), storage.RequestFilter{}, nil, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("rows=%+v", rows)
			}
			row := rows[0]
			if row.ID != w.Header().Get("X-Request-ID") || row.KeyID != "key" || row.Result != tt.result || row.Model != tt.model || string(row.AccountID) != tt.account || !row.ResponseStarted {
				t.Fatalf("record=%+v", row)
			}
			if tt.result == "completed" {
				if row.Usage.Total.Tokens != 10 || !row.Usage.Total.Known {
					t.Fatalf("tokens=%+v", row.Usage)
				}
			} else if row.Usage != (usage.Snapshot{}) {
				t.Fatalf("rejected request usage=%+v, want unknown counters", row.Usage)
			}
			logs := f.logs.String()
			if strings.Count(logs, `"msg":"request finished"`) != 1 || strings.Contains(logs, "private-marker") || strings.Contains(logs, f.key) {
				t.Fatalf("unsafe or duplicate log: %s", logs)
			}
		})
	}
}

func TestAccountingExcludesUnauthenticatedAndNonGenerationRequests(t *testing.T) {
	for _, tt := range []struct {
		name, method, path string
		invalidKey         bool
		wantStatus         int
	}{
		{name: "invalid key", method: "POST", path: "/v1/responses", invalidKey: true, wantStatus: 401},
		{name: "catalog", method: "GET", path: "/v1/models", wantStatus: 200},
		{name: "route", method: "POST", path: "/v1/unknown", wantStatus: 404},
		{name: "method", method: "GET", path: "/v1/responses", wantStatus: 405},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected generation") })
			key := f.key
			if tt.invalidKey {
				key = "invalid"
			}
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.Header.Set("Authorization", "Bearer "+key)
			w := &accountingWriter{ResponseRecorder: httptest.NewRecorder()}

			f.handler.ServeHTTP(w, r)

			if w.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d", w.Code, tt.wantStatus)
			}
			rows, err := f.store.ListRequests(t.Context(), storage.RequestFilter{}, nil, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("excluded request recorded: %+v", rows)
			}
		})
	}
}

func TestAccountingDeliveryFailureAndCanceledUpload(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		before, after, canceled bool
		wantResult              string
		wantStarted             bool
	}{
		{name: "before commitment", before: true, wantResult: "delivery_failed", wantStarted: false},
		{name: "after commitment", after: true, wantResult: "delivery_failed", wantStarted: true},
		{name: "canceled upload", canceled: true, wantResult: "canceled", wantStarted: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected generation") })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{")).WithContext(ctx)
			if tt.canceled {
				r.Body = io.NopCloser(readFunc(func([]byte) (int, error) { cancel(); return 0, io.ErrUnexpectedEOF }))
			}
			r.Header.Set("Authorization", "Bearer "+f.key)
			r.Header.Set("Content-Type", "application/json")
			w := &accountingWriter{ResponseRecorder: httptest.NewRecorder(), failBegin: tt.before, failWrite: tt.after}

			f.handler.ServeHTTP(w, r)

			row, err := f.store.GetRequest(t.Context(), w.Header().Get("X-Request-ID"))
			if err != nil {
				t.Fatal(err)
			}
			if row.Result != tt.wantResult || row.ResponseStarted != tt.wantStarted {
				t.Fatalf("row=%+v, want result=%s response_started=%v", row, tt.wantResult, tt.wantStarted)
			}
		})
	}
}

func TestAccountingRetainsKeyRevokedDuringUpload(t *testing.T) {
	f := newFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("revoked key generated") })
	r := httptest.NewRequest("POST", "/v1/responses", nil)
	r.Header.Set("Authorization", "Bearer "+f.key)
	r.Header.Set("Content-Type", "application/json")
	reader := strings.NewReader(input)
	revoked := false
	r.Body = io.NopCloser(readFunc(func(p []byte) (int, error) {
		if !revoked {
			revoked = true
			if err := f.executor.RevokeKey(t.Context(), "key"); err != nil {
				return 0, err
			}
		}
		return reader.Read(p)
	}))
	w := &accountingWriter{ResponseRecorder: httptest.NewRecorder()}

	f.handler.ServeHTTP(w, r)

	row, err := f.store.GetRequest(t.Context(), w.Header().Get("X-Request-ID"))
	if err != nil || row.KeyID != "key" || row.Model != "model" || row.AccountID != "" || w.Code != 401 {
		t.Fatalf("row=%+v status=%d err=%v", row, w.Code, err)
	}
	if row.Result != "invalid_api_key" || !row.ResponseStarted || row.Usage != (usage.Snapshot{}) {
		t.Fatalf("revoked key outcome=%+v", row)
	}
}

func TestAccountingFailurePreservesResponseAndReleasesSlot(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "data: %s\n\n", completed) })
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_accounting BEFORE INSERT ON requests BEGIN SELECT RAISE(ABORT, 'secret-marker'); END`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(input))
		r.Header.Set("Authorization", "Bearer "+f.key)
		r.Header.Set("Content-Type", "application/json")
		w := &accountingWriter{ResponseRecorder: httptest.NewRecorder()}

		f.handler.ServeHTTP(w, r)

		if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"completed"`) {
			t.Fatalf("response=%d %s", w.Code, w.Body.String())
		}
	}
	logs := f.logs.String()
	if strings.Count(logs, `"msg":"request accounting failed"`) != 2 || strings.Contains(logs, "secret-marker") {
		t.Fatalf("warnings=%s", logs)
	}
}

type accountingWriter struct {
	*httptest.ResponseRecorder
	failBegin, failWrite bool
}

func (*accountingWriter) SetReadDeadline(time.Time) error { return nil }
func (w *accountingWriter) SetWriteDeadline(time.Time) error {
	if w.failBegin {
		return errors.New("unwritable")
	}
	return nil
}
func (w *accountingWriter) Write(p []byte) (int, error) {
	if w.failWrite {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}
func (*accountingWriter) FlushError() error { return nil }

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }
