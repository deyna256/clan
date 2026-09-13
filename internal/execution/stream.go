package execution

import (
	"encoding/json"
	"sync"

	"github.com/deyna256/clan/internal/codex"
)

// Stream holds one key slot through delivery and cleanup. Next has one reader;
// Close may overlap Next, is repeatable and interrupts upstream I/O.
type Stream struct {
	attempt     *codex.Stream
	owner       *Executor
	request     *requestState
	stop        func() bool
	once        sync.Once
	failureOnce sync.Once
	closeErr    error
}

// Next forwards an event without retrying an already opened stream.
func (s *Stream) Next() (json.RawMessage, error) {
	event, err := s.next()
	if err != nil {
		s.Close()
	}
	return event, err
}

func (s *Stream) next() (json.RawMessage, error) {
	event, err := s.attempt.Next()
	s.noteFailure(s.attempt.Err())
	return event, err
}

// Result returns the provider result and known usage, including after failure.
func (s *Stream) Result() codex.Result { return s.attempt.Result() }

// Close waits for attempt cleanup before releasing the key slot.
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
		s.owner.finish(s.request, s.attempt.Result(), err)
	})
}

func (s *Stream) noteFailure(err error) {
	if err != nil {
		s.failureOnce.Do(func() { s.owner.noteFailure(s.request, s.request.accountID, err) })
	}
}
