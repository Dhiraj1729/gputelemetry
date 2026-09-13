package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/observability"
	"gpu-telemetry/internal/queue"
)

func run() (err error) {
	c, err := config.ParseQueue(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	logger, err := observability.New(os.Stderr, c.LogLevel)
	if err != nil {
		return err
	}
	var handler http.Handler
	var durable *queue.Durable
	var memory *queue.Memory
	if c.Mode == "durable" {
		durable, err = queue.OpenDurable(c.Durable)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, durable.Close()) }()
		handler = queue.DurableHandler(durable, logger)
	} else {
		memory, err = queue.NewMemory(c.Capacity, c.DedupCapacity, c.DedupTTL)
		if err != nil {
			return err
		}
		handler = queue.Handler(memory, logger)
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	logger.Info("queue listening", "address", listener.Addr().String(), "mode", c.Mode, "db", c.Durable.Path, "capacity", c.Capacity)
	select {
	case err = <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	if durable != nil {
		durable.Stop()
	} // Cancel long polls and stop new DB work before waiting on handlers.
	shutdown, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout)
	defer cancel()
	err = server.Shutdown(shutdown)
	if err != nil {
		_ = server.Close()
	}
	serveErr := <-finished
	if !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	if durable != nil {
		logger.Info("queue stopped", "mode", "durable", "db", c.Durable.Path, "data_retained", true)
	} else {
		logger.Info("queue stopped", "mode", "memory", "stats", memory.Stats(), "remaining_events_lost", memory.Stats().Depth)
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
