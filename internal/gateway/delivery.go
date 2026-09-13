package gateway

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/tmaxmax/go-sse"
)

type delivery struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	ctx        context.Context
	mu         sync.Mutex
	stop       func() bool
	done       chan struct{}
}

func newDelivery(w http.ResponseWriter, ctx context.Context) *delivery {
	d := &delivery{w: w, controller: http.NewResponseController(w), ctx: ctx, done: make(chan struct{})}
	d.stop = context.AfterFunc(ctx, func() {
		d.mu.Lock()
		// The regular write deadline remains the fallback if aborting fails.
		_ = d.controller.SetWriteDeadline(time.Now())
		d.mu.Unlock()
		close(d.done)
	})
	return d
}

func (d *delivery) close() {
	if !d.stop() {
		<-d.done
	}
}

func (d *delivery) begin() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx.Err() != nil {
		return false
	}
	return d.controller.SetWriteDeadline(time.Now().Add(30*time.Second)) == nil
}

func (d *delivery) flush() bool {
	if d.controller.Flush() != nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx.Err() != nil {
		return false
	}
	return d.controller.SetWriteDeadline(time.Time{}) == nil
}

func (d *delivery) json(status int, body []byte, started func()) bool {
	if !d.begin() {
		return false
	}
	d.w.Header().Set("Content-Type", "application/json")
	d.w.WriteHeader(status)
	if started != nil {
		started()
	}
	if _, err := d.w.Write(body); err != nil {
		return false
	}
	return d.flush()
}

func (d *delivery) event(kind string, body []byte, started func()) bool {
	eventType, err := sse.NewType(kind)
	if err != nil || !d.begin() {
		return false
	}
	if started != nil {
		d.w.Header().Set("Content-Type", "text/event-stream")
		d.w.Header().Set("X-Accel-Buffering", "no")
		d.w.WriteHeader(http.StatusOK)
		started()
	}
	message := sse.Message{Type: eventType}
	message.AppendData(string(body))
	if _, err := message.WriteTo(d.w); err != nil {
		return false
	}
	return d.flush()
}
