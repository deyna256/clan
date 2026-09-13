package execution

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/deyna256/clan/internal/codex"
)

// Stream holds one key slot through delivery and cleanup. Next has one reader;
// Close may overlap Next, is repeatable and interrupts upstream I/O.
type Stream struct {
	attempt        *codex.Stream
	owner          *Executor
	request        *requestState
	stop           func() bool
	once           sync.Once
	failureOnce    sync.Once
	closeErr       error
	deliveryFailed atomic.Bool
}

// Next forwards an event without retrying an already opened stream.
func (s *Stream) Next() (json.RawMessage, error) {
	return s.next()
}

func (s *Stream) next() (json.RawMessage, error) {
	if err := s.request.ctx.Err(); err != nil {
		return nil, err
	}
	event, err := s.attempt.Next()
	s.noteFailure(s.attempt.Err())
	return event, err
}

// Result returns the provider result and known usage, including after failure.
func (s *Stream) Result() codex.Result { return s.attempt.Result() }

// Context is canceled when execution stops this request, including key revocation.
func (s *Stream) Context() context.Context { return s.request.ctx }

// DeliveryFailed records a downstream encoding or write failure without its error text.
func (s *Stream) DeliveryFailed() { s.deliveryFailed.Store(true) }

// ResponseStarted records HTTP response commitment before body delivery.
func (s *Stream) ResponseStarted() { s.request.responseStarted.Store(true) }

// Close acknowledges delivery or abort completion and releases the key slot.
// Call it even after Next returns EOF or an error.
func (s *Stream) Close() error {
	s.stop()
	s.finish()
	return s.closeErr
}

func (s *Stream) finish() {
	s.once.Do(func() {
		s.closeErr = s.attempt.Close()
		err := s.attempt.Err()
		s.noteFailure(err)
		if s.deliveryFailed.Load() {
			err = ErrDelivery
		}
		if canceled := s.request.ctx.Err(); canceled != nil {
			err = canceled
		}
		s.owner.finish(s.request, s.attempt.Result(), err)
	})
}

func (s *Stream) abort() { s.attempt.Close() }

func (s *Stream) noteFailure(err error) {
	if err != nil {
		s.failureOnce.Do(func() { s.owner.noteFailure(s.request, s.request.accountID, err) })
	}
}
