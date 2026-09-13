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

type API struct {
	DatabaseURL, Listen, LogLevel, TempDir      string
	PoolMax, MaxConcurrent                      int
	QueryTimeout, WriteTimeout, ShutdownTimeout time.Duration
	MaxResponseBytes                            int64
}

func DefaultAPI() API {
	return API{Listen: "127.0.0.1:8080", LogLevel: "info", PoolMax: 4, MaxConcurrent: 4, QueryTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, ShutdownTimeout: 20 * time.Second, MaxResponseBytes: 64 << 20}
}
func ParseAPI(args []string, out io.Writer) (c API, err error) {
	c = DefaultAPI()
	f := flags("api", out)
	f.StringVar(&c.DatabaseURL, "database-url", os.Getenv("DATABASE_URL"), "PostgreSQL URL; prefer DATABASE_URL secret injection")
	f.StringVar(&c.Listen, "listen", c.Listen, "HTTP address")
	f.StringVar(&c.LogLevel, "log-level", c.LogLevel, "JSON log level")
	f.StringVar(&c.TempDir, "temp-dir", c.TempDir, "temporary response directory (default OS temp)")
	f.IntVar(&c.PoolMax, "pool-max", c.PoolMax, "1..10 database connections")
	f.IntVar(&c.MaxConcurrent, "max-concurrent", c.MaxConcurrent, "1..16 active data requests")
	f.DurationVar(&c.QueryTimeout, "query-timeout", c.QueryTimeout, "database and JSON preparation budget")
	f.DurationVar(&c.WriteTimeout, "write-timeout", c.WriteTimeout, "total HTTP write budget; greater than query timeout")
	f.DurationVar(&c.ShutdownTimeout, "shutdown-timeout", c.ShutdownTimeout, "graceful drain deadline")
	f.Int64Var(&c.MaxResponseBytes, "max-response-bytes", c.MaxResponseBytes, "per-response disk budget; exceeding it returns 503, never partial rows")
	f.VisitAll(func(v *flag.Flag) {
		if value, ok := os.LookupEnv("API_" + strings.ToUpper(strings.ReplaceAll(v.Name, "-", "_"))); ok && err == nil {
			if e := f.Set(v.Name, value); e != nil {
				err = fmt.Errorf("invalid environment setting for %s", v.Name)
			}
		}
	})
	f.Lookup("database-url").DefValue = ""
	if err != nil {
		return
	}
	if err = f.Parse(args); err != nil {
		return
	}
	if f.NArg() != 0 || c.DatabaseURL == "" || c.PoolMax < 1 || c.PoolMax > 10 || c.MaxConcurrent < 1 || c.MaxConcurrent > 16 || c.QueryTimeout <= 0 || c.QueryTimeout > time.Minute || c.WriteTimeout <= c.QueryTimeout || c.WriteTimeout > 5*time.Minute || c.ShutdownTimeout <= 0 || c.ShutdownTimeout > time.Minute || c.MaxResponseBytes < 2 || c.MaxResponseBytes > 1<<30 {
		err = fmt.Errorf("invalid API database URL, capacity or timeout settings")
		return
	}
	_, _, err = net.SplitHostPort(c.Listen)
	return
}
