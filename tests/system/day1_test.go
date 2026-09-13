//go:build integration

package system

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/telemetry"
)

func build(t *testing.T, ctx context.Context, root, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", path, "./cmd/"+name)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return path
}
func readLog(path, key string) map[string]any {
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == key {
			return entry
		}
	}
	return nil
}
func eventually(t *testing.T, ctx context.Context, f func() bool) {
	t.Helper()
	for {
		if f() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestDay1Processes launches all three real executables on an ephemeral port.
// Its CSV oracle reads source records independently of internal/streamer.
func TestDay1Processes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	broker := build(t, ctx, root, dir, "queue")
	producer := build(t, ctx, root, dir, "streamer")
	consumer := build(t, ctx, root, dir, "testconsumer")
	logPath := filepath.Join(dir, "queue.jsonl")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	q := exec.CommandContext(ctx, broker, "--mode", "memory", "--listen", "127.0.0.1:0", "--capacity", "8", "--shutdown-timeout", "1s")
	q.Stderr = logFile
	if err = q.Start(); err != nil {
		t.Fatal(err)
	}
	queueDone := make(chan error, 1)
	go func() { queueDone <- q.Wait() }()
	var once sync.Once
	stopQueue := func() {
		once.Do(func() {
			_ = q.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-queueDone:
				if err != nil {
					t.Errorf("queue shutdown: %v", err)
				}
			case <-time.After(2 * time.Second):
				q.Process.Kill()
				<-queueDone
				t.Error("queue did not shut down")
			}
		})
	}
	defer stopQueue()
	eventually(t, ctx, func() bool { return readLog(logPath, "queue listening") != nil })
	origin := "http://" + readLog(logPath, "queue listening")["address"].(string)
	output, err := os.Create(filepath.Join(dir, "received.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	consumerLog, err := os.Create(filepath.Join(dir, "consumer.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer consumerLog.Close()
	c := exec.CommandContext(ctx, consumer, "--mode", "memory", "--queue-url", origin, "--count", "100", "--timeout", "20s")
	c.Stdout = output
	c.Stderr = consumerLog
	if err = c.Start(); err != nil {
		t.Fatal(err)
	}
	consumerDone := make(chan error, 1)
	go func() { consumerDone <- c.Wait() }()
	consumerWaited := false
	defer func() {
		if !consumerWaited {
			_ = c.Process.Kill()
			<-consumerDone
		}
	}()
	csvPath := filepath.Join(root, "Problemstatement", "dcgm_metrics_20250718_134233.csv")
	started := time.Now().UTC().Truncate(time.Microsecond)
	p := exec.CommandContext(ctx, producer, "--csv", csvPath, "--queue-url", origin, "--rate", "100", "--count", "100")
	producerLog, err := p.CombinedOutput()
	if err != nil {
		t.Fatalf("streamer: %v\n%s", err, producerLog)
	}
	select {
	case err = <-consumerDone:
		consumerWaited = true
		if err != nil {
			b, _ := os.ReadFile(consumerLog.Name())
			t.Fatalf("consumer: %v\n%s", err, b)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err = output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reader := csv.NewReader(source)
	header, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for i, h := range header {
		positions[h] = i
	}
	decoder := json.NewDecoder(output)
	ids := map[string]bool{}
	producerID := ""
	for i := 1; i <= 100; i++ {
		var e telemetry.Event
		if err = decoder.Decode(&e); err != nil {
			t.Fatal(err)
		}
		if err = e.Validate(); err != nil {
			t.Fatal(err)
		}
		row, err := reader.Read()
		if err != nil {
			t.Fatal(err)
		}
		get := func(k string) string { return row[positions[k]] }
		ts, err := time.Parse(time.RFC3339Nano, get("timestamp"))
		if err != nil {
			t.Fatal(err)
		}
		value, err := strconv.ParseFloat(get("value"), 64)
		if err != nil {
			t.Fatal(err)
		}
		expected := telemetry.Measurement{SourceTimestamp: ts.UTC(), GPUUUID: get("uuid"), MetricName: get("metric_name"), Value: value, LocalGPUID: get("gpu_id"), Device: get("device"), ModelName: get("modelName"), Hostname: get("Hostname"), Container: get("container"), Pod: get("pod"), Namespace: get("namespace"), LabelsRaw: get("labels_raw")}
		if !reflect.DeepEqual(e.Measurement, expected) {
			t.Fatalf("source mismatch at %d: %+v", i, e)
		}
		if ids[e.EventID] || e.RowIndex != uint64(i) || e.ReplayIndex != 0 || e.ProcessedAt.Before(started) || e.ProcessedAt.After(time.Now()) {
			t.Fatalf("identity/time mismatch: %+v", e)
		}
		if producerID == "" {
			producerID = e.ProducerInstanceID
		}
		if e.ProducerInstanceID != producerID {
			t.Fatal("producer changed")
		}
		ids[e.EventID] = true
	}
	var extra telemetry.Event
	if err = decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("extra output: %v", err)
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(origin + "/internal/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	var stats queue.Stats
	err = json.NewDecoder(resp.Body).Decode(&stats)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Accepted != 100 || stats.Received != 100 || stats.Depth != 0 {
		t.Fatal(stats)
	}
	stopQueue()
	if readLog(logPath, "queue stopped") == nil {
		t.Fatal("missing graceful shutdown summary")
	}
	t.Logf("100 events verified: unique IDs, exact source values and metadata, UTC processing times, FIFO row order; queue accepted=%d received=%d depth=%d", stats.Accepted, stats.Received, stats.Depth)
	t.Logf("streamer summary: %s", strings.TrimSpace(string(producerLog)))
}

func TestStreamerSIGTERMUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root, _ := filepath.Abs("../..")
	dir := t.TempDir()
	binary := build(t, ctx, root, dir, "streamer")
	// A controlled unavailable endpoint exercises actual process signal handling.
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer unavailable.Close()
	path := filepath.Join(dir, "streamer.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, binary, "--csv", filepath.Join(root, "Problemstatement", "dcgm_metrics_20250718_134233.csv"), "--queue-url", unavailable.URL, "--http-timeout", "50ms", "--shutdown-timeout", "100ms", "--retry-min", "10ms", "--retry-max", "20ms")
	cmd.Stderr = f
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	eventually(t, ctx, func() bool { return readLog(path, "publish retry") != nil })
	start := time.Now()
	if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("shutdown deadline exceeded")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatal(fmt.Sprintf("expected unconfirmed shutdown failure, got %v", err))
	}
	summary := readLog(path, "streamer stopped")
	if summary["unconfirmed_pending"] != float64(1) || summary["accepted"] != float64(0) {
		t.Fatal(summary)
	}
	t.Logf("SIGTERM with unavailable queue: exited in %s, unconfirmed_pending=1", time.Since(start))
}
