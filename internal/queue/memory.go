// Package queue implements the volatile Day 1 queue. Receive is destructive.
package queue

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"gpu-telemetry/internal/telemetry"
)

var (
	ErrFull      = errors.New("queue capacity exhausted")
	ErrDedupFull = errors.New("deduplication capacity exhausted; wait for tombstone expiry")
	ErrConflict  = errors.New("event ID reused with different content")
)

type tombstone struct {
	id      string
	expires time.Time
}

type seenEntry struct {
	hash    [32]byte
	expires time.Time
}
type Stats struct {
	Depth        int    `json:"depth"`
	Capacity     int    `json:"capacity"`
	DedupEntries int    `json:"dedup_entries"`
	Accepted     uint64 `json:"accepted"`
	Duplicates   uint64 `json:"duplicates"`
	Received     uint64 `json:"received"`
}

// Memory uses a fixed ring and bounded dedup map. Never evicts unexpired IDs:
// admission backpressure preserves the promised dedup window after receive.
type Memory struct {
	expired                        list.List
	mu                             sync.Mutex
	ring                           []telemetry.Event
	head, size, dedupCapacity      int
	seen                           map[string]seenEntry
	ttl                            time.Duration
	now                            func() time.Time
	accepted, duplicates, received uint64
}

func NewMemory(capacity, dedupCapacity int, ttl time.Duration) (*Memory, error) {
	if capacity < 1 || dedupCapacity < capacity || ttl <= 0 {
		return nil, errors.New("capacity > 0, dedup capacity >= capacity, and positive TTL required")
	}
	return &Memory{ring: make([]telemetry.Event, capacity), seen: make(map[string]seenEntry), dedupCapacity: dedupCapacity, ttl: ttl, now: time.Now}, nil
}

func (q *Memory) Publish(e telemetry.Event) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	if len(b) > MaxRequestBytes-1024 {
		return false, errors.New("event too large")
	}
	hash := sha256.Sum256(b)
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	for q.expired.Len() > 0 {
		front := q.expired.Front()
		entry := front.Value.(tombstone)
		if now.Before(entry.expires) {
			break
		}
		delete(q.seen, entry.id)
		q.expired.Remove(front)
	}

	if entry, ok := q.seen[e.EventID]; ok {
		if entry.hash != hash {
			return false, ErrConflict
		}
		q.duplicates++
		return true, nil
	}
	if q.size == len(q.ring) {
		return false, ErrFull
	}
	if len(q.seen) == q.dedupCapacity {
		return false, ErrDedupFull
	}
	q.ring[(q.head+q.size)%len(q.ring)] = e
	q.size++
	q.seen[e.EventID] = seenEntry{hash: hash}
	q.accepted++
	return false, nil
}

func (q *Memory) Receive() (telemetry.Event, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.size == 0 {
		return telemetry.Event{}, false
	}
	e := q.ring[q.head]
	q.ring[q.head] = telemetry.Event{}
	q.head = (q.head + 1) % len(q.ring)
	q.size--
	q.received++
	entry := q.seen[e.EventID]
	entry.expires = q.now().Add(q.ttl)
	q.seen[e.EventID] = entry
	q.expired.PushBack(tombstone{e.EventID, entry.expires})
	return e, true
}

func (q *Memory) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return Stats{q.size, len(q.ring), len(q.seen), q.accepted, q.duplicates, q.received}
}
