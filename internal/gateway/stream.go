package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"

	"github.com/deyna256/clan/internal/codex"
)

func (h *handler) stream(w http.ResponseWriter, r *http.Request, input codex.Request) {
	stream, err := h.executor.Stream(r.Context(), bearer(r), requestID(r), input)
	if stream == nil {
		h.reject(w, r, classify(err))
		return
	}
	defer stream.Close()
	d := newDelivery(w, stream.Context())
	defer d.close()
	started := false
	sequence := int64(-1)
	for {
		var raw json.RawMessage
		if err == nil {
			raw, err = stream.Next()
		}
		var kind string
		if err == nil {
			raw, kind, sequence, err = prepareEvent(raw, sequence)
			if err != nil {
				stream.DeliveryFailed()
			}
		}
		if err != nil {
			if !started {
				if !writeError(d, r, classify(err), stream.ResponseStarted) {
					stream.DeliveryFailed()
				}
			} else if !errors.Is(err, io.EOF) {
				if sequence == math.MaxInt64 || !sendStreamError(d, classify(err), sequence+1) {
					stream.DeliveryFailed()
				}
			}
			return
		}
		var commit func()
		if !started {
			commit = stream.ResponseStarted
		}
		if !d.event(kind, raw, commit) {
			stream.DeliveryFailed()
			return
		}
		started = true
		switch kind {
		case "response.completed", "response.incomplete", "response.failed", "error":
			return
		}
	}
}

func sendStreamError(d *delivery, failure clientError, sequence int64) bool {
	body, _ := json.Marshal(struct {
		Type     string  `json:"type"`
		Sequence int64   `json:"sequence_number"`
		Code     string  `json:"code"`
		Message  string  `json:"message"`
		Param    *string `json:"param"`
	}{"error", sequence, failure.Code, failure.Message, failure.Param})
	return d.event("error", body, nil)
}
