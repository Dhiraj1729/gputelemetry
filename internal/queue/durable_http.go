package queue

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"gpu-telemetry/internal/telemetry"
)

type ReliableStore interface {
	PublishContext(context.Context, telemetry.Event) (bool, error)
	Lease(context.Context, time.Duration) (Delivery, bool, error)
	Ack(context.Context, AckRequest) (bool, error)
	Nack(context.Context, NackRequest) (bool, error)
	Snapshot(context.Context) (DurableStats, error)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err := d.Decode(v)
	if err == nil {
		err = d.Decode(new(any))
		if err == io.EOF {
			return true
		}
		if err == nil {
			err = errors.New("exactly one JSON document required")
		}
	}
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		failure(w, 413, "too_large", "request exceeds 64 KiB")
	} else {
		failure(w, 400, "invalid_request", err.Error())
	}
	return false
}
func durableError(w http.ResponseWriter, err error, logger *slog.Logger) {
	switch {
	case errors.Is(err, ErrInvalid):
		failure(w, 400, "invalid_request", err.Error())
	case errors.Is(err, ErrConflict):
		failure(w, 409, "id_conflict", err.Error())
	case errors.Is(err, ErrStaleLease):
		failure(w, 409, "stale_lease", err.Error())
	case errors.Is(err, ErrFull), errors.Is(err, ErrDedupFull):
		w.Header().Set("Retry-After", "1")
		failure(w, 429, "capacity", err.Error())
	case errors.Is(err, ErrClosed), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		failure(w, 503, "unavailable", "broker stopping or request canceled")
	default:
		logger.Error("storage operation failed", "error", err)
		failure(w, 503, "storage_error", "durable operation not confirmed; retry safely")
	}
}
func DurableHandler(q ReliableStore, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := q.Snapshot(r.Context()); err != nil {
			durableError(w, err, logger)
			return
		}
		respond(w, 200, map[string]string{"status": "ok", "durability": "disk"})
	})
	mux.HandleFunc("GET /internal/v1/stats", func(w http.ResponseWriter, r *http.Request) {
		s, err := q.Snapshot(r.Context())
		if err != nil {
			durableError(w, err, logger)
			return
		}
		respond(w, 200, s)
	})
	mux.HandleFunc("POST /internal/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var req PublishRequest
		if !decode(w, r, &req) {
			return
		}
		if len(req.Events) != 1 {
			failure(w, 400, "invalid_request", "exactly one event per batch required")
			return
		}
		dup, err := q.PublishContext(r.Context(), req.Events[0])
		if err != nil {
			durableError(w, err, logger)
			return
		}
		respond(w, 202, PublishResponse{[]string{req.Events[0].EventID}, dup})
	})
	mux.HandleFunc("POST /internal/v1/receive", func(w http.ResponseWriter, r *http.Request) {
		failure(w, 410, "disabled", "destructive receive is disabled in durable mode; use leases and ACK")
	})
	// Reserve handler capacity for ACK/publish even with many idle long polls.
	polls := make(chan struct{}, 32)
	mux.HandleFunc("POST /internal/v1/leases", func(w http.ResponseWriter, r *http.Request) {
		select {
		case polls <- struct{}{}:
			defer func() { <-polls }()
		default:
			failure(w, 503, "busy", "long poll capacity exhausted")
			return
		}
		var req LeaseRequest
		if !decode(w, r, &req) {
			return
		}
		if req.WaitMillis < 0 || req.WaitMillis > MaxWait.Milliseconds() {
			failure(w, 400, "invalid_request", "wait_ms must be 0..10000")
			return
		}
		e, ok, err := q.Lease(r.Context(), time.Duration(req.WaitMillis)*time.Millisecond)
		if err != nil {
			durableError(w, err, logger)
			return
		}
		if !ok {
			w.WriteHeader(204)
			return
		}
		respond(w, 200, e)
	})
	mux.HandleFunc("POST /internal/v1/acks", func(w http.ResponseWriter, r *http.Request) {
		var req AckRequest
		if !decode(w, r, &req) {
			return
		}
		dup, err := q.Ack(r.Context(), req)
		if err != nil {
			durableError(w, err, logger)
			return
		}
		respond(w, 200, ReceiptResponse{req.EventID, dup})
	})
	mux.HandleFunc("POST /internal/v1/nacks", func(w http.ResponseWriter, r *http.Request) {
		var req NackRequest
		if !decode(w, r, &req) {
			return
		}
		dup, err := q.Nack(r.Context(), req)
		if err != nil {
			durableError(w, err, logger)
			return
		}
		respond(w, 200, ReceiptResponse{req.EventID, dup})
	})
	slots := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			mux.ServeHTTP(w, r)
		default:
			failure(w, 503, "busy", "handler capacity exhausted")
		}
	})
}
