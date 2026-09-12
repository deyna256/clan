// Package sse reads bounded server-sent events without reconnecting.
package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// ErrTooLarge means an event exceeds the configured byte limit.
var ErrTooLarge = errors.New("sse: event exceeds byte limit")

// Event contains the event name and joined data lines.
type Event struct {
	Type string
	Data string
}

// Decoder must be created with New and read sequentially.
// The caller owns cancellation and reader cleanup.
// EOF describes the transport, not successful provider completion.
type Decoder struct {
	reader *bufio.Reader
	limit  int
	first  bool
	skipLF bool
	err    error
}

// New bounds each frame to maxBytes after treating CRLF as one delimiter.
func New(reader io.Reader, maxBytes int) (*Decoder, error) {
	if reader == nil || maxBytes <= 0 {
		return nil, errors.New("sse: reader and positive byte limit are required")
	}
	return &Decoder{reader: bufio.NewReader(reader), limit: maxBytes, first: true}, nil
}

// Next returns one event. Unterminated frames at EOF are discarded.
// After any error, later calls return that error without reading again.
func (d *Decoder) Next() (Event, error) {
	if d.err != nil {
		return Event{}, d.err
	}
	event := Event{Type: "message"}
	var data strings.Builder
	remaining := d.limit
	for {
		line, err := d.line(&remaining)
		if err != nil {
			d.err = err
			return Event{}, err
		}
		if d.first {
			line = strings.TrimPrefix(line, "\ufeff")
			d.first = false
		}
		if line == "" {
			if data.Len() != 0 {
				event.Data = strings.TrimSuffix(data.String(), "\n")
				return event, nil
			}
			event.Type = "message"
			remaining = d.limit
			continue
		}
		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "event":
			event.Type = value
			if value == "" {
				event.Type = "message"
			}
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
}

func (d *Decoder) line(remaining *int) (string, error) {
	var line strings.Builder
	for {
		b, err := d.reader.ReadByte()
		if err != nil {
			return "", err
		}
		if d.skipLF {
			d.skipLF = false
			if b == '\n' {
				continue
			}
		}
		if *remaining == 0 {
			return "", ErrTooLarge
		}
		*remaining -= 1
		switch b {
		case '\r':
			d.skipLF = true
			return line.String(), nil
		case '\n':
			return line.String(), nil
		default:
			line.WriteByte(b)
		}
	}
}
