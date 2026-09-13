package queueclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/telemetry"
)

func TestResponseClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		retry  bool
	}{{400, false}, {409, false}, {413, false}, {429, true}, {408, true}, {500, true}, {503, true}, {302, false}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
			defer s.Close()
			c, _ := New(s.URL, time.Second)
			defer c.Close()
			err := c.Publish(context.Background(), telemetry.Event{})
			if err == nil || Retryable(err) != tc.retry {
				t.Fatal(err)
			}
		})
	}
}
func TestTimeoutAndCancellation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer s.Close()
	c, _ := New(s.URL, 10*time.Millisecond)
	defer c.Close()
	if err := c.Publish(context.Background(), telemetry.Event{}); !Retryable(err) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Publish(ctx, telemetry.Event{}); err != context.Canceled {
		t.Fatal(err)
	}
}
func TestAcceptedIDAndNoRedirect(t *testing.T) {
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 2 {
			http.Redirect(w, r, "/another", 307)
			return
		}
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(queue.PublishResponse{AcceptedIDs: []string{"wrong"}})
	}))
	defer s.Close()
	c, _ := New(s.URL, time.Second)
	defer c.Close()
	if err := c.Publish(context.Background(), telemetry.Event{EventID: "actual"}); !Retryable(err) {
		t.Fatal(err)
	}

	if err := c.Publish(context.Background(), telemetry.Event{}); Retryable(err) || err == nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Fatal("followed redirect", count.Load())
	}
}
func TestInvalidURL(t *testing.T) {
	for _, u := range []string{"", "localhost:8081", "ftp://host", "http://host/path", "http://user:pass@host", "http://host?q=1"} {
		if c, err := New(u, time.Second); err == nil {
			c.Close()
			t.Fatal(u)
		}
	}
}

func TestReceiveResponses(t *testing.T) {
	good := telemetry.Event{SchemaVersion: 1, EventID: "id", ProducerInstanceID: "p", RowIndex: 1, ProcessedAt: time.Now().UTC().Truncate(time.Microsecond), Measurement: telemetry.Measurement{GPUUUID: "GPU-1", MetricName: "temperature", SourceTimestamp: time.Now().UTC(), Value: 42}}
	body, _ := json.Marshal(queue.ReceiveResponse{Events: []telemetry.Event{good}})
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		ok, wantError bool
	}{
		{"valid", 200, string(body), true, false}, {"empty", 204, "", false, false}, {"bad JSON", 200, "{", false, true}, {"missing event", 200, `{"events":[]}`, false, true}, {"bad event", 200, `{"events":[{}]}`, false, true}, {"service failure", 503, "unavailable", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			client, _ := New(server.URL, time.Second)
			defer client.Close()
			event, ok, err := client.Receive(context.Background())
			if ok != tc.ok || (err != nil) != tc.wantError {
				t.Fatal(event, ok, err)
			}
			if ok && event.EventID != "id" {
				t.Fatal(event)
			}
		})
	}
}
