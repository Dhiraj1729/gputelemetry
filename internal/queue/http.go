package queue

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"gpu-telemetry/internal/telemetry"
)

// Store isolates protocol handlers from storage. Day 2 will introduce lease/ACK
// operations separately, rather than silently changing destructive receive.
type Store interface {
	Publish(telemetry.Event) (bool, error)
	Receive() (telemetry.Event, bool)
	Stats() Stats
}

func Handler(q Store, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /internal/v1/stats", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, q.Stats()) })
	mux.HandleFunc("POST /internal/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		var req PublishRequest
		if err := d.Decode(&req); err != nil {
			var size *http.MaxBytesError
			if errors.As(err, &size) {
				failure(w, 413, "too_large", "request exceeds 64 KiB")
				return
			}
			failure(w, 400, "invalid_request", "invalid JSON: "+err.Error())
			return
		}
		if err := d.Decode(new(any)); err != io.EOF {
			failure(w, 400, "invalid_request", "exactly one JSON document required")
			return
		}
		if len(req.Events) != 1 {
			failure(w, 400, "invalid_request", "Day 1 requires exactly one event per batch")
			return
		}
		if err := req.Events[0].Validate(); err != nil {
			failure(w, 400, "invalid_event", err.Error())
			return
		}
		duplicate, err := q.Publish(req.Events[0])
		switch {
		case errors.Is(err, ErrFull), errors.Is(err, ErrDedupFull):
			w.Header().Set("Retry-After", "1")
			failure(w, 429, "capacity", err.Error())
			return
		case errors.Is(err, ErrConflict):
			failure(w, 409, "id_conflict", err.Error())
			return
		case err != nil:
			failure(w, 400, "invalid_event", err.Error())
			return
		}
		logger.Debug("message accepted", "event_id", req.Events[0].EventID, "duplicate", duplicate)
		respond(w, 202, PublishResponse{[]string{req.Events[0].EventID}, duplicate})
	})
	mux.HandleFunc("POST /internal/v1/receive", func(w http.ResponseWriter, r *http.Request) {
		e, ok := q.Receive()
		if !ok {
			w.WriteHeader(204)
			return
		}
		respond(w, 200, ReceiveResponse{[]telemetry.Event{e}})
	})
	// Bound simultaneously active handlers; overload is explicitly retryable.
	slots := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			mux.ServeHTTP(w, r)
		default:
			failure(w, 503, "busy", "too many active requests")
		}
	})
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, code, message string) {
	respond(w, status, ErrorResponse{code, message})
}
