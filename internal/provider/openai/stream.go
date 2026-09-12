package openai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/deyna256/clan/internal/generation"
	"github.com/deyna256/clan/internal/provider/openai/internal/sse"
	"github.com/deyna256/clan/internal/provider/openai/internal/wire"
	"github.com/tidwall/sjson"
)

// Stream has one sequential Next consumer. Close may overlap a blocked Next;
// it cancels I/O, is idempotent and never drains the provider body.
type Stream struct {
	ctx       context.Context
	cancel    context.CancelFunc
	body      io.ReadCloser
	decoder   *sse.Decoder
	state     streamState
	queue     []generation.Event
	terminal  error
	closeOnce sync.Once
	closeErr  error
	sequence  *int64
	lastFrame [sha256.Size]byte
}

// GenerateStream transfers stream ownership to the caller. The supplied context
// controls opening and every later read. Opening does not imply successful output.
func (c *Client) GenerateStream(ctx context.Context, attempt Attempt, request generation.Request) (*Stream, error) {
	payload, err := wire.EncodeRequest(request, true)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	response, err := c.transport.Create(streamCtx, attempt.Credentials.Key, payload, true)
	return c.openStream(streamCtx, cancel, attempt, response, err)
}

func (c *Client) openStream(streamCtx context.Context, cancel context.CancelFunc, attempt Attempt, response *http.Response, err error) (*Stream, error) {
	if err != nil {
		cancel()
		return nil, transportFailure(err)
	}
	diagnostics := c.diagnostics(attempt)
	if response.StatusCode != http.StatusOK {
		defer cancel()
		defer response.Body.Close()
		defer diagnostics.finish()
		body, readErr := readBounded(response.Body, c.config.MaxResponseBytes)
		result, err := decodeHTTPFailure(response, body, diagnostics)
		if readErr != nil {
			err = transportFailure(readErr)
		}
		return nil, &StartupError{Result: result, Cause: err}
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "text/event-stream" {
		cancel()
		_ = response.Body.Close()
		return nil, protocolError()
	}
	decoder, err := sse.New(response.Body, c.config.MaxEventBytes)
	if err != nil {
		cancel()
		_ = response.Body.Close()
		return nil, err
	}
	return &Stream{
		ctx: streamCtx, cancel: cancel, body: response.Body, decoder: decoder,
		state: streamState{items: make(map[int]*streamItem), diagnostics: diagnostics, limit: c.config.MaxResponseBytes},
	}, nil
}

// StartupError retains usage reported by a rejected streaming request.
type StartupError struct {
	Result generation.Result
	Cause  error
}

func (e *StartupError) Error() string { return e.Cause.Error() }
func (e *StartupError) Unwrap() error { return e.Cause }

func (s *Stream) Next() (generation.Event, error) {
	for {
		if s.terminal != nil && len(s.queue) == 0 {
			return nil, s.terminal
		}
		if err := s.ctx.Err(); s.terminal == nil && err != nil {
			s.queue = slices.DeleteFunc(s.queue, func(event generation.Event) bool {
				_, usage := event.(generation.UsageUpdated)
				return !usage
			})
			s.finish(err)
			continue
		}
		if len(s.queue) != 0 {
			event := s.queue[0]
			s.queue[0] = nil
			s.queue = s.queue[1:]
			return event, nil
		}
		frame, err := s.decoder.Next()
		if err != nil {
			if s.ctx.Err() != nil {
				err = s.ctx.Err()
			} else if errors.Is(err, sse.ErrTooLarge) {
				err = protocolError()
			} else if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				err = io.ErrUnexpectedEOF
			} else {
				err = transportFailure(err)
			}
			s.finish(err)
			continue
		}
		var event responseEvent
		if decodeStreamFrame([]byte(frame.Data), &event) != nil {
			s.state.diagnostics.warn("invalid_event", frame.Type, "skipped")
			continue
		}
		if event.Type != "response.shell_call_output_content.delta" && len(event.RawDelta) != 0 && json.Unmarshal(event.RawDelta, &event.Delta) != nil {
			s.state.diagnostics.warn("invalid_event", frame.Type, "skipped")
			continue
		}
		if event.Sequence != nil {
			fingerprint := sha256.Sum256([]byte(frame.Data))
			if *event.Sequence < 0 || (s.sequence != nil && *event.Sequence <= *s.sequence) {
				if s.sequence != nil && *event.Sequence == *s.sequence && fingerprint == s.lastFrame {
					continue
				}
				s.finish(protocolError())
				continue
			}
			s.sequence, s.lastFrame = event.Sequence, fingerprint
		}
		event.raw = frame.Data
		s.queue, err = s.state.consume(event)
		if err != nil {
			s.finish(err)
		}
	}
}

func (s *Stream) Close() error {
	err := s.closeBody()
	s.state.diagnostics.finish()
	return err
}

func (s *Stream) closeBody() error {
	s.closeOnce.Do(func() {
		s.cancel()
		if s.body.Close() != nil {
			s.closeErr = errors.New("openai: stream cleanup failed")
		}
		s.decoder.Stop()
	})
	return s.closeErr
}

func (s *Stream) finish(err error) {
	s.terminal = err
	_ = s.closeBody()
	s.state.diagnostics.finish()
}

func protocolError() error { return &generation.Failure{Kind: generation.ProtocolError} }

func decodeStreamFrame(data []byte, frame any) error {
	if !utf8.Valid(data) {
		// The response decoder retains usage before rejecting invalid content.
		header, err := sjson.DeleteBytes(data, "response")
		if err != nil || !utf8.Valid(header) {
			return protocolError()
		}
	}
	if json.Unmarshal(data, frame) != nil {
		return protocolError()
	}
	return nil
}

type responseEvent struct {
	raw               string
	Type              string          `json:"type"`
	Sequence          *int64          `json:"sequence_number"`
	OutputIndex       *int            `json:"output_index"`
	ContentIndex      *int            `json:"content_index"`
	SummaryIndex      *int            `json:"summary_index"`
	ItemID            string          `json:"item_id"`
	Item              json.RawMessage `json:"item"`
	Part              json.RawMessage `json:"part"`
	Response          json.RawMessage `json:"response"`
	Delta             *string         `json:"-"`
	RawDelta          json.RawMessage `json:"delta"`
	CommandIndex      *int            `json:"command_index"`
	Command           *string         `json:"command"`
	Output            json.RawMessage `json:"output"`
	Text              *string         `json:"text"`
	Refusal           *string         `json:"refusal"`
	Arguments         *string         `json:"arguments"`
	Input             *string         `json:"input"`
	Code              *string         `json:"code"`
	AnnotationIndex   *int            `json:"annotation_index"`
	Annotation        json.RawMessage `json:"annotation"`
	Logprobs          json.RawMessage `json:"logprobs"`
	PartialImage      *string         `json:"partial_image_b64"`
	PartialImageIndex *int            `json:"partial_image_index"`
}
