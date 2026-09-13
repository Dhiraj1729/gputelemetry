package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/storage/postgres"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeReader struct {
	gpus     func(context.Context, func(postgres.GPU) error) error
	data     func(context.Context, string, *time.Time, *time.Time, func(postgres.Datapoint) error) error
	readyErr error
}

func (f fakeReader) Ready(context.Context) error { return f.readyErr }
func (f fakeReader) VisitGPUs(c context.Context, emit func(postgres.GPU) error) error {
	if f.gpus != nil {
		return f.gpus(c, emit)
	}
	return nil
}
func (f fakeReader) VisitTelemetry(c context.Context, id string, start, end *time.Time, emit func(postgres.Datapoint) error) error {
	if f.data != nil {
		return f.data(c, id, start, end, emit)
	}
	return nil
}
func setup(t *testing.T, db fakeReader, edit func(*config.API)) http.Handler {
	t.Helper()
	c := config.DefaultAPI()
	c.TempDir = t.TempDir()
	if edit != nil {
		edit(&c)
	}
	h, _ := New(context.Background(), db, c, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	t.Cleanup(func() {
		files, _ := os.ReadDir(c.TempDir)
		if len(files) != 0 {
			t.Errorf("temporary response files leaked: %v", files)
		}
	})
	return h
}
func request(t *testing.T, h http.Handler, path string, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != want {
		t.Fatalf("%s: status %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w
}
func TestEmptyAndHealth(t *testing.T) {
	h := setup(t, fakeReader{}, nil)
	for _, path := range []string{"/api/v1/gpus", "/api/v1/gpus/GPU-1/telemetry"} {
		w := request(t, h, path, 200)
		if w.Body.String() != "[]" {
			t.Fatal(w.Body.String())
		}
		if w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Content-Length") != "2" {
			t.Fatal(w.Header())
		}
	}
	request(t, h, "/healthz", 200)
	request(t, h, "/readyz", 200)
	request(t, h, "/docs", 200)
	request(t, h, "/openapi.yaml", 200)
	request(t, h, "/openapi.json", 200)
}
func TestStatusesAndValidation(t *testing.T) {
	h := setup(t, fakeReader{readyErr: errors.New("down"), data: func(_ context.Context, id string, _, _ *time.Time, _ func(postgres.Datapoint) error) error {
		if id == "missing" {
			return postgres.ErrGPUNotFound
		}
		return errors.New("private DB details")
	}}, nil)
	request(t, h, "/healthz", 200)
	request(t, h, "/readyz", 503)
	request(t, h, "/api/v1/gpus/missing/telemetry", 404)
	w := request(t, h, "/api/v1/gpus/g/telemetry", 503)
	if strings.Contains(w.Body.String(), "private DB details") {
		t.Fatal("leaked error")
	}
	for _, query := range []string{"start_time=bad", "start_time=2026-01-01T1:00:00Z", "start_time=2026-01-01T00:00:00%2B24:00", "start_time=2026-01-01T00:00:00.1234567891Z", "end_time=bad", "start_time=", "end_time=", "start_time=2026-01-02T00:00:00Z&end_time=2026-01-01T00:00:00Z", "start_time=2026-01-01T00:00:00Z&start_time=2026-01-01T00:00:00Z", "limit=1", "start_time=%ZZ", "x=1"} {
		request(t, h, "/api/v1/gpus/g/telemetry?"+query, 400)
	}
	request(t, h, "/api/v1/gpus?limit=1", 400)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/gpus", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}
func TestFiltersAndOrderedRowsPreserved(t *testing.T) {
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	h := setup(t, fakeReader{data: func(ctx context.Context, id string, start, end *time.Time, emit func(postgres.Datapoint) error) error {
		if id != "GPU-1" || start == nil || end == nil || !start.Equal(base) || !end.Equal(base) || start.Location() != time.UTC {
			t.Fatal(id, start, end)
		}
		for _, id := range []string{"a", "b"} {
			if err := emit(postgres.Datapoint{EventID: id, ProcessedAt: base}); err != nil {
				return err
			}
		}
		return nil
	}}, nil)
	w := request(t, h, "/api/v1/gpus/GPU-1/telemetry?start_time=2026-09-12T17:30:00%2B05:30&end_time=2026-09-12T12:00:00Z", 200)
	var rows []postgres.Datapoint
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 2 || rows[0].EventID != "a" || rows[1].EventID != "b" {
		t.Fatal(err, rows)
	}
}
func TestNoHiddenRowLimit(t *testing.T) {
	h := setup(t, fakeReader{gpus: func(ctx context.Context, emit func(postgres.GPU) error) error {
		for i := 0; i < 1500; i++ {
			if err := emit(postgres.GPU{UUID: "g"}); err != nil {
				return err
			}
		}
		return nil
	}}, nil)
	w := request(t, h, "/api/v1/gpus", 200)
	var rows []postgres.GPU
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1500 {
		t.Fatal(err, len(rows))
	}
}
func TestLateDatabaseFailureIs503NotPartial200(t *testing.T) {
	h := setup(t, fakeReader{gpus: func(ctx context.Context, emit func(postgres.GPU) error) error {
		if err := emit(postgres.GPU{UUID: "g"}); err != nil {
			return err
		}
		return errors.New("connection died mid-result")
	}}, nil)
	w := request(t, h, "/api/v1/gpus", 503)
	if !json.Valid(w.Body.Bytes()) || strings.HasPrefix(w.Body.String(), "[") {
		t.Fatal(w.Body.String())
	}
}
func TestResourceFailures(t *testing.T) {
	db := fakeReader{gpus: func(ctx context.Context, emit func(postgres.GPU) error) error {
		return emit(postgres.GPU{UUID: "large"})
	}}
	request(t, setup(t, db, func(c *config.API) { c.MaxResponseBytes = 2 }), "/api/v1/gpus", 503)
	// Use a file as the temp directory so creation fails without deleting user files.
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	request(t, setup(t, db, func(c *config.API) { c.TempDir = file }), "/api/v1/gpus", 503)
	request(t, setup(t, fakeReader{gpus: func(ctx context.Context, _ func(postgres.GPU) error) error { <-ctx.Done(); return ctx.Err() }}, func(c *config.API) { c.QueryTimeout = time.Millisecond }), "/api/v1/gpus", 503)
}
func TestCapacityAndCancellation(t *testing.T) {
	entered := make(chan struct{})
	released := make(chan struct{})
	h := setup(t, fakeReader{gpus: func(ctx context.Context, _ func(postgres.GPU) error) error {
		close(entered)
		select {
		case <-released:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}, func(c *config.API) { c.MaxConcurrent = 1 })
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/gpus", nil))
	}()
	<-entered
	request(t, h, "/api/v1/gpus", 503)
	close(released)
	<-done
	c := config.DefaultAPI()
	ctx, cancel := context.WithCancel(context.Background())
	h, _ = New(ctx, fakeReader{}, c, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	cancel()
	request(t, h, "/readyz", 503)
	request(t, h, "/api/v1/gpus", 503)
}
func TestGeneratedOpenAPIContract(t *testing.T) {
	_, a := New(context.Background(), nil, config.DefaultAPI(), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	spec := a.OpenAPI()
	op := spec.Paths["/api/v1/gpus/{id}/telemetry"].Get
	if op == nil || op.OperationID != "get-gpu-telemetry" || len(op.Parameters) != 3 {
		t.Fatal("missing typed operation")
	}
	for _, status := range []string{"200", "400", "404", "503"} {
		if op.Responses[status] == nil {
			t.Fatal("missing status", status)
		}
	}
	schema := op.Responses["200"].Content["application/json"].Schema
	if schema.Type != "array" || schema.Nullable || schema.Items == nil {
		t.Fatal(schema)
	}
	generated, err := spec.YAML()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, saved) {
		t.Fatal("OpenAPI is stale; run make openapi")
	}
}
func TestParseRangeOptionalBounds(t *testing.T) {
	for _, q := range []string{"", "start_time=2026-01-01T00:00:00Z", "end_time=2026-01-01T00:00:00Z"} {
		if _, _, err := parseRange(q); err != nil {
			t.Fatal(err)
		}
	}
}
