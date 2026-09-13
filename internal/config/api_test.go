package config

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestAPIConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:unique-password-937@localhost/db")
	t.Setenv("API_POOL_MAX", "3")
	c, err := ParseAPI([]string{"--pool-max", "2"}, io.Discard)
	if err != nil || c.PoolMax != 2 || c.Listen != "127.0.0.1:8080" {
		t.Fatal(c, err)
	}
	for _, args := range [][]string{{"--pool-max", "11"}, {"--max-concurrent", "0"}, {"--query-timeout", "0s"}, {"--write-timeout", "5s"}, {"--max-response-bytes", "1"}, {"--listen", "invalid"}, {"--database-url", ""}, {"extra"}} {
		if _, err := ParseAPI(args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
	var b bytes.Buffer
	_, _ = ParseAPI([]string{"--help"}, &b)
	if strings.Contains(b.String(), "unique-password-937") {
		t.Fatal("credential leak")
	}
	t.Setenv("API_POOL_MAX", "bad")
	if _, err := ParseAPI(nil, io.Discard); err == nil {
		t.Fatal("invalid env accepted")
	}
}
