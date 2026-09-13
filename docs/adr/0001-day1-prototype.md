# ADR 0001: small, explicit Day 1 prototype

Status: implemented, 12 September 2026.

Use one Go module (gpu-telemetry, a local module name to replace with the eventual repository import path if needed), standard-library HTTP/JSON, flags, slog, a one-event pending producer, and a fixed-capacity queue ring. This supports the full process demonstration without adding a framework, database or deployment dependency.

Separate CSV source, replay loop, single-attempt client and queue store interfaces. The runner owns retry policy and resource cleanup. Producer identity is random per process; replay and row indexes provide measurement identity within that process. Source values stay unchanged.

Choose explicit memory-only acceptance and destructive receive for Day 1. The design plan's durable acceptance and batching targets describe later milestones; they are not claimed by this implementation. Default rate remains 10 events/second, with one event per publish.

Use an independent pending-publish context on graceful shutdown, bounded to 20 seconds after signal. A canceled generation context must not immediately discard in-flight publication. Join the watcher on every runner exit, including finite completion and errors.

Retain active-ID hashes and consumed-ID tombstones with TTL and a hard capacity. When dedup storage fills, backpressure preserves the documented window instead of silently evicting unexpired entries. FIFO expiry avoids scanning every retained ID on every publish. Duplicates with different payloads are rejected, and ambiguous network success is retried with unchanged content.

Initial configuration is flags only, fully described by --help. Bind loopback by default. Go tests inject clocks/waits where appropriate; process tests validate real signal wiring and the complete HTTP path.

Consequences: queue restart and destructive receive failures can lose accepted events; producer restarts replay input; dedup-window expiry can admit repeated events. Day 2 must implement durable storage and a separate lease/ACK protocol before claiming at-least-once delivery.
