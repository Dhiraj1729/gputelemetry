package queueclient

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/telemetry"
)

func TestLeaseAndLostACKResponse(t *testing.T) {
	o := queue.DefaultDurableOptions(filepath.Join(t.TempDir(), "queue.db"))
	d, err := queue.OpenDurable(o)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := queue.DurableHandler(d, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	var drop atomic.Bool
	drop.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/acks" && drop.Swap(false) {
			var req queue.AckRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			if _, err := d.Ack(r.Context(), req); err != nil {
				t.Error(err)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	c, _ := New(server.URL, time.Second)
	defer c.Close()
	e := telemetry.Event{SchemaVersion: 1, EventID: "id", ProducerInstanceID: "p", RowIndex: 1, ProcessedAt: time.Now().UTC().Truncate(time.Microsecond), Measurement: telemetry.Measurement{GPUUUID: "GPU-1", MetricName: "temperature", SourceTimestamp: time.Now(), Value: 42}}
	if err = c.Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	delivery, ok, err := c.Lease(context.Background(), 0)
	if !ok || err != nil {
		t.Fatal(delivery, ok, err)
	}
	req := queue.AckRequest{EventID: e.EventID, Token: delivery.Token}
	if err = c.Ack(context.Background(), req); !Retryable(err) {
		t.Fatal("expected lost ACK response", err)
	}
	if err = c.Ack(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = c.Lease(context.Background(), 10*time.Millisecond); ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, _, err = c.Lease(context.Background(), 2*time.Second); err == nil {
		t.Fatal("wait exceeds client timeout")
	}
	if _, _, err = c.Receive(context.Background()); err == nil {
		t.Fatal("destructive receive allowed")
	}
	e.EventID = "nack"
	if err = c.Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	delivery, _, _ = c.Lease(context.Background(), 0)
	if err = c.Nack(context.Background(), queue.NackRequest{EventID: e.EventID, Token: delivery.Token, Permanent: true, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
}
