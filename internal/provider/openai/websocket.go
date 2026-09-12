package openai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/deyna256/clan/internal/usage"
)

type CreateOptions struct {
	Lane     string
	Generate generation.Optional[bool]
}

// SessionEvent contains exactly one of Event and Failure. An empty ResponseID
// on a request failure means that the provider did not identify a response.
type SessionEvent struct {
	Lane, ResponseID string
	Event            generation.Event
	Failure          *SessionFailure
}

type SessionFailure struct {
	Cause error
	Usage usage.Snapshot
}

func (f *SessionFailure) Error() string { return f.Cause.Error() }
func (f *SessionFailure) Unwrap() error { return f.Cause }

// Session has one Next consumer and permits concurrent SendCreate and Close.
// Its opening context controls the session lifetime. Admission and correlation
// belong to the caller; the adapter does not queue, retry, or reconnect requests.
// Next returning io.EOF means a clean connection close, not that all sent
// requests were answered. Construct sessions with Client.OpenSession.
type Session struct {
	ctx         context.Context
	cancel      context.CancelFunc
	conn        *websocket.Conn
	diagnostics *diagnostics
	lanes       map[string]*sessionLane
	queue       []SessionEvent
	limit, size int64
	terminal    error
	stopped     atomic.Bool
	closeOnce   sync.Once
	closeErr    error
}

type sessionLane struct {
	id        string
	state     *streamState
	sequence  *int64
	lastFrame [sha256.Size]byte
}

func (c *Client) OpenSession(ctx context.Context, attempt Attempt) (*Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	conn, response, err := c.transport.DialWebSocket(sessionCtx, attempt.Credentials.Key)
	if err != nil {
		cancel()
		if response == nil {
			return nil, transportFailure(err)
		}
		if response.Body != nil {
			defer response.Body.Close()
		}
		if response.StatusCode == http.StatusSwitchingProtocols {
			return nil, protocolError()
		}
		diagnostics := c.diagnostics(attempt)
		defer diagnostics.finish()
		var reader io.Reader = http.NoBody
		if response.Body != nil {
			reader = response.Body
		}
		body, readErr := readBounded(reader, c.config.MaxResponseBytes)
		if readErr != nil {
			return nil, transportFailure(readErr)
		}
		result, cause := decodeHTTPFailure(response, body, diagnostics)
		return nil, &StartupError{Result: result, Cause: cause}
	}
	conn.SetReadLimit(int64(c.config.MaxEventBytes))
	session := &Session{ctx: sessionCtx, cancel: cancel, conn: conn, diagnostics: c.diagnostics(attempt), lanes: make(map[string]*sessionLane), limit: c.config.MaxResponseBytes}
	context.AfterFunc(sessionCtx, func() { _ = session.Close() })
	return session, nil
}

// SendCreate validates and writes one request. Its context governs only this
// call; cancellation during a write closes the session. After a successful send,
// callers must Close the session to cancel an admitted request.
func (s *Session) SendCreate(ctx context.Context, request generation.Request, options CreateOptions) error {
	if err := ctx.Err(); err != nil {
		_ = s.Close()
		return err
	}
	if s.stopped.Load() || s.ctx.Err() != nil {
		return context.Canceled
	}
	payload, err := wire.EncodeWebSocketCreate(request, options.Lane, options.Generate)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	if err := s.conn.Write(writeCtx, websocket.MessageText, payload); err != nil {
		_ = s.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return transportFailure(err)
	}
	return nil
}

func (s *Session) Next() (SessionEvent, error) {
	for {
		if s.terminal != nil && len(s.queue) == 0 {
			return SessionEvent{}, s.terminal
		}
		if s.terminal == nil && (s.stopped.Load() || s.ctx.Err() != nil) {
			s.queue = slices.DeleteFunc(s.queue, func(event SessionEvent) bool {
				_, keep := event.Event.(generation.UsageUpdated)
				return !keep
			})
			err := s.ctx.Err()
			if err == nil {
				err = context.Canceled
			}
			s.finish(err)
		}
		if len(s.queue) != 0 {
			event := s.queue[0]
			s.queue[0] = SessionEvent{}
			s.queue = s.queue[1:]
			return event, nil
		}
		if s.terminal != nil {
			continue
		}
		kind, data, err := s.conn.Read(s.ctx)
		if err != nil {
			s.finish(s.readFailure(err))
			continue
		}
		if kind != websocket.MessageText || !utf8.Valid(data) {
			s.finish(protocolError())
			continue
		}
		if err := s.consume(data); err != nil {
			s.finish(err)
		}
	}
}

func (s *Session) consume(data []byte) error {
	var frame struct {
		responseEvent
		Lane   generation.Optional[string] `json:"stream_id"`
		Status int                         `json:"status"`
		Error  *struct {
			Type    string                      `json:"type"`
			Code    generation.Optional[string] `json:"code"`
			Headers json.RawMessage             `json:"headers"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &frame) != nil || frame.Type == "" || frame.Lane.IsNull() {
		return protocolError()
	}
	name, named := frame.Lane.Value()
	if (named && name == "") || wire.ValidateWebSocketLane(name) != nil {
		return protocolError()
	}
	if frame.Type == "error" {
		if frame.Error == nil {
			return protocolError()
		}
		code, _ := frame.Error.Code.Value()
		if frame.Error.Type == "invalid_request_error" && (code == "previous_response_not_found" || code == "invalid_stream_id" || code == "websocket_stream_limit_reached" || code == "websocket_connection_limit_reached") {
			code = "invalid_request_error"
		}
		headers := make(http.Header)
		var fields map[string]json.RawMessage
		if len(frame.Error.Headers) != 0 && json.Unmarshal(frame.Error.Headers, &fields) != nil {
			s.diagnostics.warn("invalid_error_headers", "error", "skipped")
		}
		for key, raw := range fields {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				s.diagnostics.warn("invalid_error_headers", "error", "skipped")
				continue
			}
			headers.Set(key, value)
		}
		cause := classifyHTTPFailure(frame.Status, headers, wire.ProviderError{Code: code})
		if !named {
			return cause
		}
		s.queue = append(s.queue, SessionEvent{Lane: name, Failure: &SessionFailure{Cause: cause}})
		return nil
	}
	if frame.Type == "response.ping" || frame.Type == "ping" || frame.Type == "response.queued" {
		return nil
	}
	lane := s.lanes[name]
	terminal := frame.Type == "response.completed" || frame.Type == "response.incomplete" || frame.Type == "response.failed"
	lifecycle := frame.Type == "response.created" || frame.Type == "response.in_progress" || terminal
	if !lifecycle && (lane == nil || lane.state == nil) {
		idle := streamState{items: make(map[int]*streamItem), diagnostics: s.diagnostics, limit: s.limit}
		events, err := idle.consume(frame.responseEvent)
		if len(events) != 0 || idle.started || len(idle.items) != 0 {
			return protocolError()
		}
		return err
	}
	if lane == nil {
		lane = &sessionLane{}
		s.lanes[name] = lane
		s.size += int64(128 + len(name))
	}
	if frame.Sequence != nil && lane.sequence != nil && *frame.Sequence == *lane.sequence && sha256.Sum256(data) == lane.lastFrame {
		return nil
	}
	before := lane.bytes()
	if lifecycle {
		var identity struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(frame.Response, &identity) != nil || identity.ID == "" {
			return protocolError()
		}
		if lane.state != nil && lane.id != identity.ID {
			return protocolError()
		}
		if lane.state == nil {
			if lane.id == identity.ID {
				return protocolError()
			}
			lane.id = identity.ID
			lane.state = &streamState{items: make(map[int]*streamItem), diagnostics: s.diagnostics}
			lane.sequence, lane.lastFrame = nil, [sha256.Size]byte{}
		}
	}
	if lane.state == nil {
		return protocolError()
	}
	if frame.Sequence != nil {
		fingerprint := sha256.Sum256(data)
		if *frame.Sequence < 0 || (lane.sequence != nil && *frame.Sequence <= *lane.sequence) {
			return protocolError()
		}
		lane.sequence, lane.lastFrame = frame.Sequence, fingerprint
	}
	if frame.Type != "response.shell_call_output_content.delta" && len(frame.RawDelta) != 0 {
		var delta *string
		if json.Unmarshal(frame.RawDelta, &delta) == nil {
			frame.Delta = delta
		}
	}
	// Each lane receives only the budget left after all other retained lanes.
	lane.state.limit = max(0, s.limit-(s.size-lane.state.size)-(lane.bytes()-before))
	frame.raw = string(data)
	events, err := lane.state.consume(frame.responseEvent)
	s.size += lane.bytes() - before
	if s.size > s.limit {
		events = slices.DeleteFunc(events, func(event generation.Event) bool {
			_, keep := event.(generation.UsageUpdated)
			return !keep
		})
		err = protocolError()
	}
	for _, event := range events {
		s.queue = append(s.queue, SessionEvent{Lane: name, ResponseID: lane.id, Event: event})
	}
	if frame.Type == "response.failed" && err != nil {
		var failure *generation.Failure
		if !errors.As(err, &failure) || failure.Kind == generation.ProtocolError {
			return err
		}
		s.queue = append(s.queue, SessionEvent{Lane: name, ResponseID: lane.id, Failure: &SessionFailure{Cause: err, Usage: lane.state.usage.Usage}})
		err = io.EOF
	}
	if errors.Is(err, io.EOF) {
		s.size -= lane.bytes() - int64(len(lane.id))
		lane.state = nil
		return nil
	}
	return err
}

func (lane *sessionLane) bytes() int64 {
	size := int64(len(lane.id))
	if lane.state != nil {
		size += lane.state.size
	}
	return size
}

func (s *Session) readFailure(err error) error {
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if errors.Is(err, websocket.ErrMessageTooBig) {
		return protocolError()
	}
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
		for _, lane := range s.lanes {
			if lane.state != nil {
				return io.ErrUnexpectedEOF
			}
		}
		return io.EOF
	}
	return transportFailure(err)
}

func (s *Session) Close() error {
	s.stopped.Store(true)
	s.closeOnce.Do(func() {
		s.cancel()
		if err := s.conn.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.closeErr = errors.New("openai: session cleanup failed")
		}
		s.diagnostics.finish()
	})
	return s.closeErr
}

func (s *Session) finish(err error) {
	s.terminal = err
	_ = s.Close()
}
