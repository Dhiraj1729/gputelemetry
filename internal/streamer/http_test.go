package streamer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/queueclient"
)

func TestAmbiguousHTTPAcceptance(t *testing.T) {
	q, _ := queue.NewMemory(1, 2, time.Minute)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := queue.Handler(q, logger)
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			var req queue.PublishRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			if _, err := q.Publish(req.Events[0]); err != nil {
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
	defer s.Close()
	client, err := queueclient.New(s.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	run := runner()
	run.Options.Count = 1
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stats, err := run.Run(ctx, openSource(t), client)
	if err != nil || stats.Accepted != 1 || stats.Retries != 1 || q.Stats().Depth != 1 || q.Stats().Duplicates != 1 {
		t.Fatal(stats, q.Stats(), err)
	}
}
