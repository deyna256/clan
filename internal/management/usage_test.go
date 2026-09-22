package management_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/deyna256/clan/internal/storage"
	"github.com/deyna256/clan/internal/usage"
)

func TestUsageSummaryNullsAndKnownZero(t *testing.T) {
	f := newFixture(t, nil)
	stamp := time.Date(2026, 9, 1, 23, 59, 59, 999000000, time.UTC)
	for _, row := range []storage.RequestRecord{
		{ID: "unknown", FinishedAt: stamp, KeyID: "key", Result: "invalid_request"},
		{ID: "partial", FinishedAt: stamp, KeyID: "key", Result: "completed", Model: "model", AccountID: "account",
			Usage: usage.Snapshot{Input: usage.Counter{Known: true}, Output: usage.Counter{Known: true, Tokens: 7}}},
	} {
		if err := f.store.InsertRequest(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}

	body := usageBody(t, call(f.handler, "GET", "/api/usage?from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z", authorization, ""))
	items := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items=%v", items)
	}
	item := items[0].(map[string]any)
	if item["requests"] != float64(2) || item["unknown_usage"] != float64(2) || item["input_tokens"] != float64(0) || item["output_tokens"] != float64(7) {
		t.Fatalf("summary=%v", item)
	}
	for _, name := range []string{"total_tokens", "model", "account_id", "day"} {
		if _, ok := item[name]; ok {
			t.Fatalf("unexpected %s in %v", name, item)
		}
	}

	body = usageBody(t, call(f.handler, "GET", "/api/usage?to=2026-09-02T00:00:00Z&group_by=account,model,day", authorization, ""))
	if body["from"] != "2026-08-03T00:00:00Z" {
		t.Fatalf("default from=%v", body["from"])
	}
	items = body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("groups=%v", items)
	}
	unknown := items[0].(map[string]any)
	for _, name := range []string{"account_id", "model"} {
		value, ok := unknown[name]
		if !ok || value != nil {
			t.Fatalf("missing null dimension: %v", unknown)
		}
	}
	if unknown["day"] != "2026-09-01" {
		t.Fatalf("day=%v", unknown)
	}
	if _, ok := unknown["input_tokens"]; ok {
		t.Fatal("invented zero")
	}
	body = usageBody(t, call(f.handler, "GET", "/api/usage?key_id=missing", authorization, ""))
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("empty items=%v", body)
	}
	row := usageBody(t, call(f.handler, "GET", "/api/requests/unknown", authorization, ""))
	for _, field := range []string{"model", "account_id", "input_tokens", "output_tokens", "total_tokens"} {
		if _, exists := row[field]; exists {
			t.Fatalf("unknown field present: %s", field)
		}
	}
	row = usageBody(t, call(f.handler, "GET", "/api/requests/partial", authorization, ""))
	if row["input_tokens"] != float64(0) || row["output_tokens"] != float64(7) {
		t.Fatalf("partial=%v", row)
	}
}

func TestRequestCursorFiltersAndPagination(t *testing.T) {
	f := newFixture(t, nil)
	stamp := time.Date(2026, 9, 1, 0, 0, 0, 124000000, time.UTC)
	for _, id := range []string{"a", "b", "c"} {
		if err := f.store.InsertRequest(t.Context(), storage.RequestRecord{ID: id, FinishedAt: stamp, KeyID: "key", Model: "model", Result: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, filter := range []string{"", "&from=2026-09-01T00:00:00.1235Z", "&to=2026-09-01T00:00:00.1245Z"} {
		first := usageBody(t, call(f.handler, "GET", "/api/requests?limit=1"+filter, authorization, ""))
		if got := first["items"].([]any)[0].(map[string]any)["id"]; got != "c" {
			t.Fatalf("first=%v", first)
		}
		cursor := first["next_cursor"].(string)
		next := usageBody(t, call(f.handler, "GET", "/api/requests?limit=2"+filter+"&cursor="+cursor, authorization, ""))
		items := next["items"].([]any)
		if len(items) != 2 || items[0].(map[string]any)["id"] != "b" || items[1].(map[string]any)["id"] != "a" {
			t.Fatalf("next=%v", next)
		}
		if _, exists := next["next_cursor"]; exists {
			t.Fatal("last page has cursor")
		}
	}
	first := usageBody(t, call(f.handler, "GET", "/api/requests?limit=1", authorization, ""))
	cursor := first["next_cursor"].(string)
	for _, tt := range []struct {
		name  string
		query string
	}{
		{name: "from", query: "from=2026-08-01T00:00:00Z"},
		{name: "to", query: "to=2026-10-01T00:00:00Z"},
		{name: "key_id", query: "key_id=other"},
		{name: "account_id", query: "account_id=other"},
		{name: "model", query: "model=other"},
		{name: "result", query: "result=canceled"},
	} {
		t.Run("changed filter/"+tt.name, func(t *testing.T) {
			response := call(f.handler, "GET", "/api/requests?cursor="+cursor+"&"+tt.query, authorization, "")

			requireStatus(t, response, 422)
		})
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		field string
	}{
		{name: "unknown field", field: `,"extra":true}`},
		{name: "duplicate field", field: `,"id":"duplicate"}`},
	} {
		t.Run("invalid cursor/"+tt.name, func(t *testing.T) {
			modified := strings.TrimSuffix(string(data), "}") + tt.field
			invalid := base64.RawURLEncoding.EncodeToString([]byte(modified))

			response := call(f.handler, "GET", "/api/requests?cursor="+invalid, authorization, "")

			requireStatus(t, response, 422)
		})
	}
	first = usageBody(t, call(f.handler, "GET", "/api/requests?limit=1&from=2026-09-01T00:00:00.000Z", authorization, ""))
	cursor = first["next_cursor"].(string)
	requireStatus(t, call(f.handler, "GET", "/api/requests?cursor="+cursor+"&from=2026-09-01T03:00:00%2B03:00", authorization, ""), 200)
	if _, err := f.store.DeleteRequestsBefore(t.Context(), stamp.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	last := usageBody(t, call(f.handler, "GET", "/api/requests?cursor="+cursor+"&from=2026-09-01T00:00:00Z", authorization, ""))
	if len(last["items"].([]any)) != 0 {
		t.Fatalf("retained rows=%v", last)
	}
	requireStatus(t, call(f.handler, "GET", "/api/requests/a", authorization, ""), 404)
}

func TestUsageValidation(t *testing.T) {
	f := newFixture(t, nil)
	for _, path := range []string{
		"/api/usage?group_by=key,key", "/api/usage?group_by=secret-marker", "/api/usage?result=completed",
		"/api/usage?from=2026-09-02T00:00:00Z&to=2026-09-01T00:00:00Z",
		"/api/usage?to=0000-01-01T00:00:00Z",
		"/api/requests?from=secret-marker", "/api/requests?limit=0", "/api/requests?limit=101",
		"/api/requests?cursor=invalid", "/api/requests?cursor=" + strings.Repeat("a", 2049),
		"/api/requests?cursor=" + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"a"}`)),
		"/api/usage?key_id=", "/api/requests?from=x&from=y",
	} {
		t.Run(path[:min(len(path), 90)], func(t *testing.T) {
			response := call(f.handler, "GET", path, authorization, "")
			requireStatus(t, response, 422)
			if strings.Contains(response.Body.String(), "secret-marker") {
				t.Fatal("query value reflected")
			}
		})
	}
}

func TestUsageAuthentication(t *testing.T) {
	f := newFixture(t, nil)

	for _, path := range []string{"/api/usage", "/api/requests", "/api/requests/id"} {
		requireStatus(t, call(f.handler, "GET", path, "Bearer wrong", ""), 401)
	}
}

func TestUsageSchemaOptionalAndNullableFields(t *testing.T) {
	f := newFixture(t, nil)

	schema := usageBody(t, call(f.handler, "GET", "/api/openapi.json", authorization, ""))

	schemas := schema["components"].(map[string]any)["schemas"].(map[string]any)
	tokens := []string{"input_tokens", "output_tokens", "total_tokens"}
	for _, tt := range []struct {
		name     string
		fields   []string
		wantType any
	}{
		{name: "RequestView", fields: tokens, wantType: "integer"},
		{name: "SummaryView", fields: tokens, wantType: "integer"},
		{name: "SummaryView", fields: []string{"account_id", "model"}, wantType: []any{"string", "null"}},
	} {
		view := schemas[tt.name].(map[string]any)
		properties := view["properties"].(map[string]any)
		required := view["required"].([]any)
		for _, field := range tt.fields {
			t.Run(tt.name+"/"+field, func(t *testing.T) {
				property, ok := properties[field].(map[string]any)
				if !ok || !reflect.DeepEqual(property["type"], tt.wantType) {
					t.Fatalf("type = %v, want %v", property["type"], tt.wantType)
				}
				if slices.Contains(required, any(field)) {
					t.Fatal("optional field is required")
				}
			})
		}
	}
}

func usageBody(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	requireStatus(t, response, 200)
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}
