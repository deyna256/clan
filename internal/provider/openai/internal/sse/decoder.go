// Package sse adapts bounded server-sent events without reconnecting.
package sse

import (
	"bufio"
	"errors"
	"io"
	"iter"
	"sync"

	gosse "github.com/tmaxmax/go-sse"
)

// ErrTooLarge means an encoded event exceeds the configured byte limit.
var ErrTooLarge = bufio.ErrTooLong

// Event contains the event name and joined data lines.
type Event struct{ Type, Data string }

// Decoder must be constructed with New and has one sequential Next consumer.
// Stop may overlap Next after the caller cancels or closes its reader to unblock I/O.
type Decoder struct {
	mu   sync.Mutex
	next func() (gosse.Event, error, bool)
	stop func()
	err  error
}

// New bounds encoded events, including literal CRLF delimiters, to maxBytes.
func New(reader io.Reader, maxBytes int) (*Decoder, error) {
	if reader == nil || maxBytes <= 0 {
		return nil, errors.New("sse: reader and positive byte limit are required")
	}
	next, stop := iter.Pull2(gosse.Read(reader, &gosse.ReadConfig{MaxEventSize: maxBytes}))
	return &Decoder{next: next, stop: stop}, nil
}

// Next returns nonempty event data. A final newline-terminated event may be
// dispatched at EOF; EOF itself does not establish provider completion.
func (d *Decoder) Next() (Event, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return Event{}, d.err
	}
	for {
		event, err, ok := d.next()
		if !ok {
			err = io.EOF
		}
		if errors.Is(err, gosse.ErrUnexpectedEOF) {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			d.err = err
			d.stop()
			return Event{}, err
		}
		if event.Data == "" {
			continue
		}
		if event.Type == "" {
			event.Type = "message"
		}
		return Event{Type: event.Type, Data: event.Data}, nil
	}
}

// Stop releases an abandoned iterator and waits for an active Next to finish.
// Cancel or close the reader before calling Stop. Repeated calls are safe.
func (d *Decoder) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stop()
	if d.err == nil {
		d.err = io.EOF
	}
}
