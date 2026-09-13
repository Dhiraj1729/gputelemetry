// testconsumer is a diagnostic client, not the production collector.
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/observability"
	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/testconsumer"
)

func run() error {
	c, err := config.ParseConsumer(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	logger, err := observability.New(os.Stderr, c.LogLevel)
	if err != nil {
		return err
	}
	client, err := queueclient.New(c.QueueURL, c.HTTPTimeout)
	if err != nil {
		return err
	}
	defer client.Close()
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signals, c.Timeout)
	defer cancel()
	_, err = testconsumer.Run(ctx, c, client, os.Stdout, logger)
	if errors.Is(err, context.Canceled) && signals.Err() != nil {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		logger, _ := observability.New(os.Stderr, "info")
		logger.Error("application failed", "error", err)
		os.Exit(1)
	}
}
