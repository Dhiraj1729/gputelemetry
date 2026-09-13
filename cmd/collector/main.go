package main

import (
	"context"
	"errors"
	"flag"
	"gpu-telemetry/internal/collector"
	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/observability"
	"gpu-telemetry/internal/queueclient"
	"gpu-telemetry/internal/storage/postgres"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run() error {
	c, err := config.ParseCollector(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	log, err := observability.New(os.Stderr, c.LogLevel)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	q, err := queueclient.New(c.QueueURL, c.HTTPTimeout)
	if err != nil {
		return err
	}
	defer q.Close()
	s, err := postgres.Open(ctx, c.DatabaseURL, int32(c.PoolMax))
	if err != nil {
		return err
	}
	defer s.Close()
	mux := collector.HealthHandler(ctx, s)
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	served := make(chan error, 1)
	go func() { e := server.Serve(listener); served <- e; stop() }()
	log.Info("collector started", "listen", listener.Addr().String(), "workers", c.Workers, "batch_size", 1, "pool_max", c.PoolMax)
	err = collector.Run(ctx, c, q, s, log)
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := server.Shutdown(shutdown); e != nil {
		_ = server.Close()
		err = errors.Join(err, e)
	}
	if e := <-served; !errors.Is(e, http.ErrServerClosed) {
		err = errors.Join(err, e)
	}
	log.Info("collector stopped")
	return err
}
func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		log, _ := observability.New(os.Stderr, "info")
		log.Error("collector failed; verify configuration and service availability")
		os.Exit(1)
	}
}
