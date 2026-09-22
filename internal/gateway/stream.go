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
	stream, openErr := h.executor.Stream(r.Context(), bearer(r), requestInfo(r), input)
	if stream == nil {
		h.reject(w, r, classify(openErr))
		return
	}
	defer stream.Close()
	d := newDelivery(w, stream.Context())
	defer d.close()
	started := false
	sequence := int64(-1)
	sendError := func(err error) {
		if !started {
			if !writeError(d, r, classify(err), stream.ResponseStarted) {
				stream.DeliveryFailed()
			}
		} else if !errors.Is(err, io.EOF) {
			if sequence == math.MaxInt64 || !sendStreamError(d, classify(err), sequence+1) {
				stream.DeliveryFailed()
			}
		}
	}
	if openErr != nil {
		sendError(openErr)
		return
	}
	for {
		raw, nextErr := stream.Next()
		if nextErr != nil {
			sendError(nextErr)
			return
		}
		event, kind, nextSequence, err := prepareEvent(raw, sequence)
		sequence = nextSequence
		if err != nil {
			stream.DeliveryFailed()
			sendError(err)
			return
		}
		var commit func()
		if !started {
			commit = stream.ResponseStarted
		}
		if !d.event(kind, event, commit) {
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
