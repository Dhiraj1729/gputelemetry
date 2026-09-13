# Day 4: read-only API and generated OpenAPI

The API is a separate Go executable at cmd/api. It reads PostgreSQL and does not call the queue. Huma v2.39.1 and the standard net/http server share typed operations between the running API and offline OpenAPI generation. The existing database schema is unchanged; migrations remain explicit Day 3 work.

## Start and inspect

From the project directory, with the Day 3 PostgreSQL database running and migrated:

```sh
cd /Users/dhirajsriharsha/Preparation/ProjectMsgQueue
export DATABASE_URL='postgres://telemetry:gpu-local-dev@127.0.0.1:15432/telemetry?sslmode=disable'
make build
./bin/api
```

If PostgreSQL is stopped, use `colima start gpu-telemetry` and `make postgres-up`; run `make migrate` on a new project database. The sample password is the existing local development default, not a production credential. Existing telemetry can be queried without running the streamer, broker or collector.

```sh
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/api/v1/gpus | jq
```

Choose a UUID from the GPU listing:

```sh
GPU_UUID='replace-with-a-returned-GPU-UUID'
curl -fsS "http://127.0.0.1:8080/api/v1/gpus/$GPU_UUID/telemetry" | jq
curl -fsS -G "http://127.0.0.1:8080/api/v1/gpus/$GPU_UUID/telemetry" \
  --data-urlencode 'start_time=2026-09-12T00:00:00Z' \
  --data-urlencode 'end_time=2026-09-12T23:59:59.999999Z' | jq
```

Choose a window matching your stored processed_at values. These are streamer creation times, not the historical CSV source dates. URL-encode timezone plus signs (curl --data-urlencode handles this).

## Endpoint contract

| Endpoint | Behavior |
| --- | --- |
| GET /healthz | 200 process liveness, including during DB outages |
| GET /readyz | 200 when PostgreSQL and migration marker are accessible; 503 otherwise or during shutdown |
| GET /api/v1/gpus | All GPUs with at least one telemetry row, ordered by UUID; empty DB yields 200 with [] |
| GET /api/v1/gpus/{id}/telemetry | All matching rows, ordered by processed_at ASC, event_id ASC |

GPU objects contain uuid, hostname, local_gpu_id, device, model_name, first_seen and last_seen. The UUID is the global identity; local_gpu_id is host-local.

Telemetry objects contain event_id, gpu_uuid, metric_name, value, processed_at, source_timestamp, ingested_at, labels_raw, container, pod and namespace. Raw labels and values are preserved; timestamps are emitted in UTC. The API does not expose the stored JSONB event copy in addition to these fields.

Optional start_time/end_time apply to processed_at with inclusive >= / <= comparisons. Either can be omitted. Accept RFC3339 with an explicit offset or Z and up to nine fractional digits. PostgreSQL stores microseconds: a sub-microsecond lower bound is rounded upward and an upper bound downward for comparisons, preserving inclusion mathematically instead of accidentally including an earlier/later row.

Unknown GPU returns 404. A known GPU with no matches returns 200 and []; arrays are never null. Malformed/empty/repeated timestamp parameters, reversed ranges and unknown query parameters return 400. GPU listing accepts no query parameters. Database connection/query failures, deadlines and response resource failures return 503. Application errors use Huma's application/problem+json schema; unmatched routes/methods use net/http behavior. No pagination or write routes exist.

## Complete results with bounded memory

The API consumes PostgreSQL rows one at a time and encodes the ordered JSON array into a private mode-0600 temporary file using a 32KiB buffer. Only after the database finishes successfully does it send 200, Content-Length and the file contents. Empty arrays are exactly []. There is no row-count LIMIT and no silently truncated success response.

This deliberately buffers on disk rather than sending a success header while the database is still executing: HTTP cannot switch an already-sent 200 to 503 if a later database read fails. Both early and mid-result database failures therefore remain 503. Tests cover a failure after the first row has already been encoded.

The default per-response disk budget is 64MiB and the default active-data-request limit is four, bounding temporary response payload space to 256MiB per API process. These are explicit operational budgets, not pagination. Exceeding the budget or query deadline returns 503 without partial data. Increase API_MAX_RESPONSE_BYTES (up to 1GiB) or narrow a time window when appropriate; do not interpret 503 as an empty result. PostgreSQL's inherited server statement timeout remains 10s per statement even if the API's overall preparation budget is increased.

Temporary response files close and are removed after normal success, failure or cancellation; no existing project/database files are deleted. Abrupt process termination can leave an unfinished temp file; automatic stale-file cleanup is not implemented. Select an appropriate temporary filesystem for future containers. No fsync is needed for these disposable HTTP buffers.

Once transfer begins, a client disconnect, socket deadline or local file read failure can still interrupt delivery. The server aborts the response, and Content-Length lets clients detect an incomplete transfer. Clients must check HTTP status and consume/parse the complete body before accepting a result. API transfer failures are not represented as successful shortened arrays.

## Configuration and lifecycle

Flags override API_ environment defaults. DATABASE_URL supplies the database connection; API_DATABASE_URL can override that default. Other flags use uppercase names with hyphens changed to underscores. Help/logs do not print the database URL/password.

| Flag / environment | Default | Purpose |
| --- | --- | --- |
| --listen / API_LISTEN | 127.0.0.1:8080 | HTTP listener |
| --pool-max / API_POOL_MAX | 4 | 1..10 DB connections |
| --max-concurrent / API_MAX_CONCURRENT | 4 | 1..16 active data requests; excess returns 503 |
| --query-timeout / API_QUERY_TIMEOUT | 10s | DB plus JSON preparation budget, maximum 1m |
| --write-timeout / API_WRITE_TIMEOUT | 30s | Total response write budget; must exceed query timeout, maximum 5m |
| --shutdown-timeout / API_SHUTDOWN_TIMEOUT | 20s | Graceful drain, maximum 1m |
| --max-response-bytes / API_MAX_RESPONSE_BYTES | 67108864 | Per-response disk budget, 2 bytes..1GiB |
| --temp-dir / API_TEMP_DIR | OS temp directory | Existing writable directory for private temporary files |
| --log-level / API_LOG_LEVEL | info | Structured JSON logs |

The API pool sets default_transaction_read_only=on, uses UTC and has no minimum idle connections. This is an application/session safeguard, not authentication; a dedicated SELECT-only PostgreSQL role is a later hardening option. No application SQL mutation or automatic migration is performed.

Readiness has a one-second dependency deadline. SIGTERM/SIGINT stops new work and marks readiness unavailable while active requests drain. At the shutdown deadline, request contexts are canceled and connections are closed. JSON request logs contain operation, duration and status, not full URLs or credentials.

## OpenAPI and reference documentation

```sh
make openapi
make check-openapi
```

The generated artifact is **api/openapi.yaml**, OpenAPI 3.1. The generator calls the same typed route-registration function as cmd/api and needs no DATABASE_URL, running database, queue or Docker daemon. Array schemas come from the Go element types; YAML is not handwritten. check-openapi compares generated bytes with the existing artifact and fails on drift without changing it. The generated file remains unstaged/uncommitted per the task's explicit Git constraint; it can be included when you later authorize a commit.

While the API runs:

- http://127.0.0.1:8080/openapi.json — machine-readable JSON.
- http://127.0.0.1:8080/openapi.yaml — machine-readable YAML.
- http://127.0.0.1:8080/docs — Huma's generated interactive API reference (browser assets may require internet).

There is no separate dashboard/application UI. The generated spec can also be imported into Swagger-compatible tooling. The docs endpoint and spec responses are tested; external browser/CDN rendering is not claimed as verified.

## Verification

```sh
make build vet test race coverage openapi check-openapi demo
make demo-day4
```

Day 4's real PostgreSQL suite uses a unique test-owned upstream PostgreSQL container and random loopback port, populates only that isolated database, and checks ordering, no-orphan GPU listing, exact inclusive boundaries, offsets, sub-microsecond comparisons, filtered-empty/unknown GPU behavior, malformed ranges, read-only sessions, database lock timeout, actual API executable/SIGTERM and database outage responses. Cleanup affects only the test-owned container. Override DAY4_DOCKER_CONTEXT or DAY4_POSTGRES_IMAGE when needed. No production database URL is used by this suite.

See DAY4_VERIFICATION.md for actual results. Unit coverage does not include real PostgreSQL integration or subprocess execution. Day 5 Dockerfiles/Helm and later live scaling/performance work remain outstanding. No retention/deletion policy, authentication or write API was introduced.

Design references: [Huma response streaming callbacks](https://huma.rocks/features/response-streaming/) and [Huma code-first OpenAPI generation](https://huma.rocks/features/openapi-generation/). The spool-before-success strategy is this project's choice to retain strict database-failure status behavior with bounded memory.
