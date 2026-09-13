# Day 1 internal HTTP protocol

Historical memory-mode protocol. Run the broker/consumer with --mode memory to use it. The default is now durable mode; see DAY2_PROTOCOL.md.

These endpoints are internal development interfaces, not the future public GPU API. The default bind address is loopback. No authentication or TLS termination is provided.

## Publish

`POST /internal/v1/messages`, JSON body `{ "events": [ EVENT ] }`.

Exactly one event is allowed in Day 1's batch-shaped envelope. The entire request must fit 64 KiB. Unknown fields, invalid events, multiple JSON documents, and empty/multiple-event batches are rejected.

Event fields: schema_version (1), event_id, producer_instance_id, replay_index (zero-based), row_index (one-based), processed_at, source_timestamp, gpu_uuid, metric_name, value (finite JSON number), gpu_id (host-local string), device, model_name, hostname, container, pod, namespace and labels_raw. The producer emits UTC RFC3339 timestamps with processed_at truncated to microseconds. Source metadata is not randomly modified.

Success: HTTP 202 with `{ "accepted_ids": ["EVENT_ID"], "duplicate": false }`. Duplicate publish success returns the same shape with duplicate=true. Success means volatile memory acceptance; no disk or replica commit occurs.

| Status | Meaning | Producer action |
| --- | --- | --- |
| 400 | Invalid JSON, batch or event | Stop; report permanent rejection |
| 409 | Same event ID, different content | Stop; ID contract violated |
| 413 | Request exceeds limit | Stop; fix payload |
| 429 | Queue or dedup capacity exhausted | Retry same pending event |
| 503 | Active HTTP handler capacity exhausted | Retry same pending event |
| 408 / other 5xx | Transient service failure | Retry same pending event |

Application error body: `{ "code": "capacity", "message": "queue capacity exhausted" }`. Standard Go mux errors for wrong methods/unknown routes need not use this envelope. No redirects are followed. A missing/mismatched success confirmation is ambiguous and retryable. Retry-After is an advisory broker header; the Day 1 client uses its configured bounded jitter policy.

Dedup applies throughout queue residence and for dedup-ttl after receive. The timer is not extended by retries. When the bounded dedup map is full, new IDs are rejected until expiry releases space. A retry beyond the post-receive window may be accepted again. Full dedup state is lost on restart.

## Destructive receive

`POST /internal/v1/receive` with no body. HTTP 200 returns `{ "events": [ EVENT ] }`; HTTP 204 means empty. One receive removes one FIFO event under the same mutex used for publish. Concurrent consumers divide events, rather than broadcast them.

**There is no lease, ACK, NACK or redelivery.** Network response loss or consumer failure after removal loses the event. The test consumer polls every 100ms while empty and has a finite total deadline. It emits one complete JSON event per stdout line.

Day 2 should add `/internal/v1/leases`, `/internal/v1/acks` and `/internal/v1/nacks` with delivery tokens and explicit guarantees. Do not silently reinterpret this destructive endpoint as a lease endpoint.

## Diagnostics and shutdown

`GET /healthz` returns status=ok. `GET /internal/v1/stats` returns depth, capacity, dedup_entries, accepted, duplicates and received. Expired tombstones are purged on the next publish; dedup_entries can include logically expired tombstones until then, while remaining bounded.

Streamer SIGINT/SIGTERM stops new generation. One independent publish context remains alive for at most shutdown-timeout; the same event can retry in that window. After expiry, outstanding I/O is canceled and final stats report unconfirmed pending work. Finite completion and runtime errors also close the CSV/client and join the shutdown watcher. Forced termination can lose an unaccepted event or leave acceptance uncertain.

Broker graceful shutdown stops accepting connections and waits for existing handlers within shutdown-timeout. It then logs its remaining volatile backlog. Stopping the broker does not persist or transfer that backlog.
