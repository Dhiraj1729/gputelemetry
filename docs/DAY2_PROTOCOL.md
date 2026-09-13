# Day 2 durable queue protocol

Status: implemented and tested. This replaces Day 1's default broker mode, not its event or publish contract. Day 1 memory mode is explicitly available for regression tests.

## Delivery guarantee and deployment boundary

The queue provides at-least-once delivery for durably accepted events, assuming the database/storage survives and consumers eventually resume. Events remain pending until successfully acknowledged or explicitly quarantined by a permanent NACK. It is not exactly-once processing. Duplicate deliveries and output are possible. Concurrent consumers divide work, not broadcasts. Delayed retry, expiry and competing consumers can change processing order; only ready-time/sequence order is used to select the next eligible event.

The broker uses bbolt v1.5.0 for atomic storage transactions. Our Go code implements admission, deduplication, ready scheduling, leases, fencing tokens, ACK/NACK, quotas and cleanup. Disk synchronization remains enabled. A success response is sent only after Update/commit succeeds. If a response is lost after commit, the operation may have succeeded: retry using the documented identity.

Future Kubernetes deployment: **one StatefulSet replica**, an internal ClusterIP Service on port **8081**, and a persistent volume. The chart must retain persistent storage on uninstall; do not put a deletable unprotected PVC in a release or equate retention with backup. Helm resources are not implemented today. Do not scale this broker to independent replicas behind one Service. A second process cannot acquire the same database lock, but this is not replication or a distributed leader-election protocol. Pod/process recovery is supported; destruction of the volume, filesystem corruption and machine-wide storage loss are not covered. Keep backups and separate storage headroom for any serious deployment.

## Event and publish compatibility

`POST /internal/v1/messages`

```json
{"events":[{"schema_version":1,"event_id":"...","producer_instance_id":"...","replay_index":0,"row_index":1,"processed_at":"2026-09-12T10:00:00Z","source_timestamp":"2025-07-18T20:42:34Z","gpu_uuid":"GPU-...","metric_name":"GPU_UTIL","value":42,"gpu_id":"0","device":"nvidia0","model_name":"H100","hostname":"host","container":"","pod":"","namespace":"","labels_raw":"..."}]}
```

Exactly one event per batch-shaped request, preserving the existing streamer. HTTP 202:

```json
{"accepted_ids":["..."],"duplicate":false}
```

The same event ID and content returns duplicate=true while the identity is retained. Different content with the same retained ID returns 409. The content fingerprint is computed from the decoded event's canonical Go JSON encoding; property order/whitespace in the request does not matter.

Active/leased/quarantined identities do not expire. Completed identities expire completion-ttl after successful ACK (default one hour). An old ID can be accepted as a new message after that retention window; changing its payload after expiry is also allowed. The streamer retains one pending event and stable contents during retries, unchanged from Day 1.

## Lease

`POST /internal/v1/leases`

```json
{"wait_ms":1000}
```

wait_ms is 0..10000. The default client waits one second, with a three-second HTTP timeout. HTTP timeout must exceed requested long-poll wait. Empty results return 204. HTTP 200 returns one delivery:

```json
{"event":{"event_id":"..."},"token":"32-character-random-token","expires_at":"2026-09-12T10:00:30Z","attempt":1}
```

The real event object contains the complete shared telemetry contract. Every delivery attempt has a new cryptographically random token and persisted attempt count. Lease duration is controlled by the broker (default 30 seconds), not the consumer. State and token are committed atomically before delivery is returned. No overlapping *valid* leases are issued for an event. Slow consumers may keep processing after expiry, so side effects still require idempotency.

Broker wall time decides expiry; client clocks are not authoritative. A broker restart preserves outstanding leases until their stored expiry. Due lease recovery occurs through indexed cleanup and lease acquisition. Clock jumps can shorten or extend apparent leases; run hosts with sensible time synchronization. Lease renewal is not implemented. Processing plus receipt must fit the configured duration.

Polling never holds a database transaction during network waits. The service permits at most 32 concurrent long polls and 64 active handlers, leaving capacity for receipts/publications. Timer wakeups cover delayed events/expiry, while committed transitions signal waiting callers. Shutdown and request cancellation interrupt waiting callers. A canceled/lost lease response may already have assigned a lease, which safely expires for redelivery.

## ACK

`POST /internal/v1/acks`

```json
{"event_id":"...","token":"..."}
```

The token must match the current lease and the broker must consider it unexpired. ACK atomically deletes the payload, updates counters, and retains a completion record with the hash and successful token. HTTP 200:

```json
{"event_id":"...","duplicate":false}
```

Retrying the same successful ACK returns duplicate=true until completion retention expires, even if the original lease deadline has now passed. A different token, an expired still-unacknowledged lease, an unknown event, or a superseded attempt returns 409 stale_lease. After completion retention expires, the old ACK is no longer recognized; callers must not interpret this as proof that processing failed.

The future collector must commit PostgreSQL changes before ACK, with a unique constraint on event_id. If commit succeeds but ACK is lost, safe redelivery must not create a duplicate database row. The current test consumer merely writes JSON to its output stream; it is not durable transactional processing and may output duplicates.

## NACK

`POST /internal/v1/nacks`

Transient example:

```json
{"event_id":"...","token":"...","permanent":false,"delay_ms":1000,"reason":"temporary processing failure"}
```

Permanent example:

```json
{"event_id":"...","token":"...","permanent":true,"delay_ms":0,"reason":"invalid measurement"}
```

The current unexpired token is required. reason is 1..256 bytes. delay_ms is 0..3600000; permanent NACK requires zero delay. Transient NACK atomically moves the event to ready state with a future due time. Permanent NACK retains the payload, hash and reason in quarantined state, excluded from delivery.

HTTP 200 returns event_id and duplicate. Repeating an identical NACK is safe while its outcome is still current: during delayed/ready state before another lease, or indefinitely while quarantined. Altering the same token's NACK request returns 409 id_conflict. A new lease supersedes that token and clears the previous NACK retry record; an old NACK then returns 409 stale_lease. A rejected capacity-limited NACK leaves the lease unchanged. Receipt transitions and expiry are serialized in atomic transactions.

Quarantine has **indefinite retention** in this milestone and is bounded by both count and logical bytes. Cleanup never silently deletes it to reclaim space. Full quarantine returns 429 for additional permanent NACKs. Operator inspection/release/purge tools are deferred; reasons/payloads remain in the database and counts are exposed through stats.

## Limits and errors

| Status | Meaning | Retry behavior |
| --- | --- | --- |
| 200 / 202 | Committed success | Check matching response identity |
| 204 | No eligible event before lease wait expires | Poll again within caller deadline |
| 400 | Invalid event, receipt, wait/delay, or configured payload limit | Fix request; permanent rejection |
| 409 id_conflict | Retained ID/content or repeated NACK changed | Do not blindly retry changed content |
| 409 stale_lease | Unknown, expired or superseded token | Stop using that delivery token |
| 410 disabled | Destructive receive in durable mode | Use leases and ACK/NACK |
| 413 | HTTP JSON body exceeds 64 KiB | Reduce request size |
| 429 capacity | Payload/count/dedup/quarantine budget exhausted | Retry same pending operation with backoff |
| 503 | Storage error, shutdown, canceled request or handler overload | Outcome unconfirmed; retry with same identity |

Error bodies contain code and message. Wrong-method/unknown-route mux errors may use Go's default response format. Each request is one JSON document; unknown fields are rejected. Receipt IDs are 1..128 bytes; tokens are exactly 32 bytes. No redirects are followed. The existing client's retryable 408/429/5xx/network classification remains in place.

Default total message capacity is 1000, including ready, delayed, leased and quarantined messages. Default identity capacity is 10000, covering all pending/quarantined identities plus completion records. Admission reserves a completion slot for each event: ACK never needs an extra identity slot. Completion expiry frees those slots in bounded batches. Do not set the retained-ID window beyond what storage and traffic can support without expecting backpressure.

Serialized event limit defaults to 64512 bytes (64 KiB minus 1024), within the 64 KiB request limit. Logical bytes include actual serialized payload plus a conservative 4096-byte metadata allowance for each pending/quarantined event, and 1024 bytes per completion record. ACK reduces that allowance along with deleting the payload. The default logical budget is 64 MiB. These are application accounting limits, **not an exact filesystem-size limit**. bbolt transaction pages, indexes, freelist, mmap and filesystem overhead require headroom. Lowering limits on restart does not discard existing data: admission stays blocked until usage falls below the new limits.

## Storage schema, cleanup and shutdown

The database stores versioned records plus ready-time, lease-expiry and completion-expiry indexes. A single metadata record holds transactionally updated counters. Index keys combine due time and a monotonic sequence. Payload/hash/attempt/token/reason and state changes are committed with their indexes/counters. State is recovered directly from the same database; startup does not rebuild it by loading the entire backlog into RAM.

A periodic worker (default one-second cadence) expires at most cleanup-batch leases and cleanup-batch completions in a transaction (100 per index by default). Publish also expires a bounded completion batch, plus its directly addressed expired ID if necessary. There is no full database scan per publish. Idle cleanup first probes the index in a read transaction to avoid unnecessary write commits. Quarantined records are deliberately retained, not aged out.

`GET /internal/v1/stats` exposes ready (including delayed), leased, quarantined, completed, logical_bytes, accepted, acked, deliveries, redeliveries, file_bytes, configured limits, and cleanup_failures. Expired leases/completions can remain counted until a bounded cleanup pass reaches them. `GET /healthz` verifies the database is readable; it does not prove a future write will succeed.

Open uses an exclusive database lock with a one-second default timeout and a clear second-writer error. Database files are created with mode 0600; created parent directories use 0700. Never use memory mode as a fallback when a durable database cannot be opened.

On SIGINT/SIGTERM, the broker stops new database work, cancels long polls, stops the cleanup worker, waits for existing bounded handlers/transactions, and closes bbolt. It retains accepted data. Application shutdown timeout defaults to 20 seconds; future Kubernetes grace period should be 30 seconds. OS-level stalled disk writes cannot safely be canceled mid-fsync: the deployment may ultimately force termination, and recovery then relies on the last committed database state.

bbolt reuses freed pages but does not automatically shrink the file. For compaction, stop the broker, take a backup, compact into a *different* database file using bbolt's Compact API or a reviewed maintenance tool, verify the result, and replace it only while the broker is stopped. This project does not implement automated live compaction or an operator maintenance command yet. Do not remove the live database as a space-reclamation method.

Official storage reference: https://pkg.go.dev/go.etcd.io/bbolt (transaction, locking, synchronization and compaction behavior).
