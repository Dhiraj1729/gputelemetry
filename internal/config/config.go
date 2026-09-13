// Package config uses explicit flags for a small, reproducible Day 1 configuration surface.
package config

import (
	"flag"
	"fmt"
	"gpu-telemetry/internal/queue"
	"io"
	"math"
	"net"
	"time"
)

const DefaultCSV = "Problemstatement/dcgm_metrics_20250718_134233.csv"

type Common struct {
	LogLevel        string
	ShutdownTimeout time.Duration
}
type Streamer struct {
	Common
	CSV, QueueURL                   string
	Rate                            float64
	Count                           uint64
	HTTPTimeout, RetryMin, RetryMax time.Duration
}
type Queue struct {
	Mode    string
	Durable queue.DurableOptions
	Common
	Listen                  string
	Capacity, DedupCapacity int
	DedupTTL                time.Duration
}
type Consumer struct {
	ProcessingDelay                        time.Duration
	Mode, Action, Reason                   string
	LeaseWait, RetryDelay, ShutdownTimeout time.Duration
	LogLevel                               string
	QueueURL                               string
	Count                                  uint64
	Timeout, HTTPTimeout                   time.Duration
}

func common(f *flag.FlagSet, c *Common) {
	f.StringVar(&c.LogLevel, "log-level", "info", "debug, info, warn, error")
	f.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", 20*time.Second, "maximum time to finish pending work after a signal")
}
func flags(name string, out io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(out)
	return f
}
func valid(f *flag.FlagSet, c Common) error {
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown-timeout must be positive")
	}
	return nil
}
func ParseStreamer(args []string, out io.Writer) (c Streamer, err error) {
	f := flags("streamer", out)
	common(f, &c.Common)
	f.StringVar(&c.CSV, "csv", DefaultCSV, "input CSV path")
	f.StringVar(&c.QueueURL, "queue-url", "http://127.0.0.1:8081", "queue origin")
	f.Float64Var(&c.Rate, "rate", 10, "maximum generated events per second")
	f.Uint64Var(&c.Count, "count", 0, "accepted event target, zero loops until signal")
	f.DurationVar(&c.HTTPTimeout, "http-timeout", 3*time.Second, "per-request timeout")
	f.DurationVar(&c.RetryMin, "retry-min", 100*time.Millisecond, "initial retry backoff")
	f.DurationVar(&c.RetryMax, "retry-max", 2*time.Second, "maximum retry backoff")
	if err = f.Parse(args); err != nil {
		return
	}
	err = valid(f, c.Common)
	if err != nil {
		return
	}
	if c.CSV == "" || c.Rate < 0.001 || c.Rate > 1000000 || math.IsNaN(c.Rate) || math.IsInf(c.Rate, 0) || c.HTTPTimeout <= 0 || c.RetryMin <= 0 || c.RetryMax < c.RetryMin || c.RetryMax > time.Minute {
		err = fmt.Errorf("invalid CSV, rate (0.001..1000000), HTTP timeout or retry intervals")
	}
	return
}
func ParseQueue(args []string, out io.Writer) (c Queue, err error) {
	f := flags("queue", out)
	c.Durable = queue.DefaultDurableOptions("work/queue.db")
	f.StringVar(&c.Mode, "mode", "durable", "durable or memory (Day 1 regression only)")
	f.StringVar(&c.Durable.Path, "db", c.Durable.Path, "persistent database path")
	f.IntVar(&c.Durable.QuarantineCapacity, "quarantine-capacity", c.Durable.QuarantineCapacity, "maximum quarantined messages (also count toward total capacity)")
	f.IntVar(&c.Durable.MaxPayloadBytes, "max-payload-bytes", c.Durable.MaxPayloadBytes, "maximum serialized event bytes")
	f.Int64Var(&c.Durable.MaxLogicalBytes, "max-logical-bytes", c.Durable.MaxLogicalBytes, "payload and metadata logical byte budget")
	f.DurationVar(&c.Durable.LeaseDuration, "lease-duration", c.Durable.LeaseDuration, "broker lease duration")
	f.DurationVar(&c.Durable.CompletionTTL, "completion-ttl", c.Durable.CompletionTTL, "completed ID retention from ACK")
	f.DurationVar(&c.Durable.OpenTimeout, "db-open-timeout", c.Durable.OpenTimeout, "exclusive database lock timeout")
	f.DurationVar(&c.Durable.CleanupInterval, "cleanup-interval", c.Durable.CleanupInterval, "lease/completion expiry cadence")
	f.IntVar(&c.Durable.CleanupBatch, "cleanup-batch", c.Durable.CleanupBatch, "maximum expired entries per index per cleanup")
	common(f, &c.Common)
	f.StringVar(&c.Listen, "listen", "127.0.0.1:8081", "HTTP listen address")
	f.IntVar(&c.Capacity, "capacity", 1000, "maximum queued events (1..10000)")
	f.IntVar(&c.DedupCapacity, "dedup-capacity", 10000, "maximum active IDs and consumed tombstones (up to 100000)")
	f.DurationVar(&c.DedupTTL, "dedup-ttl", time.Minute, "dedup window after receive")
	if err = f.Parse(args); err != nil {
		return
	}
	err = valid(f, c.Common)
	if err != nil {
		return
	}
	if _, _, err = net.SplitHostPort(c.Listen); err != nil {
		return
	}
	if c.Capacity < 1 || c.Capacity > 10000 || c.DedupCapacity < c.Capacity || c.DedupCapacity > 100000 || c.DedupTTL <= 0 {
		err = fmt.Errorf("invalid queue capacity, dedup capacity or TTL")
	}
	if err == nil {
		if c.Mode != "durable" && c.Mode != "memory" {
			err = fmt.Errorf("mode must be durable or memory")
		} else if c.Mode == "durable" {
			c.Durable.Capacity = c.Capacity
			c.Durable.DedupCapacity = c.DedupCapacity
			err = c.Durable.Validate()
		}
	}
	return
}
func ParseConsumer(args []string, out io.Writer) (c Consumer, err error) {
	f := flags("testconsumer", out)
	f.DurationVar(&c.ProcessingDelay, "processing-delay", 0, "diagnostic delay after leasing and before output/ACK")
	f.StringVar(&c.Mode, "mode", "durable", "durable or memory")
	f.StringVar(&c.Action, "action", "ack", "ack, noack, retry, quarantine")
	f.StringVar(&c.Reason, "reason", "diagnostic consumer request", "reason for NACK (1..256 bytes)")
	f.DurationVar(&c.LeaseWait, "lease-wait", time.Second, "long-poll wait, shorter than HTTP timeout")
	f.DurationVar(&c.RetryDelay, "retry-delay", time.Second, "delay for transient NACK")
	f.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", 20*time.Second, "maximum pending receipt duration")
	f.StringVar(&c.LogLevel, "log-level", "info", "debug, info, warn, error")
	f.StringVar(&c.QueueURL, "queue-url", "http://127.0.0.1:8081", "queue origin")
	f.Uint64Var(&c.Count, "count", 100, "number of events to receive")
	f.DurationVar(&c.Timeout, "timeout", 30*time.Second, "total receive deadline")
	f.DurationVar(&c.HTTPTimeout, "http-timeout", 3*time.Second, "per-request timeout")
	if err = f.Parse(args); err != nil {
		return
	}
	if f.NArg() != 0 {
		err = fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	if err != nil {
		return
	}
	if c.ProcessingDelay < 0 || c.ProcessingDelay > time.Hour || c.Count == 0 || c.Timeout <= 0 || c.HTTPTimeout <= 0 || c.ShutdownTimeout <= 0 || c.LeaseWait < 0 || c.LeaseWait > queue.MaxWait || c.LeaseWait >= c.HTTPTimeout || c.RetryDelay < 0 || c.RetryDelay > queue.MaxNackDelay || len(c.Reason) == 0 || len(c.Reason) > 256 || (c.Mode != "durable" && c.Mode != "memory") || (c.Action != "ack" && c.Action != "noack" && c.Action != "retry" && c.Action != "quarantine") || (c.Mode == "memory" && c.Action != "ack") {
		err = fmt.Errorf("count and timeouts must be positive")
	}
	return
}
