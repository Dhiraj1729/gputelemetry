//go:build postgres_integration

package day4

import (
	"context"
	"encoding/json"
	"fmt"
	"gpu-telemetry/internal/api"
	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/storage/postgres"
	"gpu-telemetry/internal/telemetry"
	"gpu-telemetry/migrations"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDay4Postgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dc := os.Getenv("DOCKER_CONTEXT")
	if dc == "" {
		dc = os.Getenv("DAY4_DOCKER_CONTEXT")
	}
	if dc == "" {
		dc = "colima-gpu-telemetry"
	}
	image := os.Getenv("POSTGRES_IMAGE")
	if image == "" {
		image = os.Getenv("DAY4_POSTGRES_IMAGE")
	}
	if image == "" {
		image = "postgres:18"
	}
	docker := func(args ...string) string {
		t.Helper()
		b, err := exec.CommandContext(ctx, "docker", append([]string{"--context", dc}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("Docker %s: %v: %s", args[0], err, b)
		}
		return strings.TrimSpace(string(b))
	}
	name := fmt.Sprintf("gpu-day4-test-%d", time.Now().UnixNano())
	docker("run", "-d", "--name", name, "--memory", "512m", "--cpus", "1", "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=day4-local-test", "-e", "POSTGRES_DB=telemetry", image)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		b, err := exec.CommandContext(clean, "docker", "--context", dc, "rm", "-f", name).CombinedOutput()
		if err != nil {
			t.Logf("test container cleanup: %v %s", err, b)
		}
	})
	dburl := "postgres://postgres:day4-local-test@" + docker("port", name, "5432/tcp") + "/telemetry?sslmode=disable"
	writeDB, err := postgres.Open(ctx, dburl, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer writeDB.Close()
	eventually(t, 30*time.Second, func() bool {
		c, done := context.WithTimeout(ctx, time.Second)
		defer done()
		return writeDB.Pool.Ping(c) == nil
	})
	if err = migrations.Apply(ctx, writeDB.Pool); err != nil {
		t.Fatal(err)
	}
	var version string
	if err = writeDB.Pool.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Log("PostgreSQL", version, "image", docker("inspect", "--format", "{{.Image}}", name))
	readDB, err := postgres.OpenReadOnly(ctx, dburl, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer readDB.Close()
	c := config.DefaultAPI()
	c.QueryTimeout = 200 * time.Millisecond
	c.TempDir = t.TempDir()
	handler, _ := api.New(ctx, readDB, c, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	server := httptest.NewServer(handler)
	defer server.Close()
	get := func(t *testing.T, path string, status int) []byte {
		t.Helper()
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s status=%d want=%d: %s", path, resp.StatusCode, status, body)
		}
		return body
	}
	if body := get(t, "/api/v1/gpus", 200); string(body) != "[]" {
		t.Fatal("empty DB", string(body))
	}
	base := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	put := func(id, gpu string, at time.Time) {
		t.Helper()
		e := telemetry.Event{SchemaVersion: 1, EventID: id, ProducerInstanceID: "day4", RowIndex: 1, ProcessedAt: at, Measurement: telemetry.Measurement{GPUUUID: gpu, MetricName: "power", Value: 42, SourceTimestamp: base.AddDate(-1, 0, 0), Hostname: "host", ModelName: "H100", LabelsRaw: "raw labels"}}
		if err := writeDB.Persist(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	put("b", "GPU-B", base)
	put("a", "GPU-B", base)
	put("c", "GPU-B", base.Add(time.Microsecond))
	put("d", "GPU-A", base.Add(time.Second))
	if _, err = writeDB.Pool.Exec(ctx, `INSERT INTO gpus(uuid,hostname,local_gpu_id,device,model_name,first_seen,last_seen,last_event_id) VALUES('GPU-orphan','','','','',now(),now(),'orphan')`); err != nil {
		t.Fatal(err)
	}
	t.Run("gpus_order_and_no_orphans", func(t *testing.T) {
		var rows []postgres.GPU
		if err := json.Unmarshal(get(t, "/api/v1/gpus", 200), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].UUID != "GPU-A" || rows[1].UUID != "GPU-B" {
			t.Fatal(rows)
		}
	})
	t.Run("inclusive_bounds_order_and_precision", func(t *testing.T) {
		cases := []struct {
			query string
			ids   string
		}{
			{"", "a,b,c"}, {"?start_time=2026-09-12T12:00:00Z&end_time=2026-09-12T12:00:00Z", "a,b"},
			{"?start_time=2026-09-12T17:30:00%2B05:30&end_time=2026-09-12T12:00:00.000001Z", "a,b,c"},
			{"?start_time=2026-09-12T12:00:00.000000001Z", "c"},
			{"?end_time=2026-09-12T12:00:00.000000999Z", "a,b"},
			{"?start_time=2026-09-12T12:00:00.000000001Z&end_time=2026-09-12T12:00:00.000000999Z", ""},
			{"?start_time=2027-01-01T00:00:00Z", ""}}
		for _, tc := range cases {
			var rows []postgres.Datapoint
			body := get(t, "/api/v1/gpus/GPU-B/telemetry"+tc.query, 200)
			if err := json.Unmarshal(body, &rows); err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, d := range rows {
				ids = append(ids, d.EventID)
				if d.ProcessedAt.Location() != time.UTC || d.LabelsRaw != "raw labels" || d.Value != 42 {
					t.Fatal(d)
				}
			}
			if strings.Join(ids, ",") != tc.ids {
				t.Fatalf("%s got %v want %s", tc.query, ids, tc.ids)
			}
			if tc.ids == "" && string(body) != "[]" {
				t.Fatal(string(body))
			}
		}
	})
	t.Run("unknown_invalid_and_readonly", func(t *testing.T) {
		get(t, "/api/v1/gpus/missing/telemetry", 404)
		get(t, "/api/v1/gpus/GPU-B/telemetry?start_time=no", 400)
		get(t, "/api/v1/gpus/GPU-B/telemetry?start_time=2027-01-01T00:00:00Z&end_time=2026-01-01T00:00:00Z", 400)
		var mode string
		if err := readDB.Pool.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&mode); err != nil || mode != "on" {
			t.Fatal(mode, err)
		}
		// Harmless zero-row UPDATE still must be refused by the read-only session.
		if _, err := readDB.Pool.Exec(ctx, `UPDATE telemetry SET value=value WHERE false`); err == nil {
			t.Fatal("API pool permitted write")
		}
	})
	t.Run("database_timeout_returns_503", func(t *testing.T) {
		tx, err := writeDB.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `LOCK TABLE telemetry IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		get(t, "/api/v1/gpus/GPU-B/telemetry", 503)
		if err = tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		get(t, "/api/v1/gpus/GPU-B/telemetry", 200)
	})
	t.Run("real_API_binary_and_SIGTERM", func(t *testing.T) {
		root, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		temp := t.TempDir()
		binary := filepath.Join(temp, "api")
		build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/api")
		build.Dir = root
		if b, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, b)
		}
		logFile, err := os.Create(filepath.Join(temp, "api.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer logFile.Close()
		cmd := exec.Command(binary, "--listen", "127.0.0.1:0")
		cmd.Env = append(os.Environ(), "DATABASE_URL="+dburl)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer cmd.Process.Kill()
		var address string
		eventually(t, 5*time.Second, func() bool {
			b, _ := os.ReadFile(logFile.Name())
			for _, line := range strings.Split(string(b), "\n") {
				var event struct {
					Address string `json:"address"`
				}
				if json.Unmarshal([]byte(line), &event) == nil && event.Address != "" {
					address = event.Address
					return true
				}
			}
			return false
		})
		resp, err := http.Get("http://" + address + "/api/v1/gpus")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
		if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("API shutdown timed out")
		}
	})
	t.Run("unavailable_database", func(t *testing.T) {
		docker("stop", "--time", "1", name)
		get(t, "/healthz", 200)
		get(t, "/readyz", 503)
		get(t, "/api/v1/gpus", 503)
		get(t, "/api/v1/gpus/GPU-B/telemetry", 503)
	})
	t.Log("Verified real PostgreSQL ordering, inclusive/sub-microsecond bounds, empty arrays, 404/400/503, read-only sessions and API process shutdown")
}
func eventually(t *testing.T, d time.Duration, check func() bool) {
	t.Helper()
	until := time.Now().Add(d)
	for time.Now().Before(until) {
		if check() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out")
}
