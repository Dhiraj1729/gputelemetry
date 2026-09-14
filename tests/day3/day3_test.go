//go:build postgres_integration

package day3

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gpu-telemetry/internal/collector"
	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/storage/postgres"
	"gpu-telemetry/internal/telemetry"
	"gpu-telemetry/migrations"
)

func TestDay3Postgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dc := os.Getenv("DOCKER_CONTEXT")
	if dc == "" {
		dc = os.Getenv("DAY3_DOCKER_CONTEXT")
	}
	if dc == "" {
		dc = "colima-gpu-telemetry"
	}
	docker := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", append([]string{"--context", dc}, args...)...)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v: %s", args[0], err, b)
		}
		return strings.TrimSpace(string(b))
	}
	image := os.Getenv("POSTGRES_IMAGE")
	if image == "" {
		image = os.Getenv("DAY3_POSTGRES_IMAGE")
	}
	if image == "" {
		image = "postgres:18"
	}
	name := fmt.Sprintf("gpu-day3-test-%d", time.Now().UnixNano())
	docker("run", "-d", "--rm", "--name", name, "--memory", "512m", "--cpus", "1", "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=day3-local-test", "-e", "POSTGRES_DB=telemetry", image)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if b, err := exec.CommandContext(cleanup, "docker", "--context", dc, "rm", "-f", name).CombinedOutput(); err != nil {
			t.Logf("test container cleanup: %v %s", err, b)
		}
	})
	address := docker("port", name, "5432/tcp")
	dburl := "postgres://postgres:day3-local-test@" + address + "/telemetry?sslmode=disable"
	s, err := postgres.Open(ctx, dburl, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	eventually(t, 30*time.Second, func() bool { p, c := context.WithTimeout(ctx, time.Second); defer c(); return s.Pool.Ping(p) == nil })
	t.Log("PostgreSQL image:", docker("inspect", "--format", "{{.Image}}", name))
	if err = migrations.Apply(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Apply(ctx, s.Pool); err != nil {
		t.Fatal("migration rerun", err)
	}
	if err = s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	var version string
	if err = s.Pool.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Log("PostgreSQL", version, "migration rerun passed")

	opts := queue.DefaultDurableOptions(filepath.Join(t.TempDir(), "queue.db"))
	opts.LeaseDuration = 1500 * time.Millisecond
	broker, err := queue.OpenDurable(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server := httptest.NewServer(queue.DurableHandler(broker, log))
	defer server.Close()
	q, err := queueclient.New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	c := config.Collector{Workers: 2, WorkTimeout: 500 * time.Millisecond, ShutdownTimeout: time.Second, RetryMin: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond, LeaseWait: 10 * time.Millisecond}
	event := func(id string) telemetry.Event {
		return telemetry.Event{SchemaVersion: 1, EventID: id, ProducerInstanceID: "day3", RowIndex: 1, ProcessedAt: time.Now().UTC().Truncate(time.Microsecond), Measurement: telemetry.Measurement{GPUUUID: "GPU-" + id, MetricName: "power", Value: 123.5, SourceTimestamp: time.Date(2025, 7, 18, 20, 42, 34, 0, time.UTC), Hostname: "host", LocalGPUID: "0", Device: "nvidia0", ModelName: "H100", LabelsRaw: "raw labels"}}
	}
	lease := func() queue.Delivery {
		t.Helper()
		var d queue.Delivery
		eventually(t, 4*time.Second, func() bool {
			var ok bool
			d, ok, err = q.LeaseForProcessing(ctx, 50*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			return ok
		})
		return d
	}
	count := func(id string) int {
		t.Helper()
		var n int
		if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM telemetry WHERE event_id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("normal_and_concurrent_duplicate", func(t *testing.T) {
		e := event("normal")
		if err = q.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
		d := lease()
		if err = collector.Process(ctx, c, q, s, d, log); err != nil {
			t.Fatal(err)
		}
		if count(e.EventID) != 1 {
			t.Fatal("missing row")
		}
		var first time.Time
		if err = s.Pool.QueryRow(ctx, `SELECT ingested_at FROM telemetry WHERE event_id=$1`, e.EventID).Scan(&first); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		fail := make(chan error, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); fail <- s.Persist(ctx, e) }()
		}
		wg.Wait()
		close(fail)
		for err := range fail {
			if err != nil {
				t.Fatal(err)
			}
		}
		var ingested, processed, source time.Time
		var value float64
		var labels string
		if err = s.Pool.QueryRow(ctx, `SELECT ingested_at,processed_at,source_timestamp,value,labels_raw FROM telemetry WHERE event_id=$1`, e.EventID).Scan(&ingested, &processed, &source, &value, &labels); err != nil {
			t.Fatal(err)
		}
		if count(e.EventID) != 1 || !first.Equal(ingested) || !processed.Equal(e.ProcessedAt) || !source.Equal(e.SourceTimestamp) || value != e.Value || labels != e.LabelsRaw {
			t.Fatal("duplicate/metadata/timestamp mismatch")
		}
		t.Log("12 concurrent duplicate writes retained one row and original ingestion time")
	})
	t.Run("metadata_order_and_atomic_rollback", func(t *testing.T) {
		e := event("metadata-new")
		if err = s.Persist(ctx, e); err != nil {
			t.Fatal(err)
		}
		old := e
		old.EventID = "metadata-old"
		old.ProcessedAt = old.ProcessedAt.Add(-time.Hour)
		old.Hostname = "old-host"
		if err = s.Persist(ctx, old); err != nil {
			t.Fatal(err)
		}
		var hostname string
		var first, last time.Time
		if err = s.Pool.QueryRow(ctx, `SELECT hostname,first_seen,last_seen FROM gpus WHERE uuid=$1`, e.GPUUUID).Scan(&hostname, &first, &last); err != nil {
			t.Fatal(err)
		}
		if hostname != e.Hostname || !first.Equal(old.ProcessedAt) || !last.Equal(e.ProcessedAt) {
			t.Fatal("metadata regressed")
		}
		// Force the second statement to fail; the first telemetry INSERT must roll back.
		if _, err = s.Pool.Exec(ctx, `ALTER TABLE gpus ADD CONSTRAINT test_reject CHECK (hostname <> 'reject-test')`); err != nil {
			t.Fatal(err)
		}
		bad := event("rollback")
		bad.Hostname = "reject-test"
		if err = s.Persist(ctx, bad); err == nil {
			t.Fatal("expected transaction failure")
		}
		if count(bad.EventID) != 0 {
			t.Fatal("partial transaction persisted")
		}
	})
	t.Run("database_lock_timeout_no_ack_then_redelivery", func(t *testing.T) {
		e := event("locked")
		if err = q.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
		d := lease()
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `LOCK TABLE telemetry IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		short := c
		short.WorkTimeout = 150 * time.Millisecond
		if err = collector.Process(ctx, short, q, s, d, log); err == nil {
			t.Fatal("expected database deadline")
		}
		stats, err := broker.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Leased != 1 {
			t.Fatal("failed DB work was ACKed", stats)
		}
		if err = tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if count(e.EventID) != 0 {
			t.Fatal("timed out insert committed")
		}
		d = lease()
		if d.Attempt != 2 {
			t.Fatal("expected redelivery", d.Attempt)
		}
		if err = collector.Process(ctx, c, q, s, d, log); err != nil {
			t.Fatal(err)
		}
		if count(e.EventID) != 1 {
			t.Fatal("recovery missing")
		}
		t.Log("PostgreSQL lock timeout caused no ACK; redelivery persisted after recovery")
	})
	t.Run("invalid_quarantine", func(t *testing.T) {
		e := event("invalid-nul")
		e.LabelsRaw = "bad\x00text"
		if err = q.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
		d := lease()
		if err = collector.Process(ctx, c, q, s, d, log); err != nil {
			t.Fatal(err)
		}
		stats, err := broker.Snapshot(ctx)
		if err != nil || stats.Quarantined != 1 || count(e.EventID) != 0 {
			t.Fatal(err, stats)
		}
	})
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	for _, app := range []string{"collector", "streamer"} {
		cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(temp, app), "./cmd/"+app)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, b)
		}
	}
	start := func(queueURL string) (*exec.Cmd, *os.File) {
		t.Helper()
		f, err := os.CreateTemp(temp, "collector-*.log")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(filepath.Join(temp, "collector"), "--queue-url", queueURL, "--listen", "127.0.0.1:0", "--workers", "2", "--work-timeout", "1s", "--shutdown-timeout", "2s")
		cmd.Env = append(os.Environ(), "DATABASE_URL="+dburl)
		cmd.Stdout = f
		cmd.Stderr = f
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = f.Close() })
		return cmd, f
	}
	t.Run("commit_then_collector_SIGKILL_before_ACK", func(t *testing.T) {
		target, _ := url.Parse(server.URL)
		proxy := httputil.NewSingleHostReverseProxy(target)
		gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/internal/v1/acks" {
				http.Error(w, "injected lost ACK", 503)
				return
			}
			proxy.ServeHTTP(w, r)
		}))
		defer gate.Close()
		e := event("commit-crash")
		if err = q.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
		cmd, _ := start(gate.URL)
		eventually(t, 10*time.Second, func() bool { return count(e.EventID) == 1 })
		if err = cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		d := lease()
		if d.Event.EventID != e.EventID || d.Attempt < 2 {
			t.Fatal("no expected redelivery", d)
		}
		if err = collector.Process(ctx, c, q, s, d, log); err != nil {
			t.Fatal(err)
		}
		if count(e.EventID) != 1 {
			t.Fatal("duplicate row")
		}
		t.Log("Real collector committed, ACK was blocked, collector SIGKILL, redelivery: exactly one telemetry row")
	})
	t.Run("real_streamer_and_collector", func(t *testing.T) {
		var before int
		if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM telemetry`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		cmd, _ := start(server.URL)
		stream := exec.CommandContext(ctx, filepath.Join(temp, "streamer"), "--queue-url", server.URL, "--csv", filepath.Join(root, config.DefaultCSV), "--count", "20", "--rate", "100")
		if b, err := stream.CombinedOutput(); err != nil {
			t.Fatalf("streamer %v %s", err, b)
		}
		eventually(t, 10*time.Second, func() bool {
			var n int
			if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM telemetry`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			stats, err := broker.Snapshot(ctx)
			return err == nil && n == before+20 && stats.Ready == 0 && stats.Leased == 0
		})
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
		case <-time.After(4 * time.Second):
			t.Fatal("collector shutdown exceeded bound")
		}
		t.Log("20 CSV events persisted and ACKed; real collector SIGTERM exited cleanly")
	})
}
func eventually(t *testing.T, timeout time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition not reached before timeout")
}
