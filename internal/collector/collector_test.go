package collector

import (
	"context"
	"errors"
	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/telemetry"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakeQ struct {
	ack   func(context.Context, queue.AckRequest) error
	nack  func(context.Context, queue.NackRequest) error
	lease func(context.Context, time.Duration) (queue.Delivery, bool, error)
}

func (q fakeQ) Ack(c context.Context, a queue.AckRequest) error {
	if q.ack != nil {
		return q.ack(c, a)
	}
	return nil
}
func (q fakeQ) Nack(c context.Context, a queue.NackRequest) error {
	if q.nack != nil {
		return q.nack(c, a)
	}
	return nil
}
func (q fakeQ) LeaseForProcessing(c context.Context, w time.Duration) (queue.Delivery, bool, error) {
	return q.lease(c, w)
}

type sinkFunc func(context.Context, telemetry.Event) error

func (f sinkFunc) Persist(c context.Context, e telemetry.Event) error { return f(c, e) }
func fixture() (config.Collector, queue.Delivery, *slog.Logger) {
	c := config.Collector{Workers: 2, WorkTimeout: 100 * time.Millisecond, ShutdownTimeout: 50 * time.Millisecond, RetryMin: time.Millisecond, RetryMax: 5 * time.Millisecond}
	e := telemetry.Event{SchemaVersion: 1, EventID: "test", ProducerInstanceID: "p", RowIndex: 1, ProcessedAt: time.Now().UTC().Truncate(time.Microsecond), Measurement: telemetry.Measurement{GPUUUID: "GPU-1", MetricName: "power", Value: 4, SourceTimestamp: time.Now().UTC()}}
	return c, queue.Delivery{Event: e, Token: "01234567890123456789012345678901", ExpiresAt: time.Now().Add(time.Second), Attempt: 1}, slog.New(slog.NewJSONHandler(io.Discard, nil))
}
func TestCommitBeforeAckAndReceiptRetry(t *testing.T) {
	c, d, l := fixture()
	committed := false
	acks := 0
	writes := 0
	q := fakeQ{ack: func(_ context.Context, a queue.AckRequest) error {
		acks++
		if !committed || a.Token != d.Token || a.EventID != d.Event.EventID {
			t.Fatal("ACK before commit or changed identity")
		}
		if acks == 1 {
			return &queueclient.Error{Temporary: true}
		}
		return nil
	}}
	err := Process(context.Background(), c, q, sinkFunc(func(context.Context, telemetry.Event) error { writes++; committed = true; return nil }), d, l)
	if err != nil || writes != 1 || acks != 2 {
		t.Fatalf("err=%v writes=%d acks=%d", err, writes, acks)
	}
}
func TestDatabaseFailureNeverAck(t *testing.T) {
	c, d, l := fixture()
	acks := 0
	writes := 0
	q := fakeQ{ack: func(context.Context, queue.AckRequest) error { acks++; return nil }}
	err := Process(context.Background(), c, q, sinkFunc(func(context.Context, telemetry.Event) error { writes++; return errors.New("database down") }), d, l)
	if !errors.Is(err, context.DeadlineExceeded) || acks != 0 || writes < 2 || writes > 150 {
		t.Fatalf("err=%v acks=%d writes=%d", err, acks, writes)
	}
}
func TestTransientDatabaseRecovery(t *testing.T) {
	c, d, l := fixture()
	writes := 0
	err := Process(context.Background(), c, fakeQ{}, sinkFunc(func(context.Context, telemetry.Event) error {
		writes++
		if writes < 3 {
			return errors.New("unavailable")
		}
		return nil
	}), d, l)
	if err != nil || writes != 3 {
		t.Fatal(err, writes)
	}
}
func TestInvalidQuarantined(t *testing.T) {
	for _, invalid := range []string{"schema", "nul"} {
		t.Run(invalid, func(t *testing.T) {
			c, d, l := fixture()
			if invalid == "schema" {
				d.Event.SchemaVersion = 99
			} else {
				d.Event.LabelsRaw = "bad\x00label"
			}
			calls := 0
			q := fakeQ{ack: func(context.Context, queue.AckRequest) error { t.Fatal("invalid ACK"); return nil }, nack: func(_ context.Context, n queue.NackRequest) error {
				calls++
				if !n.Permanent || n.Reason == "" || n.Token != d.Token {
					t.Fatal(n)
				}
				return nil
			}}
			if err := Process(context.Background(), c, q, sinkFunc(func(context.Context, telemetry.Event) error { t.Fatal("invalid persisted"); return nil }), d, l); err != nil || calls != 1 {
				t.Fatal(err, calls)
			}
		})
	}
}
func TestExpiredAndCanceledDoNotWrite(t *testing.T) {
	c, d, l := fixture()
	d.ExpiresAt = time.Now()
	if err := Process(context.Background(), c, fakeQ{}, sinkFunc(func(context.Context, telemetry.Event) error { t.Fatal("expired write"); return nil }), d, l); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestStaleAckNotRetried(t *testing.T) {
	c, d, l := fixture()
	calls := 0
	q := fakeQ{ack: func(context.Context, queue.AckRequest) error { calls++; return &queueclient.Error{Status: 409} }}
	if err := Process(context.Background(), c, q, sinkFunc(func(context.Context, telemetry.Event) error { return nil }), d, l); err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
func TestShutdownDrainsBoundedWorkers(t *testing.T) {
	c, d, l := fixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var leases, active, max, acks atomic.Int32
	entered := make(chan struct{}, 2)
	q := fakeQ{lease: func(ctx context.Context, _ time.Duration) (queue.Delivery, bool, error) {
		leases.Add(1)
		return d, true, nil
	}, ack: func(context.Context, queue.AckRequest) error { acks.Add(1); return nil }}
	sink := sinkFunc(func(ctx context.Context, _ telemetry.Event) error {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := max.Load()
			if old >= n || max.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	done := make(chan error, 1)
	go func() { done <- Run(ctx, c, q, sink, l) }()
	<-entered
	<-entered
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown hung")
	}
	if max.Load() != 2 || leases.Load() != 2 || acks.Load() != 0 {
		t.Fatal(max.Load(), leases.Load(), acks.Load())
	}
}
func TestShutdownFinishesCommittedWork(t *testing.T) {
	c, d, l := fixture()
	c.Workers = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var acks atomic.Int32
	q := fakeQ{lease: func(context.Context, time.Duration) (queue.Delivery, bool, error) { return d, true, nil }, ack: func(context.Context, queue.AckRequest) error { acks.Add(1); return nil }}
	if err := Run(ctx, c, q, sinkFunc(func(context.Context, telemetry.Event) error { cancel(); return nil }), l); err != nil || acks.Load() != 1 {
		t.Fatal(err, acks.Load())
	}
}
func TestFatalAndTransientLeaseErrors(t *testing.T) {
	for _, temporary := range []bool{false, true} {
		t.Run(map[bool]string{false: "fatal", true: "retry"}[temporary], func(t *testing.T) {
			c, _, l := fixture()
			c.Workers = 1
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls int
			q := fakeQ{lease: func(context.Context, time.Duration) (queue.Delivery, bool, error) {
				calls++
				if calls == 3 {
					cancel()
				}
				return queue.Delivery{}, false, &queueclient.Error{Status: 503, Temporary: temporary}
			}}
			err := Run(ctx, c, q, nil, l)
			if temporary && (err != nil || calls != 3) || !temporary && (err == nil || calls != 1) {
				t.Fatal(err, calls)
			}
		})
	}
}
