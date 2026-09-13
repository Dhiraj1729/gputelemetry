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
	"gpu-telemetry/internal/streamer"
)

func run() error {
	c, err := config.ParseStreamer(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	logger, err := observability.New(os.Stderr, c.LogLevel)
	if err != nil {
		return err
	}
	source, err := streamer.OpenCSV(c.CSV)
	if err != nil {
		return err
	}
	client, err := queueclient.New(c.QueueURL, c.HTTPTimeout)
	if err != nil {
		source.Close()
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("streamer starting", "rate", c.Rate, "count", c.Count, "csv", c.CSV)
	_, err = (streamer.Runner{Options: streamer.Options{Rate: c.Rate, Count: c.Count, ShutdownTimeout: c.ShutdownTimeout, RetryMin: c.RetryMin, RetryMax: c.RetryMax}, Logger: logger}).Run(ctx, source, client)
	return err
}
func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		logger, _ := observability.New(os.Stderr, "info")
		logger.Error("application failed", "error", err)
		os.Exit(1)
	}
}
