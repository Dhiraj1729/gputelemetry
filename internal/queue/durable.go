package queue

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"
	"gpu-telemetry/internal/telemetry"
)

var (
	ErrClosed     = errors.New("broker stopping or closed")
	ErrStaleLease = errors.New("unknown, expired or superseded delivery token")
	ErrStorage    = errors.New("durable storage operation failed")
	ErrInvalid    = errors.New("invalid request")
	errNoReady    = errors.New("no ready event")
)

const (
	activeOverhead    int64 = 4096 // Conservative logical allowance for bounded metadata/indexes.
	completedOverhead int64 = 1024
	MaxWait                 = time.Second * 10
	MaxNackDelay            = time.Hour
)

var records = []byte("records")
var ready = []byte("ready")
var leases = []byte("leases")
var completions = []byte("completions")
var metadata = []byte("metadata")

type DurableOptions struct {
	Path                                                         string
	Capacity, DedupCapacity, QuarantineCapacity, MaxPayloadBytes int
	MaxLogicalBytes                                              int64
	LeaseDuration, CompletionTTL, OpenTimeout, CleanupInterval   time.Duration
	CleanupBatch                                                 int
	Now                                                          func() time.Time
}

func DefaultDurableOptions(path string) DurableOptions {
	return DurableOptions{Path: path, Capacity: 1000, DedupCapacity: 10000, QuarantineCapacity: 100, MaxPayloadBytes: MaxRequestBytes - 1024, MaxLogicalBytes: 64 << 20, LeaseDuration: 30 * time.Second, CompletionTTL: time.Hour, OpenTimeout: time.Second, CleanupInterval: time.Second, CleanupBatch: 100}
}
func (o DurableOptions) Validate() error {
	if o.Path == "" || o.Capacity < 1 || o.Capacity > 100000 || o.DedupCapacity < o.Capacity || o.DedupCapacity > 1000000 || o.QuarantineCapacity < 1 || o.QuarantineCapacity > 100000 || o.MaxPayloadBytes < 1 || o.MaxPayloadBytes > MaxRequestBytes-1024 || o.MaxLogicalBytes < activeOverhead+int64(o.MaxPayloadBytes) || o.LeaseDuration < time.Millisecond || o.LeaseDuration > 24*time.Hour || o.CompletionTTL <= 0 || o.CompletionTTL > 365*24*time.Hour || o.OpenTimeout <= 0 || o.CleanupInterval <= 0 || o.CleanupBatch < 1 || o.CleanupBatch > 1000 {
		return fmt.Errorf("%w: invalid durable capacity, path, size or duration limits", ErrInvalid)
	}
	return nil
}

type diskRecord struct {
	Event         json.RawMessage `json:"event,omitempty"`
	Hash          [32]byte        `json:"hash"`
	State         string          `json:"state"`
	Sequence      uint64          `json:"sequence"`
	Attempt       uint64          `json:"attempt"`
	Due           int64           `json:"due"`
	Token         string          `json:"token,omitempty"`
	LastNackToken string          `json:"last_nack_token,omitempty"`
	LastNackHash  [32]byte        `json:"last_nack_hash"`
	Reason        string          `json:"reason,omitempty"`
}
type DurableStats struct {
	Ready           int    `json:"ready"` // Includes delayed retries; never includes leased events.
	Leased          int    `json:"leased"`
	Quarantined     int    `json:"quarantined"`
	Completed       int    `json:"completed"`
	LogicalBytes    int64  `json:"logical_bytes"`
	Accepted        uint64 `json:"accepted"`
	Acked           uint64 `json:"acked"`
	Deliveries      uint64 `json:"deliveries"`
	Redeliveries    uint64 `json:"redeliveries"`
	FileBytes       int64  `json:"file_bytes"`
	Capacity        int    `json:"capacity"`
	MaxLogicalBytes int64  `json:"max_logical_bytes"`
	CleanupFailures uint64 `json:"cleanup_failures"`
}

// Database is deliberately narrow and permits deterministic rollback/failure tests.
type database interface {
	Update(func(*bolt.Tx) error) error
	View(func(*bolt.Tx) error) error
	Close() error
}
type Durable struct {
	db                  database
	opts                DurableOptions
	gate                chan struct{}
	stopped             chan struct{}
	notify              chan struct{}
	stopOnce, closeOnce sync.Once
	worker              sync.WaitGroup
	closeErr            error
	cleanupFailures     atomic.Uint64
}

func OpenDurable(o DurableOptions) (*Durable, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(o.Path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(o.Path, 0600, &bolt.Options{Timeout: o.OpenTimeout})
	if err != nil {
		return nil, fmt.Errorf("open queue database %q (one writer only, timeout %s): %w", o.Path, o.OpenTimeout, err)
	}
	// NoSync remains false. Successful Update returns only after commit/sync.
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{records, ready, leases, completions, metadata} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		m := tx.Bucket(metadata)
		if version := m.Get([]byte("version")); version != nil && string(version) != "1" {
			return errors.New("unsupported queue database version")
		}
		if err := m.Put([]byte("version"), []byte("1")); err != nil {
			return err
		}
		if m.Get([]byte("stats")) == nil {
			return putJSON(m, []byte("stats"), DurableStats{})
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	d := &Durable{db: db, opts: o, gate: make(chan struct{}, 1), stopped: make(chan struct{}), notify: make(chan struct{}, 1)}
	d.worker.Add(1)
	go func() {
		defer d.worker.Done()
		t := time.NewTicker(o.CleanupInterval)
		defer t.Stop()
		for {
			select {
			case <-d.stopped:
				return
			case <-t.C:
				if err := d.Cleanup(context.Background()); err != nil && !errors.Is(err, ErrClosed) {
					d.cleanupFailures.Add(1)
				}
			}
		}
	}()
	return d, nil
}
func (d *Durable) Stop() { d.stopOnce.Do(func() { close(d.stopped) }) }
func (d *Durable) Close() error {
	d.closeOnce.Do(func() { d.Stop(); d.worker.Wait(); d.gate <- struct{}{}; d.closeErr = d.db.Close(); <-d.gate })
	return d.closeErr
}
func (d *Durable) enter(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.stopped:
		return ErrClosed
	case d.gate <- struct{}{}:
	}
	select {
	case <-ctx.Done():
		<-d.gate
		return ctx.Err()
	case <-d.stopped:
		<-d.gate
		return ErrClosed
	default:
		return nil
	}
}
func (d *Durable) signal() {
	select {
	case d.notify <- struct{}{}:
	default:
	}
}
func (d *Durable) view(ctx context.Context, f func(*bolt.Tx) error) error {
	if err := d.enter(ctx); err != nil {
		return err
	}
	defer func() { <-d.gate }()
	return d.db.View(f)
}
func (d *Durable) update(ctx context.Context, f func(*bolt.Tx, *DurableStats, int64) error) error {
	if err := d.enter(ctx); err != nil {
		return err
	}
	defer func() { <-d.gate }()
	err := d.db.Update(func(tx *bolt.Tx) error {
		var s DurableStats
		if err := json.Unmarshal(tx.Bucket(metadata).Get([]byte("stats")), &s); err != nil {
			return err
		}
		if err := f(tx, &s, d.opts.Now().UnixNano()); err != nil {
			return err
		}
		return putJSON(tx.Bucket(metadata), []byte("stats"), s)
	})
	if err == nil {
		d.signal()
	}
	return err
}
func putJSON(b *bolt.Bucket, key []byte, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put(key, data)
}
func load(tx *bolt.Tx, id string) (diskRecord, bool, error) {
	var r diskRecord
	b := tx.Bucket(records).Get([]byte(id))
	if b == nil {
		return r, false, nil
	}
	err := json.Unmarshal(b, &r)
	return r, true, err
}
func indexKey(due int64, seq uint64) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b, uint64(due))
	binary.BigEndian.PutUint64(b[8:], seq)
	return b
}
func dueFirst(b *bolt.Bucket, now int64) ([]byte, []byte) {
	k, v := b.Cursor().First()
	if k == nil || int64(binary.BigEndian.Uint64(k)) > now {
		return nil, nil
	}
	return k, v
}
func save(tx *bolt.Tx, id string, r diskRecord) error {
	return putJSON(tx.Bucket(records), []byte(id), r)
}

func (d *Durable) Publish(e telemetry.Event) (bool, error) {
	return d.PublishContext(context.Background(), e)
}
func (d *Durable) PublishContext(ctx context.Context, e telemetry.Event) (duplicate bool, err error) {
	if err = e.Validate(); err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(data) > d.opts.MaxPayloadBytes {
		return false, fmt.Errorf("%w: payload exceeds configured size limit", ErrInvalid)
	}
	hash := sha256.Sum256(data)
	err = d.update(ctx, func(tx *bolt.Tx, s *DurableStats, now int64) error {
		if err := d.expireCompleted(tx, s, now); err != nil {
			return err
		}
		old, ok, err := load(tx, e.EventID)
		if err != nil {
			return err
		}
		if ok && old.State == "completed" && old.Due <= now {
			if err := removeCompleted(tx, s, e.EventID, old); err != nil {
				return err
			}
			ok = false
		}
		if ok {
			if old.Hash != hash {
				return ErrConflict
			}
			duplicate = true
			return nil
		}
		if s.Ready+s.Leased+s.Quarantined >= d.opts.Capacity || s.LogicalBytes+int64(len(data))+activeOverhead > d.opts.MaxLogicalBytes {
			return ErrFull
		}
		// Reserve one completion slot for every admitted event so ACK cannot block on metadata capacity.
		if s.Ready+s.Leased+s.Quarantined+s.Completed >= d.opts.DedupCapacity {
			return ErrDedupFull
		}
		seq, err := tx.Bucket(records).NextSequence()
		if err != nil {
			return err
		}
		r := diskRecord{Event: data, Hash: hash, State: "ready", Sequence: seq, Due: now}
		if err = save(tx, e.EventID, r); err != nil {
			return err
		}
		if err = tx.Bucket(ready).Put(indexKey(now, seq), []byte(e.EventID)); err != nil {
			return err
		}
		s.Ready++
		s.Accepted++
		s.LogicalBytes += int64(len(data)) + activeOverhead
		return nil
	})
	if err != nil {
		return false, err
	}
	return duplicate, nil
}

func (d *Durable) Lease(ctx context.Context, wait time.Duration) (Delivery, bool, error) {
	if wait < 0 || wait > MaxWait {
		return Delivery{}, false, fmt.Errorf("%w: wait must be between 0 and 10 seconds", ErrInvalid)
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		if ctx.Err() != nil {
			return Delivery{}, false, ctx.Err()
		}
		var due bool
		err := d.view(ctx, func(tx *bolt.Tx) error {
			now := d.opts.Now().UnixNano()
			a, _ := dueFirst(tx.Bucket(ready), now)
			b, _ := dueFirst(tx.Bucket(leases), now)
			due = a != nil || b != nil
			return nil
		})
		if err != nil {
			return Delivery{}, false, err
		}
		if due {
			delivery, ok, err := d.tryLease(ctx)
			if ok || err != nil {
				return delivery, ok, err
			}
		}
		if wait == 0 {
			return Delivery{}, false, nil
		}
		// A bounded timer also wakes delayed retries and expired leases without holding a transaction.
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Delivery{}, false, ctx.Err()
		case <-d.stopped:
			timer.Stop()
			return Delivery{}, false, ErrClosed
		case <-deadline.C:
			timer.Stop()
			return Delivery{}, false, nil
		case <-d.notify:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (d *Durable) tryLease(ctx context.Context) (result Delivery, ok bool, err error) {
	err = d.update(ctx, func(tx *bolt.Tx, s *DurableStats, now int64) error {
		if err := d.expireLeases(tx, s, now); err != nil {
			return err
		}
		k, v := dueFirst(tx.Bucket(ready), now)
		if k == nil {
			return errNoReady
		}
		id := string(v)
		r, _, err := load(tx, id)
		if err != nil {
			return err
		}
		token, err := telemetry.NewID()
		if err != nil {
			return err
		}
		if err = tx.Bucket(ready).Delete(k); err != nil {
			return err
		}
		r.State = "leased"
		r.Token = token
		r.Due = now + int64(d.opts.LeaseDuration)
		r.Attempt++
		r.LastNackToken = ""
		r.LastNackHash = [32]byte{}
		r.Reason = ""
		if err = save(tx, id, r); err != nil {
			return err
		}
		if err = tx.Bucket(leases).Put(indexKey(r.Due, r.Sequence), []byte(id)); err != nil {
			return err
		}
		s.Ready--
		s.Leased++
		s.Deliveries++
		if r.Attempt > 1 {
			s.Redeliveries++
		}
		if err = json.Unmarshal(r.Event, &result.Event); err != nil {
			return err
		}
		result.Token = token
		result.ExpiresAt = time.Unix(0, r.Due).UTC()
		result.Attempt = r.Attempt
		return nil
	})
	if errors.Is(err, errNoReady) {
		return Delivery{}, false, nil
	}
	if err != nil {
		return Delivery{}, false, err
	}
	return result, true, nil
}
func validateReceipt(id, token string) error {
	if id == "" || len(id) > 128 || len(token) != 32 {
		return fmt.Errorf("%w: event_id (1..128 bytes) and 32-character token required", ErrInvalid)
	}
	return nil
}
func (d *Durable) Ack(ctx context.Context, a AckRequest) (duplicate bool, err error) {
	if err = validateReceipt(a.EventID, a.Token); err != nil {
		return false, err
	}
	err = d.update(ctx, func(tx *bolt.Tx, s *DurableStats, now int64) error {
		r, ok, err := load(tx, a.EventID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrStaleLease
		}
		if r.State == "completed" && r.Token == a.Token && r.Due > now {
			duplicate = true
			return nil
		}
		if r.State != "leased" || r.Token != a.Token || r.Due <= now {
			return ErrStaleLease
		}
		if err = tx.Bucket(leases).Delete(indexKey(r.Due, r.Sequence)); err != nil {
			return err
		}
		s.LogicalBytes -= int64(len(r.Event)) + activeOverhead - completedOverhead
		r.Event = nil
		r.State = "completed"
		r.Due = now + int64(d.opts.CompletionTTL)
		if err = save(tx, a.EventID, r); err != nil {
			return err
		}
		if err = tx.Bucket(completions).Put(indexKey(r.Due, r.Sequence), []byte(a.EventID)); err != nil {
			return err
		}
		s.Leased--
		s.Completed++
		s.Acked++
		return nil
	})
	if err != nil {
		return false, err
	}
	return duplicate, nil
}
func (d *Durable) Nack(ctx context.Context, n NackRequest) (duplicate bool, err error) {
	if err = validateReceipt(n.EventID, n.Token); err != nil {
		return false, err
	}
	if n.DelayMillis < 0 || n.DelayMillis > MaxNackDelay.Milliseconds() || len(n.Reason) > 256 || n.Reason == "" || (n.Permanent && n.DelayMillis != 0) {
		return false, fmt.Errorf("%w: reason (1..256 bytes), delay 0..3600000ms; permanent NACK requires zero delay", ErrInvalid)
	}
	body, _ := json.Marshal(n)
	hash := sha256.Sum256(body)
	err = d.update(ctx, func(tx *bolt.Tx, s *DurableStats, now int64) error {
		r, ok, err := load(tx, n.EventID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrStaleLease
		}
		if (r.State == "ready" || r.State == "quarantined") && r.LastNackToken == n.Token {
			if r.LastNackHash != hash {
				return ErrConflict
			}
			duplicate = true
			return nil
		}
		if r.State != "leased" || r.Token != n.Token || r.Due <= now {
			return ErrStaleLease
		}
		if n.Permanent && s.Quarantined >= d.opts.QuarantineCapacity {
			return ErrFull
		}
		if err = tx.Bucket(leases).Delete(indexKey(r.Due, r.Sequence)); err != nil {
			return err
		}
		r.LastNackToken = n.Token
		r.LastNackHash = hash
		r.Token = ""
		r.Reason = n.Reason
		s.Leased--
		if n.Permanent {
			r.State = "quarantined"
			r.Due = 0
			s.Quarantined++
		} else {
			r.State = "ready"
			r.Due = now + n.DelayMillis*int64(time.Millisecond)
			s.Ready++
			if err = tx.Bucket(ready).Put(indexKey(r.Due, r.Sequence), []byte(n.EventID)); err != nil {
				return err
			}
		}
		return save(tx, n.EventID, r)
	})
	if err != nil {
		return false, err
	}
	return duplicate, nil
}
func (d *Durable) expireLeases(tx *bolt.Tx, s *DurableStats, now int64) error {
	for i := 0; i < d.opts.CleanupBatch; i++ {
		k, v := dueFirst(tx.Bucket(leases), now)
		if k == nil {
			break
		}
		id := string(v)
		r, _, err := load(tx, id)
		if err != nil {
			return err
		}
		if err = tx.Bucket(leases).Delete(k); err != nil {
			return err
		}
		r.State = "ready"
		r.Token = ""
		r.Due = now
		if err = save(tx, id, r); err != nil {
			return err
		}
		if err = tx.Bucket(ready).Put(indexKey(now, r.Sequence), []byte(id)); err != nil {
			return err
		}
		s.Leased--
		s.Ready++
	}
	return nil
}
func removeCompleted(tx *bolt.Tx, s *DurableStats, id string, r diskRecord) error {
	if err := tx.Bucket(completions).Delete(indexKey(r.Due, r.Sequence)); err != nil {
		return err
	}
	if err := tx.Bucket(records).Delete([]byte(id)); err != nil {
		return err
	}
	s.Completed--
	s.LogicalBytes -= completedOverhead
	return nil
}
func (d *Durable) expireCompleted(tx *bolt.Tx, s *DurableStats, now int64) error {
	for i := 0; i < d.opts.CleanupBatch; i++ {
		_, v := dueFirst(tx.Bucket(completions), now)
		if v == nil {
			break
		}
		id := string(v)
		r, _, err := load(tx, id)
		if err != nil {
			return err
		}
		if err = removeCompleted(tx, s, id, r); err != nil {
			return err
		}
	}
	return nil
}
func (d *Durable) Cleanup(ctx context.Context) error {
	// Read-only probe avoids fsync for idle periodic passes.
	var due bool
	if err := d.view(ctx, func(tx *bolt.Tx) error {
		now := d.opts.Now().UnixNano()
		a, _ := dueFirst(tx.Bucket(leases), now)
		b, _ := dueFirst(tx.Bucket(completions), now)
		due = a != nil || b != nil
		return nil
	}); err != nil {
		return err
	}
	if !due {
		return nil
	}
	return d.update(ctx, func(tx *bolt.Tx, s *DurableStats, now int64) error {
		if err := d.expireLeases(tx, s, now); err != nil {
			return err
		}
		return d.expireCompleted(tx, s, now)
	})
}
func (d *Durable) Snapshot(ctx context.Context) (s DurableStats, err error) {
	err = d.view(ctx, func(tx *bolt.Tx) error { return json.Unmarshal(tx.Bucket(metadata).Get([]byte("stats")), &s) })
	if err != nil {
		return s, err
	}
	info, err := os.Stat(d.opts.Path)
	if err != nil {
		return s, err
	}
	s.FileBytes = info.Size()
	s.Capacity = d.opts.Capacity
	s.MaxLogicalBytes = d.opts.MaxLogicalBytes
	s.CleanupFailures = d.cleanupFailures.Load()
	return s, nil
}
