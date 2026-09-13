//go:build integration

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/telemetry"
)

type brokerProcess struct {
	cmd     *exec.Cmd
	done    chan error
	stopped bool
	url     string
	log     *os.File
}

func startBroker(t *testing.T, ctx context.Context, binary, path string) *brokerProcess {
	t.Helper()
	log, err := os.CreateTemp(filepath.Dir(path), "broker-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	p := &brokerProcess{cmd: exec.CommandContext(ctx, binary, "--db", path, "--listen", "127.0.0.1:0", "--lease-duration", "700ms", "--shutdown-timeout", "1s"), done: make(chan error, 1), log: log}
	p.cmd.Stderr = log
	if err = p.cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait() }()
	t.Cleanup(func() {
		if !p.stopped {
			p.cmd.Process.Kill()
			<-p.done
		}
		log.Close()
	})
	eventually(t, ctx, func() bool { return readLog(log.Name(), "queue listening") != nil })
	p.url = "http://" + readLog(log.Name(), "queue listening")["address"].(string)
	return p
}
func (p *brokerProcess) kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-p.done; err == nil {
		t.Fatal("expected abrupt SIGKILL exit")
	}
	p.stopped = true
}
func newClient(t *testing.T, url string) *queueclient.Client {
	t.Helper()
	c, err := queueclient.New(url, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func durableStats(t *testing.T, url string) queue.DurableStats {
	t.Helper()
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get(url + "/internal/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s queue.DurableStats
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	if err = json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDay2CrashRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root, _ := filepath.Abs("../..")
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.db")
	broker := build(t, ctx, root, dir, "queue")
	producer := build(t, ctx, root, dir, "streamer")
	consumer := build(t, ctx, root, dir, "testconsumer")
	p := startBroker(t, ctx, broker, path)
	second := exec.CommandContext(ctx, broker, "--db", path, "--listen", "127.0.0.1:0", "--db-open-timeout", "50ms")
	if output, err := second.CombinedOutput(); err == nil || !strings.Contains(string(output), "one writer only") {
		t.Fatalf("second broker lock check: %v %s", err, output)
	}
	t.Log("A second broker process was rejected by the bounded exclusive database lock")

	c := newClient(t, p.url)
	known := telemetry.Event{SchemaVersion: 1, EventID: "durable-known", ProducerInstanceID: "system-test", RowIndex: 1, ProcessedAt: time.Now().UTC().Truncate(time.Microsecond), Measurement: telemetry.Measurement{GPUUUID: "GPU-test", MetricName: "GPU_UTIL", Value: 42, SourceTimestamp: time.Date(2025, 7, 18, 0, 0, 0, 0, time.UTC)}}
	// Commit at the broker but deliberately drop the success response to the publisher.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req queue.PublishRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if err := c.Publish(r.Context(), req.Events[0]); err != nil {
			t.Error(err)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer proxy.Close()
	sender := newClient(t, proxy.URL)
	if err := sender.Publish(ctx, known); !queueclient.Retryable(err) {
		t.Fatal("expected ambiguous publish", err)
	}
	p.kill(t)
	p = startBroker(t, ctx, broker, path)
	c = newClient(t, p.url)
	if err := c.Publish(ctx, known); err != nil {
		t.Fatal(err)
	}
	if s := durableStats(t, p.url); s.Accepted != 1 || s.Ready != 1 {
		t.Fatal("dedup failed across restart", s)
	}
	t.Log("Lost publish response + SIGKILL/restart: accepted message recovered, publisher retry deduplicated")

	consumerLog, err := os.Create(filepath.Join(dir, "consumer.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer consumerLog.Close()
	victim := exec.CommandContext(ctx, consumer, "--queue-url", p.url, "--count", "1", "--processing-delay", "30s", "--timeout", "60s")
	victim.Stdout = io.Discard
	victim.Stderr = consumerLog
	if err = victim.Start(); err != nil {
		t.Fatal(err)
	}
	victimDone := make(chan error, 1)
	go func() { victimDone <- victim.Wait() }()
	victimStopped := false
	defer func() {
		if !victimStopped {
			victim.Process.Kill()
			<-victimDone
		}
	}()
	eventually(t, ctx, func() bool { return readLog(consumerLog.Name(), "delivery leased") != nil })
	oldToken := readLog(consumerLog.Name(), "delivery leased")["token"].(string)
	if err = victim.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-victimDone
	victimStopped = true
	p.kill(t)
	p = startBroker(t, ctx, broker, path)
	c = newClient(t, p.url)
	delivery, ok, err := c.Lease(ctx, 2*time.Second)
	if err != nil || !ok || delivery.Event.EventID != known.EventID || delivery.Token == oldToken || delivery.Attempt != 2 {
		t.Fatal(delivery, ok, err)
	}
	if err = c.Ack(ctx, queue.AckRequest{EventID: known.EventID, Token: oldToken}); err == nil {
		t.Fatal("stale ACK accepted")
	}
	if err = c.Nack(ctx, queue.NackRequest{EventID: known.EventID, Token: oldToken, Reason: "stale"}); err == nil {
		t.Fatal("stale NACK accepted")
	}
	receipt := queue.AckRequest{EventID: known.EventID, Token: delivery.Token}
	if err = c.Ack(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	t.Log("Consumer SIGKILL before ACK + broker restart: lease expired, new token delivered, stale receipts rejected")
	p.kill(t)
	p = startBroker(t, ctx, broker, path)
	c = newClient(t, p.url)
	if err = c.Ack(ctx, receipt); err != nil {
		t.Fatal("completed ACK retry failed", err)
	}
	if err = c.Publish(ctx, known); err != nil {
		t.Fatal(err)
	}
	if _, ok, err = c.Lease(ctx, 0); ok || err != nil {
		t.Fatal("ACKed message redelivered", ok, err)
	}
	t.Log("ACK completion survived another SIGKILL/restart; ACK retry and publisher retry remain safe")

	// Prove unchanged Day 1 producer publishes to durable mode, and the updated
	// consumer outputs every event then ACKs. All tests use only temporary storage.
	source := filepath.Join(root, "Problemstatement", "dcgm_metrics_20250718_134233.csv")
	cmd := exec.CommandContext(ctx, producer, "--queue-url", p.url, "--csv", source, "--rate", "1000", "--count", "20")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("streamer %v: %s", err, out)
	}
	cmd = exec.CommandContext(ctx, consumer, "--queue-url", p.url, "--count", "20", "--timeout", "20s")
	var output, logs bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &logs
	if err = cmd.Run(); err != nil {
		t.Fatal(err, logs.String())
	}
	decoder := json.NewDecoder(&output)
	ids := map[string]bool{}
	for i := 1; i <= 20; i++ {
		var event telemetry.Event
		if err = decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
		if ids[event.EventID] || event.RowIndex != uint64(i) || event.Validate() != nil {
			t.Fatal(event)
		}
		ids[event.EventID] = true
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("extra event", err)
	}
	s := durableStats(t, p.url)
	if s.Accepted != 21 || s.Acked != 21 || s.Ready != 0 || s.Leased != 0 || s.Completed != 21 {
		t.Fatal(s)
	}
	t.Logf("Existing streamer + leased consumer: 20 events; total accepted=%d acked=%d ready=%d leased=%d completed=%d file_bytes=%d", s.Accepted, s.Acked, s.Ready, s.Leased, s.Completed, s.FileBytes)
	if err = p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-p.done:
		p.stopped = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("graceful shutdown timed out")
	}
	if !strings.Contains(readLog(p.log.Name(), "queue stopped")["mode"].(string), "durable") {
		t.Fatal("missing shutdown record")
	}
}
