package main

import (
	"context"
	"errors"
	"flag"
	"gpu-telemetry/internal/api"
	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/observability"
	"gpu-telemetry/internal/storage/postgres"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func run() error {
	c, err := config.ParseAPI(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	log, err := observability.New(os.Stderr, c.LogLevel)
	if err != nil {
		return err
	}
	lifetime, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := postgres.OpenReadOnly(lifetime, c.DatabaseURL, int32(c.PoolMax))
	if err != nil {
		return err
	}
	defer db.Close()
	handler, _ := api.New(lifetime, db, c, log)
	// Request contexts outlive the initial signal, allowing bounded graceful drain.
	requests, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: c.WriteTimeout, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return requests }}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	log.Info("API listening", "address", listener.Addr().String(), "pool_max", c.PoolMax, "max_concurrent", c.MaxConcurrent)
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-lifetime.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout)
	defer cancel()
	if err = server.Shutdown(shutdown); err != nil {
		cancelRequests()
		_ = server.Close()
	}
	served := <-done
	if !errors.Is(served, http.ErrServerClosed) {
		err = errors.Join(err, served)
	}
	log.Info("API stopped")
	return err
}
func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		log, _ := observability.New(os.Stderr, "info")
		log.Error("API failed; verify configuration and service availability")
		os.Exit(1)
	}
}
