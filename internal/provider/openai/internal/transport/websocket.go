package transport

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

// DialWebSocket opens one Responses session using the configured HTTP transport.
// On a rejected handshake, websocket retains at most 1024 response-body bytes.
func (c *Client) DialWebSocket(ctx context.Context, apiKey string) (*websocket.Conn, *http.Response, error) {
	if !validKey(apiKey) {
		return nil, nil, ErrInvalidKey
	}
	conn, response, err := websocket.Dial(ctx, c.root.JoinPath("responses").String(), &websocket.DialOptions{
		HTTPClient: &c.http,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + apiKey}},
	})
	if err != nil {
		return nil, response, &requestError{cause: err}
	}
	return conn, response, nil
}
