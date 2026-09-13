package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/deyna256/clan/internal/retry"
	"github.com/deyna256/clan/internal/usage"
	"github.com/tmaxmax/go-sse"
)

// Stream reads ordered Responses events. Next has one reader; Close can interrupt
// a blocked Next and is repeatable. Result returns an independent snapshot.
// Construct streams through Client.Stream, and never copy a Stream.
type Stream struct {
	ctx        context.Context
	cancel     context.CancelFunc
	body       *responseReader
	status     int
	readMu     sync.Mutex
	next       func() (sse.Event, error, bool)
	stop       func()
	closeOnce  sync.Once
	closeErr   error
	ended      bool
	err        error
	result     Result
	items      map[int]json.RawMessage
	itemBytes  int
	retryAfter retry.Cooldown
}

type responseReader struct {
	io.ReadCloser
	err error
}

func (r *responseReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil {
		r.err = err
	}
	return n, err
}

func newStream(ctx context.Context, cancel context.CancelFunc, response *http.Response) *Stream {
	body := &responseReader{ReadCloser: response.Body}
	next, stop := iter.Pull2(sse.Read(body, &sse.ReadConfig{MaxEventSize: maxPayload}))
	cooldown, _ := retry.ParseRetryAfter(response.Header.Get("Retry-After"), time.Now())
	return &Stream{ctx: ctx, cancel: cancel, body: body, status: response.StatusCode,
		next: next, stop: stop, items: make(map[int]json.RawMessage), retryAfter: cooldown}
}

// Next returns one raw JSON event or an end/error, never both. The terminal event
// contains the assembled final response. A failed terminal is followed by its
// Failure on the next read; incomplete is a terminal result, not a failed attempt.
func (s *Stream) Next() (json.RawMessage, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.ended {
		if s.err != nil {
			return nil, s.err
		}
		return nil, io.EOF
	}
	for {
		event, err, ok := s.next()
		if err != nil || !ok {
			category := InvalidResponse
			if s.ctx.Err() != nil || (s.body.err != nil && !errors.Is(s.body.err, io.EOF)) {
				category = TransportFailure
			}
			s.finish(safeFailure(s.ctx, category, s.status, s.body.err))
			return nil, s.err
		}
		if event.Data == "" {
			continue
		}
		raw := json.RawMessage(event.Data)
		fields, err := object(raw, "event", "")
		if err != nil {
			return s.badEvent()
		}
		kind, err := textValue(fields["type"], "event.type", false)
		if err != nil || (event.Type != "" && event.Type != kind) {
			return s.badEvent()
		}
		if response, ok := fields["response"]; ok {
			values, e := object(response, "response", "")
			if e != nil {
				return s.badEvent()
			}
			if e = s.observeUsage(values["usage"]); e != nil {
				return s.badEvent()
			}
		}
		switch kind {
		case "response.output_item.done":
			var index int
			if len(fields["output_index"]) == 0 || bytes.Equal(fields["output_index"], []byte("null")) || json.Unmarshal(fields["output_index"], &index) != nil || index < 0 {
				return s.badEvent()
			}
			item, err := object(fields["item"], "item", "")
			if err != nil {
				return s.badEvent()
			}
			if _, err = textValue(item["type"], "item.type", false); err != nil {
				return s.badEvent()
			}
			s.itemBytes += len(fields["item"]) - len(s.items[index])
			if s.itemBytes > maxPayload {
				return s.badEvent()
			}
			s.items[index] = fields["item"]
		case "response.completed", "response.incomplete", "response.failed":
			assembled, failure, err := s.terminal(fields, kind)
			if err != nil {
				return s.badEvent()
			}
			s.finish(failure)
			return assembled, nil
		case "error":
			detail := raw
			if nested, ok := fields["error"]; ok {
				detail = nested
			}
			failure := responseFailure(detail, s.status)
			code, _ := json.Marshal(string(failure.Category))
			message, _ := json.Marshal(failure.Error())
			clean := map[string]json.RawMessage{"type": fields["type"], "code": code, "message": message, "param": json.RawMessage("null")}
			if sequence, ok := fields["sequence_number"]; ok {
				clean["sequence_number"] = sequence
			}
			raw, _ = json.Marshal(clean)
			s.finish(failure)
			return raw, nil
		}
		return raw, nil
	}
}

// Result returns the terminal response, when available, and usage seen so far.
// It waits for an overlapping Next to finish; Close can interrupt that read.
func (s *Stream) Result() Result {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	return Result{Response: slices.Clone(s.result.Response), Usage: s.result.Usage}
}

// Close interrupts upstream I/O and waits for the reader/iterator to release its resources.
func (s *Stream) Close() error {
	s.closeBody()
	s.readMu.Lock()
	defer s.readMu.Unlock()
	s.stop()
	s.items = nil
	if !s.ended {
		s.ended = true
		s.err = safeFailure(s.ctx, TransportFailure, s.status, nil)
	}
	return s.closeErr
}

func (s *Stream) closeBody() {
	s.closeOnce.Do(func() {
		s.cancel()
		if s.body.Close() != nil {
			s.closeErr = &Failure{Category: TransportFailure, HTTPStatus: s.status}
		}
	})
}

func (s *Stream) finish(err error) {
	s.ended = true
	var failure *Failure
	if errors.As(err, &failure) {
		failure.RetryAfter = s.retryAfter
	}
	s.err = err
	s.closeBody()
	s.err = errors.Join(s.err, s.closeErr)
	s.stop()
	s.items = nil
}

func (s *Stream) badEvent() (json.RawMessage, error) {
	s.finish(&Failure{Category: InvalidResponse, HTTPStatus: s.status})
	return nil, s.err
}

func (s *Stream) terminal(fields map[string]json.RawMessage, kind string) (json.RawMessage, error, error) {
	response, err := object(fields["response"], "response", "")
	if err != nil {
		return nil, nil, err
	}
	if _, err = textValue(response["id"], "response.id", false); err != nil {
		return nil, nil, err
	}
	status := kind[len("response."):]
	if raw, ok := response["status"]; ok {
		value, e := textValue(raw, "response.status", false)
		if e != nil || value != status {
			return nil, nil, invalid("response.status")
		}
	} else {
		response["status"], _ = json.Marshal(status)
	}
	var output []json.RawMessage
	if raw, ok := response["output"]; ok {
		output, err = array(raw, "response.output")
		if err != nil {
			return nil, nil, err
		}
		for _, item := range output {
			values, err := object(item, "response.output", "")
			if err != nil {
				return nil, nil, err
			}
			if _, err = textValue(values["type"], "response.output.type", false); err != nil {
				return nil, nil, err
			}
		}
	}
	if len(output) == 0 {
		indexes := make([]int, 0, len(s.items))
		for index := range s.items {
			indexes = append(indexes, index)
		}
		slices.Sort(indexes)
		output = make([]json.RawMessage, 0, len(indexes))
		for _, index := range indexes {
			output = append(output, s.items[index])
		}
		response["output"], _ = json.Marshal(output)
	}
	var failure error
	if status == "failed" {
		classified := responseFailure(response["error"], s.status)
		response["error"] = safeErrorJSON(classified)
		failure = classified
	}
	s.result.Response, err = json.Marshal(response)
	if err != nil {
		return nil, nil, err
	}
	fields["response"] = s.result.Response
	raw, err := json.Marshal(fields)
	return raw, failure, err
}

func safeErrorJSON(failure *Failure) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"code": string(failure.Category), "message": failure.Error()})
	return raw
}

func (s *Stream) observeUsage(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	fields, err := object(raw, "usage", "")
	if err != nil {
		return err
	}
	var malformed error
	for _, count := range []struct {
		name   string
		target *usage.Counter
	}{
		{name: "input_tokens", target: &s.result.Usage.Input},
		{name: "output_tokens", target: &s.result.Usage.Output},
		{name: "total_tokens", target: &s.result.Usage.Total},
	} {
		value, ok := fields[count.name]
		if !ok || string(value) == "null" {
			continue
		}
		var tokens int64
		if json.Unmarshal(value, &tokens) != nil || tokens < 0 {
			malformed = invalid("usage")
			continue
		}
		*count.target = usage.Counter{Tokens: tokens, Known: true}
	}
	return malformed
}
