package management_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestFilteredPages(t *testing.T) {
	f := newFixture(t, nil)
	f.addAccount(t, "z", "Alpha Team", true)
	f.addAccount(t, "a", "ALPHA", true)
	f.addAccount(t, "b", "Alpha disabled", false)
	f.addKey(t, "z", "Alpha Team", true)
	f.addKey(t, "a", "ALPHA", true)
	f.addKey(t, "b", "Alpha revoked", false)
	for _, tt := range []struct {
		path         string
		ids          []string
		total, limit int
		offset       int64
	}{
		{path: "/api/accounts?q=alpha&state=connected&limit=1&offset=1", ids: []string{"z"}, total: 2, limit: 1, offset: 1},
		{path: "/api/accounts?state=disabled", ids: []string{"b"}, total: 1, limit: 50},
		{path: "/api/accounts?q=B", ids: []string{"b"}, total: 1, limit: 50},
		{path: "/api/accounts?q=%25", ids: []string{}, total: 0, limit: 50},
		{path: "/api/client-keys?q=alpha&enabled=true&limit=1&offset=1", ids: []string{"z"}, total: 2, limit: 1, offset: 1},
		{path: "/api/client-keys?enabled=false", ids: []string{"b"}, total: 1, limit: 50},
		{path: "/api/client-keys?q=Z&limit=100", ids: []string{"z"}, total: 1, limit: 100},
		{path: "/api/client-keys?q=", ids: []string{"a", "b", "z"}, total: 3, limit: 50},
		{path: "/api/client-keys?offset=9223372036854775807", ids: []string{}, total: 3, limit: 50, offset: 9223372036854775807},
		{path: "/api/models?q=MODEL&limit=1", ids: []string{"model"}, total: 2, limit: 1},
		{path: "/api/models?q=useful", ids: []string{"model"}, total: 1, limit: 50},
		{path: "/api/models?offset=20", ids: []string{}, total: 2, limit: 50, offset: 20},
	} {
		t.Run(tt.path, func(t *testing.T) {
			response := call(f.handler, "GET", tt.path, authorization, "")

			requireStatus(t, response, 200)
			var page struct {
				Items        []struct{ ID string }
				Total, Limit int
				Offset       int64
			}
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(page.Items))
			for _, item := range page.Items {
				ids = append(ids, item.ID)
			}
			if page.Items == nil || !reflect.DeepEqual(ids, tt.ids) || page.Total != tt.total || page.Limit != tt.limit || page.Offset != tt.offset {
				t.Fatalf("page = %s, want ids=%v total=%d limit=%d offset=%d", response.Body, tt.ids, tt.total, tt.limit, tt.offset)
			}
			if strings.Contains(response.Body.String(), "credential-marker") || strings.Contains(response.Body.String(), "refresh-marker") {
				t.Fatal("credentials leaked")
			}
		})
	}
}

func TestAccountLifecycleAndSafeStatus(t *testing.T) {
	f := newFixture(t, nil)
	f.addAccount(t, "one", "Main", true)
	response := call(f.handler, "GET", "/api/accounts/one", authorization, "")
	requireStatus(t, response, 200)
	var status map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	expires, ok := status["expires_at"].(string)
	if len(status) != 4 || status["id"] != "one" || status["name"] != "Main" || status["state"] != "connected" || !ok || !strings.HasSuffix(expires, "Z") {
		t.Fatalf("account status = %v", status)
	}
	for _, action := range []string{"disable", "disable", "enable", "enable"} {
		requireStatus(t, call(f.handler, "POST", "/api/accounts/one/"+action, authorization, ""), 204)
		record, err := f.store.GetAccount(t.Context(), "one")
		if err != nil || record.Enabled != (action == "enable") {
			t.Fatalf("%s persisted state = %v, %v", action, record.Enabled, err)
		}
	}
	requireStatus(t, call(f.handler, "DELETE", "/api/accounts/one", authorization, ""), 204)
	for _, request := range []struct{ method, path string }{
		{method: "GET", path: "/api/accounts/one"}, {method: "DELETE", path: "/api/accounts/one"},
		{method: "POST", path: "/api/accounts/one/enable"}, {method: "POST", path: "/api/client-keys/missing/revoke"},
	} {
		requireStatus(t, call(f.handler, request.method, request.path, authorization, ""), 404)
	}
	response = call(f.handler, "GET", "/api/models", authorization, "")
	requireStatus(t, response, 200)
	var page struct {
		Items                []json.RawMessage
		Total, Limit, Offset int
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || len(page.Items) != 0 || page.Total != 0 || page.Limit != 50 || page.Offset != 0 {
		t.Fatalf("empty catalog = %s", response.Body)
	}
}

func TestLoginTargetingAndRetention(t *testing.T) {
	f := newFixture(t, nil)
	response := call(f.handler, "GET", "/api/oauth/login", authorization, "")
	requireStatus(t, response, 200)
	if strings.TrimSpace(response.Body.String()) != `{"state":"idle"}` {
		t.Fatalf("initial status = %s", response.Body)
	}
	requireStatus(t, call(f.handler, "POST", "/api/oauth/login/cancel", authorization, `{"login_id":"old"}`), 409)
	response = call(f.handler, "POST", "/api/oauth/login", authorization, `{"name":"Main"}`)
	requireStatus(t, response, 200)
	var login struct {
		LoginID   string `json:"login_id"`
		AccountID string `json:"account_id"`
		URL       string `json:"authorization_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if login.LoginID == "" || login.AccountID == "" || login.LoginID == login.AccountID || login.URL == "" {
		t.Fatalf("login = %+v", login)
	}
	requireStatus(t, call(f.handler, "POST", "/api/oauth/login", authorization, `{"name":"Other"}`), 409)
	response = call(f.handler, "POST", "/api/oauth/login/cancel", authorization, `{"login_id":"old"}`)
	requireStatus(t, response, 409)
	if !strings.Contains(response.Body.String(), `"type":"/api/problems/login-mismatch"`) {
		t.Fatalf("mismatch type = %s", response.Body)
	}
	response = call(f.handler, "GET", "/api/oauth/login", authorization, "")
	if strings.Contains(response.Body.String(), "authorization_url") || strings.Contains(response.Body.String(), "code_challenge") || !strings.Contains(response.Body.String(), `"state":"waiting"`) {
		t.Fatalf("unsafe or replaced pending status = %s", response.Body)
	}
	body := `{"login_id":"` + login.LoginID + `"}`
	for range 2 {
		requireStatus(t, call(f.handler, "POST", "/api/oauth/login/cancel", authorization, body), 204)
	}
	response = call(f.handler, "GET", "/api/oauth/login", authorization, "")
	if !strings.Contains(response.Body.String(), `"state":"canceled"`) || !strings.Contains(response.Body.String(), login.LoginID) {
		t.Fatalf("retained status = %s", response.Body)
	}
	records, err := f.store.ListAccounts(t.Context())
	if err != nil || len(records) != 0 {
		t.Fatalf("canceled login persisted accounts = %v, %v", records, err)
	}
	f.addAccount(t, "one", "Main", true)
	response = call(f.handler, "POST", "/api/accounts/one/reconnect", authorization, "")
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), login.LoginID) {
		t.Fatal("reconnect reused login ID")
	}
	requireStatus(t, call(f.handler, "POST", "/api/oauth/login/cancel", authorization, body), 409)
	requireStatus(t, call(f.handler, "POST", "/api/accounts/one/disable", authorization, ""), 204)
	requireStatus(t, call(f.handler, "POST", "/api/accounts/one/reconnect", authorization, ""), 409)
}

func TestProviderFailureIsSafe(t *testing.T) {
	f := newFixture(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response("credential-marker"), nil
	}))
	f.addAccount(t, "one", "Main", true)
	response := call(f.handler, "GET", "/api/models", authorization, "")

	requireStatus(t, response, 503)
	if strings.Contains(response.Body.String()+f.logs.String(), "credential-marker") {
		t.Fatal("provider response leaked")
	}
}
