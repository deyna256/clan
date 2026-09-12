package openai_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai"
	"github.com/stretchr/testify/require"
)

type conversationRequest struct {
	Method, Path, Body string
	Query              url.Values
}

func TestConversationLifecycle(t *testing.T) {
	requests := make(chan conversationRequest, 4)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- conversationRequest{Method: r.Method, Path: r.URL.Path, Body: string(body), Query: r.URL.Query()}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			_, _ = io.WriteString(w, `{"object":"conversation.deleted","id":"conv_1","deleted":false}`)
		} else {
			_, _ = io.WriteString(w, `{"object":"conversation","id":"conv_1","created_at":0,"metadata":{"revision":9007199254740993}}`)
		}
	}, io.Discard)
	want := openai.Conversation{ID: "conv_1", CreatedAt: 0, Metadata: json.RawMessage(`{"revision":9007199254740993}`)}

	created, err := client.CreateConversation(t.Context(), testAttempt(), openai.ConversationCreateOptions{Items: generation.Some([]generation.Item{generation.Message{Role: generation.User, Parts: []generation.Part{generation.Text{Text: "Hello"}}}}), Metadata: generation.Null[map[string]string]()})

	require.NoError(t, err)
	require.Equal(t, want, created)
	request := <-requests
	if request.Method != "POST" || request.Path != "/conversations" {
		t.Fatalf("request = %#v", request)
	}
	assertRequestJSON(t, request.Body, `{"items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}],"metadata":null}`)

	retrieved, err := client.RetrieveConversation(t.Context(), testAttempt(), "conv_1")

	require.NoError(t, err)
	require.Equal(t, want, retrieved)
	request = <-requests
	if request.Method != "GET" || request.Path != "/conversations/conv_1" || request.Body != "" {
		t.Fatalf("request = %#v", request)
	}

	_, err = client.UpdateConversation(t.Context(), testAttempt(), "conv_1", openai.ConversationUpdateOptions{Metadata: generation.Some(map[string]string{})})

	require.NoError(t, err)
	request = <-requests
	if request.Method != "POST" || request.Path != "/conversations/conv_1" {
		t.Fatalf("request = %#v", request)
	}
	assertRequestJSON(t, request.Body, `{"metadata":{}}`)

	deleted, err := client.DeleteConversation(t.Context(), testAttempt(), "conv_1")

	if err != nil || deleted != (openai.ConversationDeleted{ID: "conv_1", Deleted: false}) {
		t.Fatalf("DeleteConversation = %#v, %v", deleted, err)
	}
	request = <-requests
	if request.Method != "DELETE" || request.Path != "/conversations/conv_1" {
		t.Fatalf("request = %#v", request)
	}
}

func TestConversationItemOperations(t *testing.T) {
	requests := make(chan conversationRequest, 4)
	message := `{"type":"message","id":"msg_1","role":"user","status":"completed","content":[{"type":"input_text","text":"Hello"}]}`
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- conversationRequest{Method: r.Method, Path: r.URL.Path, Body: string(body), Query: r.URL.Query()}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "DELETE":
			_, _ = io.WriteString(w, `{"object":"conversation","id":"conv_1","created_at":1,"metadata":null}`)
		case strings.HasSuffix(r.URL.Path, "/msg_1"):
			_, _ = io.WriteString(w, message)
		default:
			_, _ = io.WriteString(w, `{"object":"list","data":[`+message+`],"first_id":"msg_1","last_id":"msg_1","has_more":true}`)
		}
	}, io.Discard)
	wantMessage := generation.Message{ID: "msg_1", Role: generation.User, Status: generation.ItemCompleted, Parts: []generation.Part{generation.Text{Text: "Hello"}}}
	wantPage := openai.ItemPage{Data: []generation.Item{wantMessage}, FirstID: "msg_1", LastID: "msg_1", HasMore: true}

	added, err := client.AddConversationItems(t.Context(), testAttempt(), "conv_1", openai.ConversationItemsCreateOptions{Items: []generation.Item{}, Include: []string{"reasoning.encrypted_content"}})

	require.NoError(t, err)
	require.Equal(t, wantPage, added)
	request := <-requests
	if request.Method != "POST" || request.Path != "/conversations/conv_1/items" || request.Query.Get("include[]") != "reasoning.encrypted_content" {
		t.Fatalf("request = %#v", request)
	}
	assertRequestJSON(t, request.Body, `{"items":[]}`)

	listed, err := client.ListConversationItems(t.Context(), testAttempt(), "conv_1", openai.ItemListOptions{After: "msg_0", Limit: generation.Some(int64(1)), Order: "asc", Include: []string{"message.input_image.image_url", "message.output_text.logprobs"}})

	require.NoError(t, err)
	require.Equal(t, wantPage, listed)
	request = <-requests
	wantQuery := url.Values{"after": {"msg_0"}, "limit": {"1"}, "order": {"asc"}, "include[]": {"message.input_image.image_url", "message.output_text.logprobs"}}
	require.Equal(t, "GET", request.Method)
	require.Equal(t, "/conversations/conv_1/items", request.Path)
	require.Equal(t, wantQuery, request.Query)

	item, err := client.RetrieveConversationItem(t.Context(), testAttempt(), "conv_1", "msg_1", nil)

	require.NoError(t, err)
	require.Equal(t, wantMessage, item)
	request = <-requests
	if request.Method != "GET" || request.Path != "/conversations/conv_1/items/msg_1" {
		t.Fatalf("request = %#v", request)
	}

	conversation, err := client.DeleteConversationItem(t.Context(), testAttempt(), "conv_1", "msg_1")

	if err != nil || conversation.ID != "conv_1" || string(conversation.Metadata) != "null" {
		t.Fatalf("DeleteConversationItem = %#v, %v", conversation, err)
	}
	request = <-requests
	if request.Method != "DELETE" || request.Path != "/conversations/conv_1/items/msg_1" {
		t.Fatalf("request = %#v", request)
	}
}

func TestConversationEmptyPage(t *testing.T) {
	client := testClient(t, staticJSON(`{"object":"list","data":[],"first_id":"","last_id":"","has_more":false}`), io.Discard)

	page, err := client.ListConversationItems(t.Context(), testAttempt(), "conv_1", openai.ItemListOptions{})

	require.NoError(t, err)
	require.Equal(t, openai.ItemPage{Data: []generation.Item{}}, page)
}

func TestConversationInputErrorsDoNotDispatch(t *testing.T) {
	var calls atomic.Int64
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }, io.Discard)
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{name: "blank ID", call: func() error { _, err := client.RetrieveConversation(t.Context(), testAttempt(), ""); return err }},
		{name: "path ID", call: func() error {
			_, err := client.DeleteConversationItem(t.Context(), testAttempt(), "conv_1", "../private")
			return err
		}},
		{name: "missing metadata", call: func() error {
			_, err := client.UpdateConversation(t.Context(), testAttempt(), "conv_1", openai.ConversationUpdateOptions{})
			return err
		}},
		{name: "invalid metadata", call: func() error {
			_, err := client.CreateConversation(t.Context(), testAttempt(), openai.ConversationCreateOptions{Metadata: generation.Some(map[string]string{"key": "private\xff"})})
			return err
		}},
		{name: "too many items", call: func() error {
			_, err := client.AddConversationItems(t.Context(), testAttempt(), "conv_1", openai.ConversationItemsCreateOptions{Items: make([]generation.Item, 21)})
			return err
		}},
		{name: "zero limit", call: func() error {
			_, err := client.ListConversationItems(t.Context(), testAttempt(), "conv_1", openai.ItemListOptions{Limit: generation.Some(int64(0))})
			return err
		}},
		{name: "invalid order", call: func() error {
			_, err := client.ListConversationItems(t.Context(), testAttempt(), "conv_1", openai.ItemListOptions{Order: "private"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()

			var input *openai.InputError
			if !errors.As(err, &input) || strings.Contains(err.Error(), "private") {
				t.Fatalf("error = %v; want safe input error", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid inputs dispatched %d requests", calls.Load())
	}
}

func TestConversationMalformedReplies(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		page        bool
		unsupported bool
	}{
		{name: "missing metadata", body: `{"object":"conversation","id":"conv_1","created_at":0}`},
		{name: "null time", body: `{"object":"conversation","id":"conv_1","created_at":null,"metadata":{}}`},
		{name: "wrong object", body: `{"object":"file","id":"conv_1","created_at":0,"metadata":{}}`},
		{name: "page missing has_more", body: `{"object":"list","data":[],"first_id":"","last_id":""}`, page: true},
		{name: "page null data", body: `{"object":"list","data":null,"first_id":"","last_id":"","has_more":false}`, page: true},
		{name: "unknown item after valid item", body: `{"object":"list","data":[{"id":"msg_1","type":"message","role":"user","content":[]},{"id":"x","type":"future"}],"first_id":"msg_1","last_id":"x","has_more":false}`, page: true, unsupported: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, staticJSON(tc.body), io.Discard)
			var err error
			var page openai.ItemPage

			if tc.page {
				page, err = client.ListConversationItems(t.Context(), testAttempt(), "conv_1", openai.ItemListOptions{})
			} else {
				_, err = client.RetrieveConversation(t.Context(), testAttempt(), "conv_1")
			}

			var failure *generation.Failure
			wantKind := generation.ProtocolError
			if tc.unsupported {
				wantKind = generation.Unsupported
			}
			if !errors.As(err, &failure) || failure.Kind != wantKind || page.Data != nil {
				t.Fatalf("reply = %#v, %v; want %s without partial page", page, err, wantKind)
			}
		})
	}
}

func TestConversationOperationsRejectMismatchedIDs(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		call func(*openai.Client) error
	}{
		{name: "retrieve conversation", body: `{"object":"conversation","id":"other","created_at":0,"metadata":null}`, call: func(client *openai.Client) error {
			_, err := client.RetrieveConversation(t.Context(), testAttempt(), "conv_1")
			return err
		}},
		{name: "update conversation", body: `{"object":"conversation","id":"other","created_at":0,"metadata":null}`, call: func(client *openai.Client) error {
			_, err := client.UpdateConversation(t.Context(), testAttempt(), "conv_1", openai.ConversationUpdateOptions{Metadata: generation.Null[map[string]string]()})
			return err
		}},
		{name: "delete conversation", body: `{"object":"conversation.deleted","id":"other","deleted":true}`, call: func(client *openai.Client) error {
			_, err := client.DeleteConversation(t.Context(), testAttempt(), "conv_1")
			return err
		}},
		{name: "delete item conversation", body: `{"object":"conversation","id":"other","created_at":0,"metadata":null}`, call: func(client *openai.Client) error {
			_, err := client.DeleteConversationItem(t.Context(), testAttempt(), "conv_1", "msg_1")
			return err
		}},
		{name: "retrieve item", body: `{"type":"message","id":"other","role":"user","content":[]}`, call: func(client *openai.Client) error {
			_, err := client.RetrieveConversationItem(t.Context(), testAttempt(), "conv_1", "msg_1", nil)
			return err
		}},
		{name: "retrieve configuration", body: `{"type":"configuration_update","id":"other"}`, call: func(client *openai.Client) error {
			_, err := client.RetrieveConversationItem(t.Context(), testAttempt(), "conv_1", "cfg_1", nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, staticJSON(tc.body), io.Discard)

			err := tc.call(client)

			assertProtocolError(t, err)
		})
	}
}
