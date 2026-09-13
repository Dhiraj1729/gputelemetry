# Day 3: collector and PostgreSQL

## Scope

Day 3 adds cmd/collector, cmd/migrate, internal/collector, internal/storage/postgres, environment/flag configuration, an additive embedded SQL migration, unit tests and an isolated PostgreSQL integration suite. PostgreSQL is the shared store for all collector replicas and the future API. The API/OpenAPI remain Day 4 work. There are no new Dockerfiles, Helm resources, Kubernetes deployments, retention jobs or UI.

## Processing and failure behavior

Each worker leases a batch of **one** event, matching the current broker endpoint. The batch size is intentionally fixed; --workers controls concurrency, bounded to 1..10 (default 2). There is no prefetch queue, unbounded goroutine fan-out, or application-wide insert lock. The default PostgreSQL pool permits at most four connections, configurable to 1..10 per process. Ten collectors at the default permit up to 40 connections in total; budget database connections across replicas and future API pools.

The sequence is lease -> semantic validation -> PostgreSQL transaction -> telemetry insert and GPU metadata upsert -> COMMIT -> ACK. Telemetry is inserted first with `ON CONFLICT(event_id) DO NOTHING`. A deferred foreign key permits the GPU metadata upsert in the same transaction. The unique constraint arbitrates concurrent duplicate deliveries. If no row was inserted, the transaction changes no GPU metadata. A failure in either statement rolls back both. An uncertain COMMIT is treated as failure and is never ACKed. Redelivery discovers the existing event_id if that commit actually succeeded.

Database errors retry under the same delivery's deadline with exponential backoff and jitter (100ms initial, 2s maximum). Exhausting the deadline leaves the message unacknowledged; broker lease expiry permits redelivery. ACK failures retry the same token, without reinserting in that attempt. A 409 stale receipt stops that attempt. A process killed after commit but before ACK can cause delivery again, but not another telemetry row. This is at-least-once delivery with idempotent persistence, not distributed exactly-once delivery.

Processing and receipts use the earlier of work-timeout (10s default) and the received lease expiry minus 250ms. Keep clocks synchronized and set broker lease-duration comfortably above processing latency. No lease renewal is provided. An old worker can finish a database commit after its lease expires; uniqueness keeps a simultaneous redelivery safe. The 250ms margin is operational headroom, not a distributed-clock guarantee.

Invalid semantic events, invalid PostgreSQL text (NUL/invalid UTF-8), unsupported schemas and unrepresentable timestamps go through permanent NACK with a reason, and remain in broker quarantine. Full quarantine or an expired receipt leaves the event available for eventual retry. The production client preserves semantic-invalid events with valid envelope/receipt identity so they can be quarantined; the historical diagnostic client's validation remains unchanged. Completely malformed JSON/envelopes, including timestamps that cannot be decoded, cannot safely yield a receipt identity: the client reports an unconfirmed lease and lets it expire rather than guessing what to ACK/NACK. The current broker validates and emits typed JSON, so these imply protocol incompatibility/corruption and need operator investigation.

All retry sequences are deadline-bounded; the long-running worker may revisit a repeatedly failing message on later leases. Infrastructure/schema/permission errors are not automatically labeled poison messages or deleted.

## Data contract

`gpus.uuid` is text identity, preserving source strings such as GPU-... without assuming PostgreSQL's UUID syntax. Metadata comprises hostname, local_gpu_id, device, model_name, first_seen and last_seen. These observation bounds use processed_at; latest metadata wins by (processed_at, event_id), making out-of-order delivery safe and equal-time ties deterministic. Duplicate event_id delivery changes neither ingestion time nor metadata. Event identity is authoritative: if a producer reuses an ID with changed content after broker dedup expiry, the first database row wins. Producers must keep IDs stable and unique as documented in Day 1.

`telemetry` contains event_id primary key, gpu_uuid foreign key, metric_name, numeric double-precision value, processed_at, source_timestamp, ingested_at, labels_raw, container, pod, namespace, and a JSONB copy of the full event (including producer/replay identity). Raw labels remain text; no lossy label parser is introduced. Index `(gpu_uuid, processed_at, event_id)` supports the planned ordered GPU time-series API.

- source_timestamp: original CSV timestamp.
- processed_at: streamer-assigned event creation time; the timestamp for future API filtering.
- ingested_at: database wall-clock time assigned on the first successful insert, unchanged by duplicates.

Connections use UTC and columns are timestamptz. PostgreSQL timestamp columns have microsecond precision; the preserved JSON event retains the original encoded source timestamp. Numeric values are stored without unit conversion. Values must be finite.

## Migrations and retention

`make migrate` (or bin/migrate) requires DATABASE_URL. It runs the embedded 001_initial.sql in a transaction with a 60s deadline and records a SHA-256 checksum in gpu_schema_migrations. Re-running an unchanged migration is safe. A checksum mismatch fails; do not edit an already-applied migration. A database advisory lock serializes migration runners only. Collectors never migrate automatically and never hold an application-wide database lock. Future migrations should be new ordered versions; the present runner applies the one implemented version.

Use a database dedicated to this project. The first migration creates gpus and telemetry and fails rather than overwriting conflicting existing tables. Readiness checks the migration marker. Missing migrations, database outages or revoked access make readiness fail; run migrations explicitly before normal processing.

There is **no automatic telemetry deletion**, delete endpoint, down migration or retention cleanup command. A configurable archive/delete retention policy is future work. Database and volume growth must be monitored. Stopping the local PostgreSQL container retains its named volume; changing POSTGRES_PASSWORD does not rotate credentials in an already initialized database.

## Configuration and service health

Flags override environment. DATABASE_URL supplies the connection URL; COLLECTOR_DATABASE_URL can override that default. Every collector flag also accepts COLLECTOR_ plus its upper-case name with hyphens replaced by underscores (for example COLLECTOR_POOL_MAX). Passwords are omitted from help and application logs. Use environment/secret injection for actual credentials; the commands below use a local demo password only.

| Flag | Default | Bounds/purpose |
| --- | --- | --- |
| --queue-url | http://127.0.0.1:8081 | Existing durable broker origin |
| --listen | 127.0.0.1:8082 | Health HTTP listener; use :8082 inside a future container |
| --workers | 2 | 1..10; one leased event per worker |
| --pool-max | 4 | 1..10 PostgreSQL connections; no minimum idle pool |
| --lease-wait | 1s | 0..10s, less than HTTP timeout |
| --http-timeout | 3s | Queue request timeout, at most 1m |
| --work-timeout | 10s | Per-delivery DB/receipt budget, at most 1m |
| --shutdown-timeout | 20s | Drain budget after stopping acquisition, at most 1m |
| --retry-min / --retry-max | 100ms / 2s | Positive bounded jittered exponential backoff; max at most 10s |
| --log-level | info | JSON slog on stderr |

PostgreSQL connections have a 3s connect timeout, 10s server statement timeout and 15s idle-transaction timeout. The caller context can impose a shorter deadline. A bounded one-second rollback cleanup and HTTP shutdown may add cleanup time after processing cancellation; future pod grace periods should retain the existing 30s recommendation for default settings.

`GET /healthz` is process liveness and remains 200 during a database outage. `GET /readyz` checks migration availability through the database with a one-second deadline and returns 503 while unavailable or shutting down. It does not prove queue availability or that a future insert will succeed. There is no public telemetry API in the collector.

SIGINT/SIGTERM immediately cancels lease acquisition. Existing transactions/receipts can drain until the shutdown budget expires, when their contexts are canceled. Uncommitted/uncertain work is never ACKed. Health handlers and database connections then close. A crash requires no consumer-side recovery log; the broker and PostgreSQL own the durable state.

## Local run

```sh
cd /Users/dhirajsriharsha/Preparation/ProjectMsgQueue
colima start gpu-telemetry
make postgres-up
export DATABASE_URL='postgres://telemetry:gpu-local-dev@127.0.0.1:15432/telemetry?sslmode=disable'
make build
make migrate
```

The postgres-up script starts only the upstream database image, with a 512MiB limit, one CPU, loopback port 15432 and named volume gpu-telemetry-postgres18. PostgreSQL 18 uses /var/lib/postgresql as the volume mount. It preserves an existing managed container. It does not start Minikube. DAY3_DOCKER_CONTEXT selects another Docker context; DAY3_POSTGRES_IMAGE can select an explicit compatible image digest. Test evidence will record the actual image used.

In three terminals from the repository:

```sh
# Terminal 1: durable broker (keep this running)
./bin/queue --db work/queue.db

# Terminal 2: production collector (keep this running)
export DATABASE_URL='postgres://telemetry:gpu-local-dev@127.0.0.1:15432/telemetry?sslmode=disable'
./bin/collector

# Terminal 3: finite input, then inspect readiness and stored data
./bin/streamer --count 100 --rate 10
curl -fsS http://127.0.0.1:8082/readyz
curl -fsS http://127.0.0.1:8081/internal/v1/stats | jq

docker --context colima-gpu-telemetry exec gpu-telemetry-postgres \
  psql -U telemetry -d telemetry -c 'SELECT count(*) AS events, count(DISTINCT event_id) AS unique_events FROM telemetry;'
docker --context colima-gpu-telemetry exec gpu-telemetry-postgres \
  psql -U telemetry -d telemetry -c 'SELECT event_id,gpu_uuid,metric_name,value,processed_at,source_timestamp,ingested_at FROM telemetry ORDER BY processed_at,event_id LIMIT 5;'
```

On fresh broker/database storage with no other producer, expect 100 telemetry rows, 100 unique IDs, no ready/leased messages, and 100 ACKs. Existing storage yields cumulative counts. Each new streamer process generates new event IDs, so a second run adds another 100 rows; it is not a duplicate-delivery test. Do not simultaneously run the diagnostic testconsumer: it ACKs messages without PostgreSQL persistence and competes with the production collector.

Stop streamers, then collectors and broker with Ctrl-C. To stop local PostgreSQL while retaining data: `docker --context colima-gpu-telemetry stop gpu-telemetry-postgres`. `make postgres-up` starts it again. No reset/delete workflow is provided.

## Automated verification

```sh
make build vet test race coverage demo
make demo-day3
```

The first line includes the Day 1/Day 2 regression suite and new collector unit tests. demo-day3 requires a working Docker daemon and builds real collector/streamer binaries. It creates a uniquely named disposable PostgreSQL container, random loopback port and isolated broker database; cleanup removes only that test-owned container, never the manual PostgreSQL volume. A missing daemon is a test failure, not a silent skip.

The Day 3 suite covers migration reruns, normal queue-to-database persistence, concurrent duplicate inserts, metadata ordering, full transaction rollback, database lock timeout/no ACK and recovery, quarantine, real collector SIGKILL after commit with ACK blocked, and actual CSV streamer/collector operation with clean SIGTERM. Unit tests additionally cover ACK response retry without another insert, stale ACK, invalid-event quarantine, acquisition errors, bounded workers and drain deadlines, configuration precedence, health and credential-safe help.

See DAY3_VERIFICATION.md for results actually executed. No throughput or 10-replica performance claim follows from correctness tests. API endpoints, inclusive time filtering, generated OpenAPI and API tests remain Day 4.

Official references: [pgx pool and transaction contracts](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool), [PostgreSQL INSERT / ON CONFLICT](https://www.postgresql.org/docs/18/sql-insert.html).
