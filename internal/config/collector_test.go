package config

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCollectorConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("COLLECTOR_WORKERS", "3")
	c, err := ParseCollector([]string{"--workers", "2"}, io.Discard)
	if err != nil || c.Workers != 2 || c.PoolMax != 4 || c.WorkTimeout != 10*time.Second {
		t.Fatal(c, err)
	}
	for _, args := range [][]string{{"--workers", "0"}, {"--pool-max", "11"}, {"--work-timeout", "0s"}, {"--lease-wait", "3s"}, {"--retry-min", "3s", "--retry-max", "1s"}, {"--listen", "bad"}, {"extra"}, {"--database-url", ""}} {
		if _, err := ParseCollector(args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
	t.Setenv("COLLECTOR_WORKERS", "oops")
	if _, err := ParseCollector(nil, io.Discard); err == nil {
		t.Fatal("bad env")
	}
}

func TestCollectorHelpDoesNotExposeDatabaseSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:secret-password@localhost/db")
	var b bytes.Buffer
	_, _ = ParseCollector([]string{"--help"}, &b)
	if strings.Contains(b.String(), "secret-password") {
		t.Fatal("credential leaked through help")
	}
}
