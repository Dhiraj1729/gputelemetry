# Elastic GPU Telemetry Pipeline

**Day 5 packaging added:** five Docker targets and one Helm chart with database bootstrap, migration hooks, persistent storage and local verification scripts. See [Day 5 runbook](docs/DAY5_PACKAGING.md) and [verification status](docs/DAY5_VERIFICATION.md). Runtime image/deployment checks are pending access to the Mac Docker/Minikube environment.

**Day 4 API added:** read-only Go/Huma API, inclusive telemetry queries, generated OpenAPI, health/readiness and bounded resource use. See [Day 4 API guide](docs/DAY4_API.md) and [verification status](docs/DAY4_VERIFICATION.md).

**Day 3 collector added:** production Go collector, PostgreSQL persistence/migrations, commit-before-ACK, bounded workers/pool and health endpoints. See [Day 3 setup and design](docs/DAY3_COLLECTOR.md) for database setup, configuration, run commands and tests. The Day 4 API is now implemented; real database verification status is recorded separately.

**Day 2 implemented:** a Go CSV streamer, custom durable single-broker queue with bbolt, leased delivery, ACK/NACK, persistent deduplication, bounded cleanup/backpressure, and a diagnostic consumer. The streamer publish API and event format are unchanged. Go 1.27.1 is the tested toolchain; dependencies are pinned in go.mod/go.sum.

Durably accepted events survive a broker process crash/restart **when the same storage survives**. Delivery is at-least-once, not exactly-once. The queue is not replicated or highly available. Docker/Helm packaging is implemented in Day 5; runtime verification status is documented separately.

## Build and automated verification

```sh
cd /Users/dhirajsriharsha/Preparation/ProjectMsgQueue
make build
make test
make race
make vet
make coverage
make demo-day2
```

`make demo-day2` uses temporary database files/ports and real processes. It verifies an exclusive writer lock, lost publish response followed by SIGKILL/restart and publisher deduplication, consumer SIGKILL before ACK, persisted lease expiry, new delivery tokens, stale ACK/NACK rejection, and ACK completion across another restart. It also runs the unchanged streamer and updated consumer for 20 events. The expected final totals are accepted=21, acked=21, ready=0, leased=0, completed=21 (20 streamed records plus one known recovery event). It cleans up only its own processes/temp files and does not touch work/queue.db.

`make demo` / `make integration` run both the historical Day 1 memory regression and Day 2 process tests, plus the streamer signal/outage test. These process tests and the streamer Docker build use the included dummy dataset at `Problemstatement/dcgm_metrics_20250718_134233.csv`. The interview PDF and local reference prompts are intentionally excluded from the public repository. No Docker daemon or Kubernetes cluster is required for these local tests.

`make coverage` writes coverage/coverage.out and coverage/coverage.html. Open the HTML with `open coverage/coverage.html`. CLI entrypoints are exercised in subprocess tests, whose coverage is not merged into the unit profile.

## Manual run (three terminals)

Use the repository directory in every terminal. Terminal 1 starts a durable broker:

```sh
./bin/queue --db work/queue.db --listen 127.0.0.1:8081
```

Terminal 2 publishes exactly 100 events at the default rate of 10/second:

```sh
./bin/streamer --csv Problemstatement/dcgm_metrics_20250718_134233.csv \
  --queue-url http://127.0.0.1:8081 --count 100
```

Terminal 3 processes them and ACKs only after JSON output succeeds:

```sh
mkdir -p work
./bin/testconsumer --queue-url http://127.0.0.1:8081 \
  --count 100 --timeout 30s > work/received.jsonl
wc -l work/received.jsonl
curl -s http://127.0.0.1:8081/internal/v1/stats | jq
```

On a fresh database, expect 100 output lines, accepted=100, acked=100, ready=0, leased=0, completed=100. Stats are cumulative/persistent: reusing the same database from earlier runs changes these totals. The test consumer is a simulated sink; output can repeat after a crash and is not a PostgreSQL transaction.

The streamer and consumer exit after the finite count. Use Ctrl-C or `kill -TERM <pid>` to stop the broker. Restart with the **same --db path** to recover. Do not delete that file to restart. Stopping streamers first, then draining/ACKing desired work and stopping the broker gives a clean shutdown, but pending messages remain durable even if not drained.

For continuous generation: `./bin/streamer --count 0 --rate 10`. It keeps only one pending event, pauses generation while publishing retries, preserves retry IDs/timestamps, and stops generation on SIGINT/SIGTERM. A pending publish has up to 20 seconds to finish; unconfirmed acceptance is reported explicitly.

## Diagnostic consumer actions

These commands act on the next available event; publish data first. Use a separate test database if you want isolated counts.

```sh
# Receive one delivery, output it, and exit without ACK. It becomes eligible
# again after the broker's lease expires (30 seconds by default).
./bin/testconsumer --count 1 --action noack

# Explicit transient NACK, eligible again after one second.
./bin/testconsumer --count 1 --action retry --retry-delay 1s --reason 'temporary failure'

# Explicit permanent quarantine, retaining payload/identity/reason on disk.
./bin/testconsumer --count 1 --action quarantine --reason 'invalid measurement'

# Simulate slow processing for a consumer-crash test; do not use this setting
# for ordinary processing when it exceeds the configured lease duration.
./bin/testconsumer --count 1 --processing-delay 30s --timeout 60s
```

A lease token is logged for diagnostics. `--action noack` intentionally exits after its first delivery regardless of a larger count. `--count` otherwise counts successfully output/ACKed or NACKed delivery attempts, not globally unique IDs. Receipt failures retry the same token within a bounded pending phase. Stale-token rejection stops processing that attempt. The future collector must make database writes idempotent by event_id and commit before ACK.

## Configuration

Use `--help` on each binary. Configuration is flags, not environment variables, for now. Shared logging is JSON on stderr; event output is JSONL on stdout. No authentication is provided; bind is loopback by default.

| Broker flag | Default | Meaning |
| --- | --- | --- |
| --mode | durable | memory is an explicit Day 1 regression option |
| --listen | 127.0.0.1:8081 | Internal HTTP address |
| --db | work/queue.db | Persistent bbolt database |
| --capacity | 1000 | Ready + delayed + leased + quarantined messages |
| --dedup-capacity | 10000 | Pending/quarantine identity reservations + completed IDs |
| --quarantine-capacity | 100 | Retained quarantined records |
| --max-payload-bytes | 64512 | Maximum serialized event bytes |
| --max-logical-bytes | 67108864 | Payload plus metadata accounting budget |
| --lease-duration | 30s | Broker-timed lease |
| --completion-ttl | 1h | Completion/dedup retention after ACK |
| --db-open-timeout | 1s | Exclusive lock wait |
| --cleanup-interval / --cleanup-batch | 1s / 100 | Bounded expiry worker |
| --shutdown-timeout | 20s | Graceful HTTP shutdown budget |

Quarantine retention is indefinite, within its limits. Completion slots are reserved at publish so ACK cannot be blocked by metadata capacity. Admission applies backpressure instead of silently evicting pending/quarantined or unexpired completed IDs. Once completion retention expires, the same ID can be accepted again. Logical bytes are not exact file size; bbolt needs storage headroom and reuses pages without automatically shrinking. Offline compaction is maintenance work, not an automated feature.

Streamer flags remain --rate (10), --count (0 means continuous), --csv, --queue-url, --http-timeout (3s), --retry-min (100ms), --retry-max (2s), --shutdown-timeout (20s), and --log-level (info). Retries use bounded jitter and preserve the entire event. Each process has a new random producer identity and independently replays all source rows. Required headers are validated; malformed/oversized measurements are counted/skipped; an entirely invalid/empty pass fails clearly. Source values and labels are preserved, GPU UUID is identity, and processed_at is UTC microsecond processing time rather than the historical CSV timestamp.

Consumer defaults are --mode durable, --action ack, --count 100, --timeout 30s (acquisition deadline), --lease-wait 1s, --http-timeout 3s, --shutdown-timeout 20s (maximum pending receipt phase), and --processing-delay 0. A pending receipt may finish within its bounded phase after acquisition cancellation; blocked OS output/disk I/O cannot be forcibly canceled safely. Long-poll wait must be shorter than HTTP timeout. Keep simulated processing within lease duration; renewal is not implemented.

## Protocol and diagnostics

See [Day 2 protocol](docs/DAY2_PROTOCOL.md) for full wire schemas, status codes, retry windows, clock assumptions, storage accounting, cleanup and shutdown behavior.

```text
CSV -> streamer -> durable queue -> leased diagnostic consumer -> output -> ACK
                                    |                              |
                                    +---- expiry / retry NACK <----+
```

Endpoints: POST /internal/v1/messages, /leases, /acks, /nacks; GET /healthz and /internal/v1/stats. Destructive POST /internal/v1/receive returns 410 in durable mode; it is never silently reinterpreted as a lease.

Day 1 remains available explicitly with `./bin/queue --mode memory` and `./bin/testconsumer --mode memory`. Its receive operation is destructive and broker restart loses its data. `--dedup-ttl` applies only to memory mode; durable mode uses --completion-ttl. Historical behavior is in docs/DAY1_PROTOCOL.md.

## Environment and remaining milestones

The existing environment commands are make doctor, make tools, make cluster-up and make cluster-down. See docs/ENVIRONMENT_STATUS.md for verified Mac/Kubernetes setup. The Colima Docker context is colima-gpu-telemetry; Kubernetes context/Minikube profile is gpu-telemetry.

Future packaging must use one broker StatefulSet replica, an internal port-8081 Service, and persistent storage retained on uninstall, with a 30-second pod grace period against the default 20-second application timeout. No Helm deployment, image publishing, Git staging or commit is performed by the implementation workflow unless requested.

Day 3: production collector and PostgreSQL with commit-before-ACK and event_id uniqueness are implemented; see docs/DAY3_VERIFICATION.md for the verification status. The GPU REST API and generated OpenAPI are implemented in Day 4. Later: Dockerfiles, Helm, scaling/performance demonstrations. Replication, lease renewal, operator quarantine tooling, live compaction, UI and HPA are not implemented.

See docs/EXECUTION_PLAN.md, docs/adr/0002-durable-single-broker.md and docs/AI_USAGE.md for decisions, scope and exact prompt records.

## Day 3 quick start

Run `make postgres-up`, set DATABASE_URL as documented in docs/DAY3_COLLECTOR.md, then `make build migrate`. Run bin/queue, bin/collector and bin/streamer in separate terminals. The production collector persists to PostgreSQL; do not run the diagnostic testconsumer alongside it because that consumer ACKs without database writes. Run `make demo-day3` for isolated real-PostgreSQL verification. No automatic deletion/retention cleanup is implemented.

## Day 4 API quick start

With the Day 3 database running and migrated:

```sh
export DATABASE_URL='postgres://telemetry:gpu-local-dev@127.0.0.1:15432/telemetry?sslmode=disable'
make build openapi check-openapi
./bin/api
```

The default address is http://127.0.0.1:8080. Use GET /api/v1/gpus and GET /api/v1/gpus/{id}/telemetry, optionally with RFC3339 start_time/end_time. Bounds apply inclusively to processed_at. Unknown GPU is 404, malformed bounds are 400, filtered-empty results are [], and database/resource failures are 503. There is no pagination or hidden row limit. Results are prepared row-by-row in a bounded temporary file before transmission so mid-query errors cannot produce a misleading partial 200. The default response budget is 64MiB; exceeding it returns 503, never truncated data.

See /docs for the generated interactive reference, /openapi.json or /openapi.yaml for the live specification, and api/openapi.yaml for the offline generated artifact. No database is needed for make openapi or make check-openapi. API_ environment variables configure the API (for example API_LISTEN and API_POOL_MAX); DATABASE_URL supplies the database connection. Full settings and curl examples are in docs/DAY4_API.md. Run make demo-day4 for isolated PostgreSQL integration tests. No schema migration, write endpoint or data deletion is part of Day 4.

## Day 5 local deployment quick start

```sh
make cluster-up
make docker-build
make minikube-load
make helm-check helm-template
make helm-install
make helm-verify
make helm-scale
```

The shared Dockerfile produces queue, streamer, collector, api and dedicated migrate images under gpu-telemetry/<component>:dev. IMAGE_REPOSITORY/IMAGE_TAG override naming; PLATFORM defaults to linux/arm64. The Helm chart is deploy/helm/gpu-telemetry. Defaults target release gpu-telemetry in namespace gpu-telemetry-day5, with an explicit gpu-telemetry Kubernetes context. No image push is performed.

Fresh installs run PostgreSQL bootstrap first, then a full upgrade with a pre-upgrade migration. Do not skip bootstrap or revert a completed release to bootstrapOnly=true. Normal upgrades use `helm upgrade gpu-telemetry deploy/helm/gpu-telemetry --kube-context gpu-telemetry -n gpu-telemetry-day5 --reuse-values --set bootstrapOnly=false --wait --timeout 5m`. Build/load a new image tag and set images.tag when updating code.

Inspect with `kubectl --context gpu-telemetry -n gpu-telemetry-day5 get pods,jobs,services,pvc` and `kubectl --context gpu-telemetry -n gpu-telemetry-day5 logs job/gpu-telemetry-migrate`. Use `kubectl --context gpu-telemetry -n gpu-telemetry-day5 port-forward service/gpu-telemetry-api 8080:8080`, then curl /healthz, /readyz, /api/v1/gpus and /api/v1/gpus/RETURNED_UUID/telemetry.

Uninstall with `helm uninstall gpu-telemetry --kube-context gpu-telemetry -n gpu-telemetry-day5`; PVCs and the generated Secret remain. Do not delete the namespace to preserve them. Full build/load/install/upgrade/uninstall/logging/curl/scaling commands, Secret setup and retention caveats are in docs/DAY5_PACKAGING.md. For routine shutdown use make cluster-down.
