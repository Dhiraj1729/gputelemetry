# Day 3 verification — 12 September 2026

**Current status: Day 3 verification passed, including the user-run PostgreSQL integration suite.** The earlier agent-side runtime failure below is retained as historical evidence.

## Commands actually executed

| Command | Result |
| --- | --- |
| go get github.com/jackc/pgx/v5/pgxpool@v5.11.0; go mod tidy | Passed; driver and transitive versions pinned |
| gofmt on all new/changed Go files | Completed |
| make build vet test race coverage demo | Passed, exit 0 |
| go test -tags=postgres_integration ./tests/day3 -run '^$' | Passed compilation; deliberately ran no database tests |
| bash -n scripts/postgres-up.sh | Passed syntax check; database startup itself is not verified |
| git diff --check | Passed |
| make demo-day3 | Failed at Docker container startup; no database scenario ran |

Collector unit coverage is **91.5%**. Overall unit coverage is **71.9%**; PostgreSQL package coverage is **32.7%** and migration coverage is 0% in this profile. This reflects the unexecuted real-database paths; do not present them as covered. Command entrypoints also have 0% in the unit profile. Day 1/Day 2 commands ran separately in subprocess regression tests.

Unit tests verify commit-before-ACK, no ACK on database failure, bounded exponential retries, ACK retry without another insert, semantic-invalid quarantine, stale receipts, processing deadlines, worker bounds, graceful drain, health/readiness, configuration precedence and credential-safe help. Day 1 memory flow, streamer SIGTERM/outage and Day 2 broker crash/restart tests passed.

## Runtime blocker

The existing Colima profile was stopped. The first start attempt was denied access to its configuration; scoped access to ~/.colima and ~/.docker was then granted. The next attempt reached Apple's virtualization driver but failed with:

`VZErrorDomain Code=2: Invalid virtual machine configuration. Virtualization is not available on this hardware.`

This describes the agent execution environment's result, not a finding that the user's Mac lacks virtualization support. The expected Docker context was unavailable afterward. A request was sent to start `colima start gpu-telemetry` in the user's Mac Terminal. No successful startup was observed during this verification record.

Full logs: work/day3-checks.txt and work/day3-postgres-checks.txt.

## Complete the remaining gate

From the user's Mac Terminal:

```sh
cd /Users/dhirajsriharsha/Preparation/ProjectMsgQueue
colima start gpu-telemetry
docker --context colima-gpu-telemetry info
make demo-day3
```

The test creates an isolated PostgreSQL container and broker database. It does not use or delete the manual PostgreSQL volume. It must pass migration reruns, normal persistence, concurrent deduplication, metadata ordering, atomic rollback, lock timeout/no ACK, recovery, quarantine, real collector crash after commit with ACK blocked, and the CSV streamer/collector flow. Inspect failures and fix them before calling Day 3 fully verified. Image/server versions are logged on a successful startup; no PostgreSQL version has been claimed as tested yet.

See DAY3_COLLECTOR.md for the manual setup, run commands, environment variables and retention policy. API/OpenAPI remain Day 4. No commit, push, image publish, Helm deployment or automatic data deletion was performed.


## Subsequent user-provided success evidence

The user supplied Mac Terminal output showing `colima start gpu-telemetry`, Docker daemon availability, and `make demo-day3` passing. PostgreSQL was 18.6 (Debian 18.6-1.pgdg13+2); the logged image ID was sha256:4ef4dbc939d61acea57712655ddb4b4ab27419c913f94cca0cd57cb3ea3c2280. TestDay3Postgres passed in 19.80s (package 20.399s).

All subtests passed: normal/concurrent duplicates, metadata order/atomic rollback, DB lock timeout with no ACK and recovery, invalid quarantine, real collector SIGKILL after commit before ACK, and 20 real CSV events persisted/ACKed with clean collector SIGTERM. The proxy context-canceled message occurred during the intentional crash scenario and the scenario passed. This closes the Day 3 gate based on user-provided output; it is not an agent rerun or evidence that Day 4 tests have passed.
