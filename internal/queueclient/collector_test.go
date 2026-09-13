package queueclient

import (
	"context"
	"encoding/json"
	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/telemetry"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCollectorCanQuarantineSemanticInvalidEvent(t *testing.T) {
	d := queue.Delivery{Event: telemetry.Event{SchemaVersion: 99, EventID: "invalid"}, Token: "01234567890123456789012345678901", ExpiresAt: time.Now().Add(time.Second), Attempt: 1}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(d) }))
	defer srv.Close()
	c, _ := New(srv.URL, time.Second)
	defer c.Close()
	if _, ok, err := c.Lease(context.Background(), 0); ok || err == nil {
		t.Fatal("legacy validation lost")
	}
	if got, ok, err := c.LeaseForProcessing(context.Background(), 0); err != nil || !ok || got.Event.EventID != "invalid" {
		t.Fatal(got, ok, err)
	}
	d.Event.EventID = ""
	if _, ok, err := c.LeaseForProcessing(context.Background(), 0); ok || err == nil {
		t.Fatal("unsafe identity accepted")
	}
}
