package streamer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/telemetry"
)

type fakePublisher struct {
	publish func(context.Context, telemetry.Event) error
	closed  bool
}

func (p *fakePublisher) Publish(c context.Context, e telemetry.Event) error { return p.publish(c, e) }
func (p *fakePublisher) Close()                                             { p.closed = true }
func options() Options {
	return Options{Rate: 10, Count: 2, ShutdownTimeout: 30 * time.Millisecond, RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond, ProducerID: "producer"}
}
func runner() Runner {
	return Runner{Options: options(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Jitter: func(d time.Duration) time.Duration { return d }}
}
func openSource(t *testing.T) *CSV {
	s, err := OpenCSV(csvFile(t, [][]string{headers, sampleRow()}))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func assertClosed(t *testing.T, s *CSV, p *fakePublisher) {
	t.Helper()
	if !p.closed {
		t.Fatal("publisher not closed")
	}
	if _, err := s.file.Stat(); err == nil {
		t.Fatal("CSV not closed")
	}
}
func TestRateIdentityAndRetryTimestamp(t *testing.T) {
	r := runner()
	now := time.Date(2026, 9, 12, 1, 2, 3, 456789123, time.FixedZone("offset", 3600))
	r.Now = func() time.Time { return now }
	var waits []time.Duration
	r.Wait = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		now = now.Add(d)
		return ctx.Err()
	}
	var events []telemetry.Event
	p := &fakePublisher{publish: func(ctx context.Context, e telemetry.Event) error {
		events = append(events, e)
		if len(events) == 1 {
			return &queueclient.Error{Temporary: true}
		}
		return nil
	}}
	s := openSource(t)
	stats, err := r.Run(context.Background(), s, p)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Accepted != 2 || stats.Retries != 1 || len(events) != 3 {
		t.Fatal(stats, events)
	}
	if !reflect.DeepEqual(events[0], events[1]) {
		t.Fatal("retry changed event")
	}
	if events[0].EventID == events[2].EventID || events[2].ReplayIndex != 1 || events[0].ProcessedAt.Nanosecond()%1000 != 0 || events[0].ProcessedAt.Location() != time.UTC {
		t.Fatal(events)
	}
	if events[2].ProcessedAt.Sub(events[0].ProcessedAt) < 100*time.Millisecond {
		t.Fatal("rate exceeded")
	}
	if len(waits) != 2 {
		t.Fatal(waits)
	}
	assertClosed(t, s, p)
}
func TestShutdownIdle(t *testing.T) {
	r := runner()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	r.Wait = func(c context.Context, d time.Duration) error { stop(); return c.Err() }
	p := &fakePublisher{publish: func(context.Context, telemetry.Event) error { return nil }}
	s := openSource(t)
	stats, err := r.Run(ctx, s, p)
	if err != nil || stats.Accepted != 1 {
		t.Fatal(stats, err)
	}
	assertClosed(t, s, p)
}
func TestShutdownPendingFinishes(t *testing.T) {
	r := runner()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	p := &fakePublisher{publish: func(c context.Context, e telemetry.Event) error {
		stop()
		if c.Err() != nil {
			t.Fatal("pending canceled with generation")
		}
		return nil
	}}
	s := openSource(t)
	stats, err := r.Run(ctx, s, p)
	if err != nil || stats.Accepted != 1 || stats.Unconfirmed != 0 {
		t.Fatal(stats, err)
	}
	assertClosed(t, s, p)
}
func TestShutdownInFlightDeadline(t *testing.T) {
	r := runner()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	p := &fakePublisher{publish: func(c context.Context, e telemetry.Event) error { stop(); <-c.Done(); return c.Err() }}
	s := openSource(t)
	start := time.Now()
	stats, err := r.Run(ctx, s, p)
	if !errors.Is(err, context.Canceled) || stats.Unconfirmed != 1 {
		t.Fatal(stats, err)
	}
	if elapsed := time.Since(start); elapsed < r.Options.ShutdownTimeout || elapsed > time.Second {
		t.Fatal(elapsed)
	}
	assertClosed(t, s, p)
}
func TestShutdownUnavailableQueue(t *testing.T) {
	r := runner()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var pending telemetry.Event
	p := &fakePublisher{publish: func(c context.Context, e telemetry.Event) error {
		if pending.EventID != "" && !reflect.DeepEqual(e, pending) {
			t.Error("changed pending event")
		}
		pending = e
		stop()
		return &queueclient.Error{Temporary: true}
	}}
	s := openSource(t)
	stats, err := r.Run(ctx, s, p)
	if err == nil || stats.Unconfirmed != 1 || stats.Retries < 1 || stats.Accepted != 0 {
		t.Fatal(stats, err)
	}
	assertClosed(t, s, p)
}
func TestPermanentRejectionAndInvalidRows(t *testing.T) {
	r := runner()
	p := &fakePublisher{publish: func(context.Context, telemetry.Event) error { return &queueclient.Error{Status: 400} }}
	s, err := OpenCSV(csvFile(t, [][]string{headers, {"bad"}, sampleRow()}))
	if err != nil {
		t.Fatal(err)
	}
	stats, err := r.Run(context.Background(), s, p)
	if err == nil || stats.InvalidRows != 1 || stats.Retries != 0 || stats.Unconfirmed != 1 {
		t.Fatal(stats, err)
	}
	assertClosed(t, s, p)
}
func TestAlreadyStoppedAndInvalidOptionsCleanup(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		r := runner()
		if invalid {
			r.Options.Rate = 0
		}
		ctx, stop := context.WithCancel(context.Background())
		stop()
		p := &fakePublisher{publish: func(context.Context, telemetry.Event) error { t.Fatal("unexpected publish"); return nil }}
		s := openSource(t)
		_, err := r.Run(ctx, s, p)
		if invalid != (err != nil) {
			t.Fatal(err)
		}
		assertClosed(t, s, p)
	}
}

func TestFreshProducerIdentityPerRun(t *testing.T) {
	var ids []string
	for i := 0; i < 2; i++ {
		r := runner()
		r.Options.Count = 1
		r.Options.ProducerID = ""
		p := &fakePublisher{publish: func(c context.Context, e telemetry.Event) error { ids = append(ids, e.ProducerInstanceID); return nil }}
		if _, err := r.Run(context.Background(), openSource(t), p); err != nil {
			t.Fatal(err)
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("reused producer identity")
	}
}
