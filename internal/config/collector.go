package config

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

type Collector struct {
	DatabaseURL, QueueURL, Listen, LogLevel                                  string
	Workers, PoolMax                                                         int
	LeaseWait, HTTPTimeout, WorkTimeout, ShutdownTimeout, RetryMin, RetryMax time.Duration
}

func ParseCollector(args []string, out io.Writer) (c Collector, err error) {
	f := flags("collector", out)
	f.StringVar(&c.DatabaseURL, "database-url", os.Getenv("DATABASE_URL"), "PostgreSQL connection string (prefer DATABASE_URL to avoid shell history)")
	f.StringVar(&c.QueueURL, "queue-url", "http://127.0.0.1:8081", "queue origin")
	f.StringVar(&c.Listen, "listen", "127.0.0.1:8082", "health HTTP address")
	f.StringVar(&c.LogLevel, "log-level", "info", "structured log level")
	f.IntVar(&c.Workers, "workers", 2, "workers, each leases a one-message batch (1..10)")
	f.IntVar(&c.PoolMax, "pool-max", 4, "maximum PostgreSQL connections (1..10)")
	f.DurationVar(&c.LeaseWait, "lease-wait", time.Second, "queue long poll wait")
	f.DurationVar(&c.HTTPTimeout, "http-timeout", 3*time.Second, "queue request timeout")
	f.DurationVar(&c.WorkTimeout, "work-timeout", 10*time.Second, "maximum processing and receipt time per delivery")
	f.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", 20*time.Second, "maximum drain time after signal")
	f.DurationVar(&c.RetryMin, "retry-min", 100*time.Millisecond, "initial retry backoff")
	f.DurationVar(&c.RetryMax, "retry-max", 2*time.Second, "maximum retry backoff")
	// Environment establishes defaults; explicit flags take precedence. Do not echo secrets on errors.
	f.VisitAll(func(v *flag.Flag) {
		if value, ok := os.LookupEnv("COLLECTOR_" + strings.ToUpper(strings.ReplaceAll(v.Name, "-", "_"))); ok && err == nil {
			if e := f.Set(v.Name, value); e != nil {
				err = fmt.Errorf("invalid environment setting for %s", v.Name)
			}
		}
	})
	if err != nil {
		return
	}
	// Help output must never print the environment-provided database password.
	f.Lookup("database-url").DefValue = ""
	if err = f.Parse(args); err != nil {
		return
	}
	if f.NArg() != 0 || c.DatabaseURL == "" || c.Workers < 1 || c.Workers > 10 || c.PoolMax < 1 || c.PoolMax > 10 || c.LeaseWait < 0 || c.LeaseWait > 10*time.Second || c.HTTPTimeout <= c.LeaseWait || c.HTTPTimeout > time.Minute || c.WorkTimeout <= 0 || c.WorkTimeout > time.Minute || c.ShutdownTimeout <= 0 || c.ShutdownTimeout > time.Minute || c.RetryMin <= 0 || c.RetryMax < c.RetryMin || c.RetryMax > 10*time.Second {
		err = fmt.Errorf("invalid collector configuration: require database URL, bounded workers/pool, positive deadlines and valid retry intervals")
		return
	}
	_, _, err = net.SplitHostPort(c.Listen)
	return
}
