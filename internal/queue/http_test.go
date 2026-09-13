package queue

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gpu-telemetry/internal/telemetry"
)

func TestHTTPContract(t *testing.T) {
	q, _ := NewMemory(1, 2, time.Minute)
	h := Handler(q, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	body, _ := json.Marshal(PublishRequest{[]telemetry.Event{event("a")}})
	send := func(method, path string, b []byte, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(b)))
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
	}
	send("POST", "/internal/v1/messages", body, 202)
	send("POST", "/internal/v1/messages", body, 202)
	other, _ := json.Marshal(PublishRequest{[]telemetry.Event{event("b")}})
	send("POST", "/internal/v1/messages", other, 429)
	send("POST", "/internal/v1/messages", []byte(`{"events":[]}`), 400)
	send("POST", "/internal/v1/messages", []byte(`{"unknown":1}`), 400)
	send("POST", "/internal/v1/messages", append(body, []byte(` {}`)...), 400)
	send("POST", "/internal/v1/messages", []byte(strings.Repeat(" ", MaxRequestBytes+1)), http.StatusRequestEntityTooLarge)
	send("GET", "/healthz", nil, 200)
	send("GET", "/internal/v1/stats", nil, 200)
	send("GET", "/internal/v1/messages", nil, 405)
	send("POST", "/internal/v1/receive", nil, 200)
	send("POST", "/internal/v1/receive", nil, 204)
}
