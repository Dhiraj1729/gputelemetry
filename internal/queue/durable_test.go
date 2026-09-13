package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"gpu-telemetry/internal/telemetry"
)

var bg = context.Background()

type clock struct{ n atomic.Int64 }

func (c *clock) now() time.Time      { return time.Unix(0, c.n.Load()).UTC() }
func (c *clock) add(d time.Duration) { c.n.Add(int64(d)) }
func fixture(t *testing.T, change func(*DurableOptions)) (*Durable, *clock) {
	t.Helper()
	c := &clock{}
	c.n.Store(time.Now().UnixNano())
	o := DefaultDurableOptions(filepath.Join(t.TempDir(), "queue.db"))
	o.Now = c.now
	o.CleanupInterval = time.Hour
	o.LeaseDuration = time.Second
	o.CompletionTTL = time.Minute
	if change != nil {
		change(&o)
	}
	d, err := OpenDurable(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d, c
}
func publish(t *testing.T, d *Durable, id string) {
	t.Helper()
	dup, err := d.Publish(event(id))
	if err != nil || dup {
		t.Fatal(dup, err)
	}
}
func lease(t *testing.T, d *Durable) Delivery {
	t.Helper()
	e, ok, err := d.Lease(bg, 0)
	if err != nil || !ok {
		t.Fatal(e, ok, err)
	}
	return e
}
func ack(t *testing.T, d *Durable, e Delivery) {
	t.Helper()
	if _, err := d.Ack(bg, AckRequest{e.Event.EventID, e.Token}); err != nil {
		t.Fatal(err)
	}
}
func reopen(t *testing.T, d *Durable) *Durable {
	t.Helper()
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	n, err := OpenDurable(d.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}
func TestDurableRecoveryAndCompletion(t *testing.T) {
	d, _ := fixture(t, nil)
	publish(t, d, "a")
	d = reopen(t, d)
	if dup, err := d.Publish(event("a")); !dup || err != nil {
		t.Fatal(dup, err)
	}
	changed := event("a")
	changed.Value++
	if _, err := d.Publish(changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	e := lease(t, d)
	if e.Event.EventID != "a" || e.Attempt != 1 {
		t.Fatal(e)
	}
	ack(t, d, e)
	d = reopen(t, d)
	if dup, err := d.Ack(bg, AckRequest{"a", e.Token}); !dup || err != nil {
		t.Fatal(dup, err)
	}
	if dup, err := d.Publish(event("a")); !dup || err != nil {
		t.Fatal(dup, err)
	}
	if _, ok, err := d.Lease(bg, 0); ok || err != nil {
		t.Fatal(ok, err)
	}
	s, err := d.Snapshot(bg)
	if err != nil || s.Completed != 1 || s.Accepted != 1 || s.Acked != 1 || s.LogicalBytes != completedOverhead {
		t.Fatal(s, err)
	}
}
func TestLeaseExpiryRestartAndStaleTokens(t *testing.T) {
	d, c := fixture(t, nil)
	publish(t, d, "a")
	old := lease(t, d)
	d = reopen(t, d)
	if _, ok, err := d.Lease(bg, 0); ok || err != nil {
		t.Fatal("overlapping lease", ok, err)
	}
	c.add(time.Second)
	if _, err := d.Ack(bg, AckRequest{"a", old.Token}); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	fresh := lease(t, d)
	if fresh.Token == old.Token || fresh.Attempt != 2 {
		t.Fatal(fresh)
	}
	if _, err := d.Ack(bg, AckRequest{"a", old.Token}); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	if _, err := d.Nack(bg, NackRequest{EventID: "a", Token: old.Token, Reason: "late"}); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	ack(t, d, fresh)
}
func TestNackRetryQuarantineAndIdempotence(t *testing.T) {
	d, c := fixture(t, nil)
	publish(t, d, "a")
	e := lease(t, d)
	n := NackRequest{EventID: "a", Token: e.Token, DelayMillis: 500, Reason: "temporary"}
	if dup, err := d.Nack(bg, n); dup || err != nil {
		t.Fatal(dup, err)
	}
	d = reopen(t, d)
	if dup, err := d.Nack(bg, n); !dup || err != nil {
		t.Fatal(dup, err)
	}
	different := n
	different.Reason = "changed"
	if _, err := d.Nack(bg, different); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, ok, err := d.Lease(bg, 0); ok || err != nil {
		t.Fatal(ok, err)
	}
	c.add(500 * time.Millisecond)
	next := lease(t, d)
	if _, err := d.Nack(bg, n); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	n = NackRequest{EventID: "a", Token: next.Token, Permanent: true, Reason: "invalid metric"}
	if _, err := d.Nack(bg, n); err != nil {
		t.Fatal(err)
	}
	d = reopen(t, d)
	c.add(365 * 24 * time.Hour)
	if err := d.Cleanup(bg); err != nil {
		t.Fatal(err)
	}
	if dup, err := d.Nack(bg, n); !dup || err != nil {
		t.Fatal(dup, err)
	}
	if dup, err := d.Publish(event("a")); !dup || err != nil {
		t.Fatal(dup, err)
	}
	if _, ok, err := d.Lease(bg, 0); ok || err != nil {
		t.Fatal(ok, err)
	}
	s, _ := d.Snapshot(bg)
	if s.Quarantined != 1 || s.Leased != 0 {
		t.Fatal(s)
	}
}
func TestDurableCapacityReservationAndBoundedCleanup(t *testing.T) {
	d, c := fixture(t, func(o *DurableOptions) { o.Capacity = 3; o.DedupCapacity = 3; o.CleanupBatch = 1 })
	for _, id := range []string{"a", "b", "c"} {
		publish(t, d, id)
	}
	first := lease(t, d)
	if _, err := d.Publish(event("d")); !errors.Is(err, ErrFull) {
		t.Fatal("leased event must count", err)
	}
	ack(t, d, first)
	ack(t, d, lease(t, d))
	ack(t, d, lease(t, d))
	if _, err := d.Publish(event("d")); !errors.Is(err, ErrDedupFull) {
		t.Fatal(err)
	}
	c.add(time.Minute)
	if err := d.Cleanup(bg); err != nil {
		t.Fatal(err)
	}
	s, _ := d.Snapshot(bg)
	if s.Completed != 2 {
		t.Fatal("cleanup not bounded", s)
	}
	if err := d.Cleanup(bg); err != nil {
		t.Fatal(err)
	}
	if err := d.Cleanup(bg); err != nil {
		t.Fatal(err)
	}
	if dup, err := d.Publish(event("a")); dup || err != nil {
		t.Fatal("expired completion can be republished", dup, err)
	}
}
func TestLogicalBytesPayloadAndQuarantineBounds(t *testing.T) {
	d, _ := fixture(t, func(o *DurableOptions) { o.MaxPayloadBytes = 1024; o.MaxLogicalBytes = activeOverhead + 1024 })
	publish(t, d, "a")
	if _, err := d.Publish(event("b")); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	huge := event("huge")
	huge.LabelsRaw = strings.Repeat("x", 1024)
	if _, err := d.Publish(huge); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	d2, _ := fixture(t, func(o *DurableOptions) { o.QuarantineCapacity = 1 })
	publish(t, d2, "a")
	publish(t, d2, "b")
	a := lease(t, d2)
	b := lease(t, d2)
	if _, err := d2.Nack(bg, NackRequest{EventID: "a", Token: a.Token, Permanent: true, Reason: "bad"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d2.Nack(bg, NackRequest{EventID: "b", Token: b.Token, Permanent: true, Reason: "bad"}); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	ack(t, d2, b) // Rejected NACK must leave the current lease intact.
}
func TestCompletionExpiredACKAndContentConflict(t *testing.T) {
	d, c := fixture(t, nil)
	publish(t, d, "a")
	e := lease(t, d)
	ack(t, d, e)
	c.add(time.Minute)
	if _, err := d.Ack(bg, AckRequest{"a", e.Token}); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	changed := event("a")
	changed.Value++
	if dup, err := d.Publish(changed); dup || err != nil {
		t.Fatal(dup, err)
	}
}
func TestConcurrentConsumersAndACKExpiryRace(t *testing.T) {
	d, c := fixture(t, nil)
	const count = 32
	for i := 0; i < count; i++ {
		publish(t, d, fmt.Sprint(i))
	}
	results := make(chan Delivery, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, ok, err := d.Lease(bg, 0)
			if err != nil {
				t.Error(err)
			}
			if ok {
				results <- e
			}
		}()
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for e := range results {
		if seen[e.Event.EventID] {
			t.Fatal("overlap", e)
		}
		seen[e.Event.EventID] = true
		ack(t, d, e)
	}
	if len(seen) != count {
		t.Fatal(len(seen))
	}
	publish(t, d, "race")
	e := lease(t, d)
	c.add(time.Second)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := d.Ack(bg, AckRequest{"race", e.Token}); !errors.Is(err, ErrStaleLease) {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := d.Cleanup(bg); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	fresh := lease(t, d)
	ack(t, d, fresh)
}
func TestPollingCancellationWakeAndStop(t *testing.T) {
	d, _ := fixture(t, nil)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, _, err := d.Lease(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	out := make(chan error, 1)
	go func() {
		e, ok, err := d.Lease(bg, time.Second)
		if err == nil && (!ok || e.Event.EventID != "a") {
			err = errors.New("missing notification")
		}
		out <- err
	}()
	publish(t, d, "a")
	if err := <-out; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, ok, err := d.Lease(bg, 20*time.Millisecond); ok || err != nil {
		t.Fatal(ok, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("poll exceeded bound")
	}
	go func() { _, _, err := d.Lease(bg, time.Second); out <- err }()
	d.Stop()
	if err := <-out; !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := d.Publish(event("b")); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestExclusiveOpen(t *testing.T) {
	d, _ := fixture(t, nil)
	o := d.opts
	o.OpenTimeout = 30 * time.Millisecond
	start := time.Now()
	other, err := OpenDurable(o)
	if err == nil {
		other.Close()
		t.Fatal("second writer opened")
	}
	if time.Since(start) > time.Second {
		t.Fatal("lock wait unbounded")
	}
}

// Fails after running the mutation callback but before commit, forcing rollback
// of all state/index/counter changes, as a commit/write failure must do.
type failingDB struct {
	database
	fail atomic.Bool
}

func (f *failingDB) Update(fn func(*bolt.Tx) error) error {
	return f.database.Update(func(tx *bolt.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if f.fail.Load() {
			return errors.New("injected disk write failure")
		}
		return nil
	})
}
func TestStorageFailureNeverConfirmsPublishOrACK(t *testing.T) {
	d, _ := fixture(t, nil)
	f := &failingDB{database: d.db}
	d.db = f
	f.fail.Store(true)
	h := DurableHandler(d, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	body, _ := json.Marshal(PublishRequest{Events: []telemetry.Event{event("a")}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/internal/v1/messages", strings.NewReader(string(body))))
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, _ := d.Snapshot(bg)
	if s.Accepted != 0 || s.Ready != 0 {
		t.Fatal(s)
	}
	f.fail.Store(false)
	publish(t, d, "a")
	e := lease(t, d)
	f.fail.Store(true)
	if _, err := d.Ack(bg, AckRequest{"a", e.Token}); err == nil {
		t.Fatal("false ACK")
	}
	s, _ = d.Snapshot(bg)
	if s.Acked != 0 || s.Leased != 1 {
		t.Fatal(s)
	}
	f.fail.Store(false)
	ack(t, d, e)
}
func TestDurableHTTPValidation(t *testing.T) {
	d, _ := fixture(t, nil)
	h := DurableHandler(d, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	for _, tc := range []struct {
		path, body string
		status     int
	}{{"receive", "", 410}, {"leases", `{"wait_ms":-1}`, 400}, {"leases", `{"wait_ms":9223372036854775807}`, 400}, {"leases", `{"unknown":1}`, 400}, {"acks", `{"event_id":"a","token":"short"}`, 400}, {"messages", `{"events":[]}`, 400}, {"nacks", `{}`, 400}, {"leases", `{} {}`, 400}, {"messages", strings.Repeat(" ", MaxRequestBytes+1), 413}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/internal/v1/"+tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}

func TestACKAndNACKRaceIsAtomic(t *testing.T) {
	d, _ := fixture(t, func(o *DurableOptions) { o.LeaseDuration = time.Hour })
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("race-%d", i)
		publish(t, d, id)
		e := lease(t, d)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; _, err := d.Ack(bg, AckRequest{id, e.Token}); results <- err }()
		go func() {
			<-start
			_, err := d.Nack(bg, NackRequest{EventID: id, Token: e.Token, Permanent: true, Reason: "bad"})
			results <- err
		}()
		close(start)
		a, b := <-results, <-results
		if (a == nil) == (b == nil) {
			t.Fatalf("exactly one transition should succeed: %v %v", a, b)
		}
		if a != nil && !errors.Is(a, ErrStaleLease) {
			t.Fatal(a)
		}
		if b != nil && !errors.Is(b, ErrStaleLease) {
			t.Fatal(b)
		}
	}
	s, _ := d.Snapshot(bg)
	if s.Completed+s.Quarantined != 20 || s.Ready != 0 || s.Leased != 0 {
		t.Fatal(s)
	}
}
func TestLeaseAndNACKStorageRollback(t *testing.T) {
	d, _ := fixture(t, nil)
	publish(t, d, "a")
	f := &failingDB{database: d.db}
	d.db = f
	f.fail.Store(true)
	if e, ok, err := d.Lease(bg, 0); err == nil || ok || e.Token != "" {
		t.Fatal(e, ok, err)
	}
	s, _ := d.Snapshot(bg)
	if s.Ready != 1 || s.Leased != 0 || s.Deliveries != 0 {
		t.Fatal(s)
	}
	f.fail.Store(false)
	e := lease(t, d)
	f.fail.Store(true)
	if _, err := d.Nack(bg, NackRequest{EventID: "a", Token: e.Token, Permanent: true, Reason: "bad"}); err == nil {
		t.Fatal("false NACK")
	}
	s, _ = d.Snapshot(bg)
	if s.Quarantined != 0 || s.Leased != 1 {
		t.Fatal(s)
	}
	f.fail.Store(false)
	ack(t, d, e)
}
func TestBackgroundCleanupAndPendingCancellation(t *testing.T) {
	d, c := fixture(t, func(o *DurableOptions) { o.CleanupInterval = 5 * time.Millisecond })
	publish(t, d, "a")
	e := lease(t, d)
	ack(t, d, e)
	c.add(time.Minute)
	deadline := time.Now().Add(time.Second)
	for {
		s, err := d.Snapshot(bg)
		if err != nil {
			t.Fatal(err)
		}
		if s.Completed == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not clean", s)
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { _, _, err := d.Lease(ctx, time.Second); done <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("poll ignored cancellation")
	}
}
