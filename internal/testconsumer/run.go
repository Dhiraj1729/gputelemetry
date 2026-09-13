// Package testconsumer simulates processing. It is not a transactional storage collector.
package testconsumer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"time"

	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/streamer"
	"gpu-telemetry/internal/telemetry"
)

type Client interface {
	Receive(context.Context) (telemetry.Event, bool, error)
	Lease(context.Context, time.Duration) (queue.Delivery, bool, error)
	Ack(context.Context, queue.AckRequest) error
	Nack(context.Context, queue.NackRequest) error
}

func Run(ctx context.Context, c config.Consumer, client Client, out io.Writer, logger *slog.Logger) (received uint64, err error) {
	enc := json.NewEncoder(out)
	defer func() { logger.Info("consumer stopped", "received", received, "target", c.Count, "action", c.Action) }()
	for received < c.Count {
		if ctx.Err() != nil {
			return received, ctx.Err()
		}
		var event telemetry.Event
		var delivery queue.Delivery
		var ok bool
		if c.Mode == "memory" {
			event, ok, err = client.Receive(ctx)
		} else {
			delivery, ok, err = client.Lease(ctx, c.LeaseWait)
			event = delivery.Event
		}
		if err != nil && !queueclient.Retryable(err) {
			return received, err
		}
		if !ok {
			if err != nil {
				logger.Warn("receive retry", "error", err)
			}
			if err = streamer.Wait(ctx, 100*time.Millisecond); err != nil {
				return received, err
			}
			continue
		}
		if c.Mode == "durable" {
			logger.Info("delivery leased", "event_id", event.EventID, "token", delivery.Token, "expires_at", delivery.ExpiresAt, "attempt", delivery.Attempt)
		}
		if c.ProcessingDelay > 0 {
			if err = streamer.Wait(ctx, c.ProcessingDelay); err != nil {
				return received, err
			}
		}
		if err = enc.Encode(event); err != nil {
			return received, err
		} // Never ACK failed output.
		if c.Mode == "memory" {
			received++
			continue
		}
		if c.Action == "noack" {
			logger.Info("exiting without ACK", "event_id", event.EventID, "token", delivery.Token, "expires_at", delivery.ExpiresAt)
			return received + 1, nil
		}
		// A pending receipt gets its own bounded context, retaining identity across
		// retries even if generation/lease polling was canceled by SIGTERM.
		pending, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.ShutdownTimeout)
		ack := queue.AckRequest{EventID: event.EventID, Token: delivery.Token}
		nack := queue.NackRequest{EventID: event.EventID, Token: delivery.Token, Permanent: c.Action == "quarantine", Reason: c.Reason}
		if c.Action == "retry" {
			nack.DelayMillis = c.RetryDelay.Milliseconds()
		}
		for {
			if c.Action == "ack" {
				err = client.Ack(pending, ack)
			} else {
				err = client.Nack(pending, nack)
			}
			if err == nil || !queueclient.Retryable(err) {
				break
			}
			logger.Warn("receipt retry", "event_id", event.EventID, "error", err)
			if err = streamer.Wait(pending, 100*time.Millisecond); err != nil {
				break
			}
		}
		cancel()
		if err != nil {
			logger.Error("receipt unconfirmed; event may be redelivered", "event_id", event.EventID, "error", err)
			return received, err
		}
		received++
	}
	return received, nil
}
