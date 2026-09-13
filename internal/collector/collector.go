// Package collector owns bounded delivery processing and commit-before-ACK.
package collector

import (
	"context"
	"errors"
	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/storage/postgres"
	"gpu-telemetry/internal/telemetry"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

type Queue interface {
	LeaseForProcessing(context.Context, time.Duration) (queue.Delivery, bool, error)
	Ack(context.Context, queue.AckRequest) error
	Nack(context.Context, queue.NackRequest) error
}
type Sink interface {
	Persist(context.Context, telemetry.Event) error
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func backoff(d, max time.Duration) time.Duration {
	if d >= max/2 {
		return max
	}
	return d * 2
}
func pause(ctx context.Context, d time.Duration) error {
	return wait(ctx, d/2+time.Duration(rand.Int64N(int64(d/2)+1)))
}

// Process handles one already leased batch of size one. All retries share its
// deadline/token. There is no ACK path before a successful database commit.
func Process(ctx context.Context, c config.Collector, q Queue, s Sink, d queue.Delivery, log *slog.Logger) error {
	deadline := time.Now().Add(c.WorkTimeout)
	if expiry := d.ExpiresAt.Add(-250 * time.Millisecond); expiry.Before(deadline) {
		deadline = expiry
	}
	work, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := postgres.Validate(d.Event); err != nil {
		reason := "invalid event: " + err.Error()
		if len(reason) > 256 {
			reason = reason[:256]
		}
		req := queue.NackRequest{EventID: d.Event.EventID, Token: d.Token, Permanent: true, Reason: reason}
		return retry(work, c, func() error { return q.Nack(work, req) }, true)
	}
	if err := retry(work, c, func() error { return s.Persist(work, d.Event) }, false); err != nil {
		return err
	}
	log.Info("telemetry committed", "event_id", d.Event.EventID, "attempt", d.Attempt)
	req := queue.AckRequest{EventID: d.Event.EventID, Token: d.Token}
	return retry(work, c, func() error { return q.Ack(work, req) }, true)
}
func retry(ctx context.Context, c config.Collector, op func() error, receipt bool) error {
	delay := c.RetryMin
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op()
		if err == nil {
			return nil
		}
		if receipt && !queueclient.Retryable(err) {
			return err
		}
		if err = pause(ctx, delay); err != nil {
			return err
		}
		delay = backoff(delay, c.RetryMax)
	}
}

// Run cancels lease acquisition immediately, while existing work drains under a
// single shutdown deadline. Fixed workers bound goroutines and in-flight events.
func Run(ctx context.Context, c config.Collector, q Queue, s Sink, log *slog.Logger) error {
	fetch, stop := context.WithCancel(ctx)
	defer stop()
	processing, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-fetch.Done():
			t := time.NewTimer(c.ShutdownTimeout)
			defer t.Stop()
			select {
			case <-t.C:
				cancel()
			case <-done:
			}
		case <-done:
		}
	}()
	var wg sync.WaitGroup
	failures := make(chan error, c.Workers)
	for i := 0; i < c.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			delay := c.RetryMin
			for fetch.Err() == nil {
				d, ok, err := q.LeaseForProcessing(fetch, c.LeaseWait)
				if err != nil {
					if fetch.Err() != nil {
						return
					}
					if !queueclient.Retryable(err) {
						failures <- err
						stop()
						return
					}
					if pause(fetch, delay) != nil {
						return
					}
					delay = backoff(delay, c.RetryMax)
					continue
				}
				if !ok {
					if pause(fetch, c.RetryMin) != nil {
						return
					}
					continue
				}
				// A lease obtained concurrently with shutdown is left for expiry.
				if fetch.Err() != nil {
					return
				}
				if err = Process(processing, c, q, s, d, log); err != nil {
					log.Warn("delivery unconfirmed; retained for redelivery", "event_id", d.Event.EventID, "error", safeError(err))
					if pause(fetch, delay) != nil {
						return
					}
					delay = backoff(delay, c.RetryMax)
				} else {
					delay = c.RetryMin
				}
			}
		}()
	}
	wg.Wait()
	close(failures)
	var result error
	for err := range failures {
		result = errors.Join(result, err)
	}
	return result
}

// Avoid logging driver errors that may include connection details or payloads.
func safeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "processing deadline exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "processing canceled"
	}
	return "operation failed"
}
