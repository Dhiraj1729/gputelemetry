package queue

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gpu-telemetry/internal/telemetry"
)

func event(id string) telemetry.Event {
	return telemetry.Event{SchemaVersion: 1, EventID: id, ProducerInstanceID: "p", RowIndex: 1, ProcessedAt: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC), Measurement: telemetry.Measurement{GPUUUID: "GPU-1", MetricName: "temperature", Value: 42, SourceTimestamp: time.Date(2025, 7, 18, 0, 0, 0, 0, time.UTC)}}
}
func TestCapacityDedupAndFIFO(t *testing.T) {
	q, _ := NewMemory(2, 3, time.Minute)
	for _, id := range []string{"a", "b"} {
		if dup, err := q.Publish(event(id)); dup || err != nil {
			t.Fatal(dup, err)
		}
	}
	if _, err := q.Publish(event("c")); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	if dup, err := q.Publish(event("a")); !dup || err != nil {
		t.Fatal(dup, err)
	}
	e := event("a")
	e.Value++
	if _, err := q.Publish(e); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if e, ok := q.Receive(); !ok || e.EventID != "a" {
		t.Fatal(e, ok)
	}
	if dup, err := q.Publish(event("a")); !dup || err != nil {
		t.Fatal(dup, err)
	}
	if _, err := q.Publish(event("c")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "c"} {
		if e, ok := q.Receive(); !ok || e.EventID != id {
			t.Fatal(e, ok)
		}
	}
	if _, ok := q.Receive(); ok {
		t.Fatal("not empty")
	}
	if _, err := q.Publish(event("d")); !errors.Is(err, ErrDedupFull) {
		t.Fatal(err)
	}
	if q.Stats().Accepted != 3 || q.Stats().Duplicates != 2 {
		t.Fatal(q.Stats())
	}
}
func TestDedupExpiresOnlyAfterReceive(t *testing.T) {
	q, _ := NewMemory(1, 1, time.Second)
	now := time.Now()
	q.now = func() time.Time { return now }
	q.Publish(event("a"))
	now = now.Add(time.Hour)
	if duplicate, err := q.Publish(event("a")); !duplicate || err != nil {
		t.Fatal(duplicate, err)
	}
	q.Receive()
	now = now.Add(time.Second)
	if duplicate, err := q.Publish(event("a")); duplicate || err != nil {
		t.Fatal(duplicate, err)
	}
}
func TestConcurrentPublishAndReceive(t *testing.T) {
	q, _ := NewMemory(200, 200, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := event(fmt.Sprint(i))
			if _, err := q.Publish(e); err != nil {
				t.Error(err)
			}
			if _, err := q.Publish(e); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if q.Stats().Depth != 200 {
		t.Fatal(q.Stats())
	}
	ids := make(chan string, 200)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				e, ok := q.Receive()
				if !ok {
					return
				}
				ids <- e.EventID
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("duplicate receive", id)
		}
		seen[id] = true
	}
	if len(seen) != 200 {
		t.Fatal(len(seen))
	}
}
func TestInvalidMemoryConfig(t *testing.T) {
	for _, c := range [][3]int{{0, 1, 1}, {2, 1, 1}, {1, 1, 0}} {
		if _, err := NewMemory(c[0], c[1], time.Duration(c[2])); err == nil {
			t.Fatal(c)
		}
	}
}
