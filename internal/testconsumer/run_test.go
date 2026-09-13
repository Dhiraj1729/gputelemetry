package testconsumer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/telemetry"
)

type stub struct {
	acks, nacks int
	ack         func(context.Context, queue.AckRequest) error
}

func (s *stub) Receive(context.Context) (telemetry.Event, bool, error) {
	return telemetry.Event{}, false, nil
}
func (s *stub) Lease(context.Context, time.Duration) (queue.Delivery, bool, error) {
	return queue.Delivery{Event: telemetry.Event{EventID: "a"}, Token: "token"}, true, nil
}
func (s *stub) Ack(c context.Context, a queue.AckRequest) error {
	s.acks++
	if s.ack != nil {
		return s.ack(c, a)
	}
	return nil
}
func (s *stub) Nack(context.Context, queue.NackRequest) error { s.nacks++; return nil }

type badWriter struct{}

func (badWriter) Write([]byte) (int, error) { return 0, errors.New("output failed") }
func TestOutputBeforeACKAndActions(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	c, _ := config.ParseConsumer([]string{"--count", "1"}, io.Discard)
	s := &stub{}
	if _, err := Run(context.Background(), c, s, badWriter{}, logger); err == nil || s.acks != 0 {
		t.Fatal(s, err)
	}
	for _, action := range []string{"ack", "noack", "retry", "quarantine"} {
		c.Action = action
		s = &stub{}
		n, err := Run(context.Background(), c, s, io.Discard, logger)
		if err != nil || n != 1 {
			t.Fatal(n, err)
		}
		if (action == "ack") != (s.acks == 1) {
			t.Fatal(action, s)
		}
		if (action == "retry" || action == "quarantine") != (s.nacks == 1) {
			t.Fatal(action, s)
		}
	}
}
func TestReceiptRetryStableAndBounded(t *testing.T) {
	c, _ := config.ParseConsumer([]string{"--count", "1", "--shutdown-timeout", "20ms"}, io.Discard)
	s := &stub{ack: func(c context.Context, a queue.AckRequest) error {
		if a.EventID != "a" || a.Token != "token" {
			t.Fatal(a)
		}
		return &queueclient.Error{Temporary: true}
	}}
	start := time.Now()
	_, err := Run(context.Background(), c, s, io.Discard, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err == nil || s.acks == 0 || time.Since(start) > time.Second {
		t.Fatal(err, s)
	}
}
