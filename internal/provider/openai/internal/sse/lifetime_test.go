package sse

import (
	"errors"
	"io"
	"iter"
	"testing"

	gosse "github.com/tmaxmax/go-sse"
)

// A producer with observable cleanup verifies that Stop releases the iterator,
// not merely that later Next calls return EOF.
func TestStopWaitsForIteratorCleanup(t *testing.T) {
	released := make(chan struct{})
	next, stop := iter.Pull2(func(yield func(gosse.Event, error) bool) {
		defer close(released)
		yield(gosse.Event{Data: "ready"}, nil)
	})
	decoder := &Decoder{next: next, stop: stop}
	t.Cleanup(decoder.Stop)
	if _, err := decoder.Next(); err != nil {
		t.Fatal(err)
	}

	decoder.Stop()

	select {
	case <-released:
	default:
		t.Fatal("Stop returned before iterator cleanup")
	}
}

func TestStopMayOverlapCancelledNext(t *testing.T) {
	reading := make(chan struct{})
	cancelled := make(chan struct{})
	released := make(chan struct{})
	next, stop := iter.Pull2(func(yield func(gosse.Event, error) bool) {
		defer close(released)
		close(reading)
		<-cancelled
		yield(gosse.Event{}, io.ErrClosedPipe)
	})
	decoder := &Decoder{next: next, stop: stop}
	finished := make(chan error, 1)
	go func() {
		_, err := decoder.Next()
		finished <- err
	}()
	<-reading

	close(cancelled)
	decoder.Stop()

	if err := <-finished; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Next = %v; want closed pipe", err)
	}
	select {
	case <-released:
	default:
		t.Fatal("Stop returned before cancelled iterator cleanup")
	}
}
