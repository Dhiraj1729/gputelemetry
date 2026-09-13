package config

import (
	"io"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	s, err := ParseStreamer(nil, io.Discard)
	if err != nil || s.Rate != 10 || s.Count != 0 || s.ShutdownTimeout != 20*time.Second {
		t.Fatal(s, err)
	}
	q, err := ParseQueue(nil, io.Discard)
	if err != nil || q.Capacity != 1000 {
		t.Fatal(q, err)
	}
	if _, err = ParseConsumer(nil, io.Discard); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidFlags(t *testing.T) {
	for _, args := range [][]string{{"--rate", "NaN"}, {"--rate", "0"}, {"--rate", "Inf"}, {"--http-timeout", "0s"}, {"--retry-min", "3s", "--retry-max", "1s"}, {"--count", "-1"}, {"--shutdown-timeout", "0s"}, {"extra"}} {
		if _, err := ParseStreamer(args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
	for _, args := range [][]string{{"--capacity", "0"}, {"--capacity", "10001"}, {"--capacity", "10", "--dedup-capacity", "9"}, {"--dedup-ttl", "0s"}, {"--listen", "invalid"}} {
		if _, err := ParseQueue(args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
	if _, err := ParseConsumer([]string{"--count", "0"}, io.Discard); err == nil {
		t.Fatal("zero count")
	}
}

func TestDurableConfiguration(t *testing.T) {
	c, err := ParseQueue(nil, io.Discard)
	if err != nil || c.Mode != "durable" || c.Durable.LeaseDuration != 30*time.Second || c.Durable.Path != "work/queue.db" {
		t.Fatal(c, err)
	}
	for _, args := range [][]string{{"--mode", "bad"}, {"--db", ""}, {"--lease-duration", "0s"}, {"--max-payload-bytes", "999999"}, {"--max-logical-bytes", "1"}, {"--cleanup-batch", "0"}, {"--completion-ttl", "0s"}, {"--quarantine-capacity", "0"}} {
		if _, err := ParseQueue(args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
	for _, args := range [][]string{{"--action", "bad"}, {"--mode", "bad"}, {"--processing-delay", "-1s"}, {"--lease-wait", "4s"}, {"--mode", "memory", "--action", "retry"}} {
		if _, err := ParseConsumer(args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
}
