package collector

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

type readinessFunc func(context.Context) error

func (f readinessFunc) Ready(c context.Context) error { return f(c) }
func TestHealthAndReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	down := false
	h := HealthHandler(ctx, readinessFunc(func(c context.Context) error {
		if _, ok := c.Deadline(); !ok {
			t.Fatal("unbounded health check")
		}
		if down {
			return errors.New("offline")
		}
		return nil
	}))
	check := func(path string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatal(path, w.Code)
		}
	}
	check("/healthz", 200)
	check("/readyz", 200)
	down = true
	check("/healthz", 200)
	check("/readyz", 503)
	down = false
	cancel()
	check("/readyz", 503)
}
