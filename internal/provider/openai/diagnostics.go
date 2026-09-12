package openai

import (
	"log/slog"
	"sync"
)

type diagnostics struct {
	mu     sync.Mutex
	logger *slog.Logger
	counts map[string]int
	done   bool
}

func (c *Client) diagnostics(attempt Attempt) *diagnostics {
	return &diagnostics{
		logger: c.logger.With("request_id", attempt.RequestID, "attempt_id", attempt.ID),
		counts: make(map[string]int),
	}
}

func (d *diagnostics) report(reason string) {
	d.warn(reason, "response", "kept_valid_metadata")
}

func (d *diagnostics) warn(reason, eventType, action string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return
	}
	d.counts[reason]++
	if d.counts[reason] != 1 {
		return
	}
	if reason == "unknown_event" || reason == "invalid_event" {
		eventType = "unrecognized"
	}
	d.logger.Warn("provider protocol warning", "reason", reason, "event_type", eventType, "action", action)
}

func (d *diagnostics) finish() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return
	}
	d.done = true
	for reason, count := range d.counts {
		if count > 1 {
			d.logger.Warn("provider protocol warning repeated", "reason", reason, "count", count)
		}
	}
}
