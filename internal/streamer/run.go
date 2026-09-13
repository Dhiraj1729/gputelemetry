package streamer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"time"

	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/telemetry"
)

type Source interface {
	Next(context.Context) (Row, error)
	Close() error
}
type Publisher interface {
	Publish(context.Context, telemetry.Event) error
	Close()
}
type Stats struct{ Accepted, InvalidRows, Retries, Unconfirmed uint64 }
type Options struct {
	Rate               float64
	Count              uint64
	ShutdownTimeout    time.Duration
	RetryMin, RetryMax time.Duration
	ProducerID         string
}
type Runner struct {
	Options Options
	Logger  *slog.Logger
	// Injectable scheduling removes real-time sleeps from rate and retry tests.
	Now    func() time.Time
	Wait   func(context.Context, time.Duration) error
	Jitter func(time.Duration) time.Duration
}

func Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run owns both resources, including on validation errors. The stop context
// stops generation; an independent context keeps one pending publish alive
// for at most ShutdownTimeout after stop. The shutdown watcher is always joined.
func (r Runner) Run(stop context.Context, source Source, pub Publisher) (stats Stats, err error) {
	defer pub.Close()
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	o := r.Options
	if o.Rate <= 0 || o.Rate > 1000000 || math.IsNaN(o.Rate) || math.IsInf(o.Rate, 0) || o.Rate < 0.001 || o.ShutdownTimeout <= 0 || o.RetryMin <= 0 || o.RetryMax < o.RetryMin || o.RetryMax > time.Minute {
		return stats, errors.New("invalid rate (0.001..1000000), shutdown timeout, or retry interval")
	}
	if o.ProducerID == "" {
		o.ProducerID, err = telemetry.NewID()
		if err != nil {
			return stats, err
		}
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Wait == nil {
		r.Wait = Wait
	}
	if r.Jitter == nil {
		r.Jitter = func(d time.Duration) time.Duration { return d/2 + time.Duration(rand.Int64N(int64(d/2)+1)) }
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	publishCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-done:
			return
		case <-stop.Done():
		}
		timer := time.NewTimer(o.ShutdownTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			cancel()
		}
	}()
	defer func() {
		close(done)
		cancel()
		<-joined
		r.Logger.Info("streamer stopped", "accepted", stats.Accepted, "invalid_rows", stats.InvalidRows, "retries", stats.Retries, "unconfirmed_pending", stats.Unconfirmed)
	}()
	interval := time.Duration(float64(time.Second) / o.Rate)
	var next time.Time
	for o.Count == 0 || stats.Accepted < o.Count {
		if stop.Err() != nil {
			return stats, nil
		}
		if delay := next.Sub(r.Now()); delay > 0 {
			if r.Wait(stop, delay) != nil {
				return stats, nil
			}
		}
		row, readErr := source.Next(stop)
		if readErr != nil {
			if stop.Err() != nil {
				return stats, nil
			}
			var invalid *InvalidRow
			if errors.As(readErr, &invalid) {
				stats.InvalidRows++
				r.Logger.Warn("invalid CSV row", "error", invalid)
				continue
			}
			return stats, readErr
		}
		if stop.Err() != nil {
			return stats, nil
		}
		e := telemetry.Event{SchemaVersion: telemetry.SchemaVersion, EventID: fmt.Sprintf("%s:%d:%d", o.ProducerID, row.ReplayIndex, row.RowIndex), ProducerInstanceID: o.ProducerID, ReplayIndex: row.ReplayIndex, RowIndex: row.RowIndex, ProcessedAt: r.Now().UTC().Truncate(time.Microsecond), Measurement: row.Measurement}
		if err = e.Validate(); err != nil {
			return stats, err
		}
		next = r.Now().Add(interval)
		backoff := o.RetryMin
		for {
			err = pub.Publish(publishCtx, e)
			if err == nil {
				stats.Accepted++
				r.Logger.Debug("event accepted", "event_id", e.EventID)
				break
			}
			if !queueclient.Retryable(err) || publishCtx.Err() != nil {
				stats.Unconfirmed = 1
				r.Logger.Error("pending event not confirmed", "event_id", e.EventID, "error", err)
				return stats, err
			}
			stats.Retries++
			r.Logger.Warn("publish retry", "event_id", e.EventID, "retry", stats.Retries, "error", err)
			if waitErr := r.Wait(publishCtx, r.Jitter(backoff)); waitErr != nil {
				stats.Unconfirmed = 1
				return stats, waitErr
			}
			if backoff < o.RetryMax {
				backoff *= 2
				if backoff > o.RetryMax {
					backoff = o.RetryMax
				}
			}
		}
	}
	return stats, nil
}
