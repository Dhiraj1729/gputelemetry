# Day 2 implementation and verification

Day 2 is complete: the Go queue now persists messages in bbolt and supports leased delivery, ACK/NACK, retry delays, quarantine, persistent deduplication, backpressure, bounded cleanup, structured logging and graceful shutdown. The diagnostic consumer uses the new protocol; the CSV streamer retains its existing publish contract.

## Verification results

Executed successfully: `make build vet test race coverage demo demo-day2`.

- Build, vet, unit tests and race detector passed.
- Overall unit coverage: **76.4%**; queue package: **81.7%**. CLI entrypoints are exercised by separate subprocess tests, outside the unit coverage profile.
- Day 1 memory regression and streamer signal/outage tests passed.
- Day 2 tests use real broker/streamer/consumer processes, temporary databases and SIGKILL/restart.
- Verified exclusive database lock, ambiguous publish retry deduplication, consumer crash before ACK, persisted leases, expiry/redelivery with a new token, stale ACK/NACK rejection and ACK persistence across restart.
- Final Day 2 process-demo totals: accepted=21, acked=21, ready=0, leased=0, completed=21. This comprises one recovery-test event plus 20 CSV events.
- Injected transaction failures verify rollback and error responses; these do not simulate physical power loss or all filesystem failure modes.

## Run the automated demonstration

```sh
cd /Users/dhirajsriharsha/Preparation/ProjectMsgQueue
make build
make demo-day2
```

Expect `PASS` and the recovery checks listed above. This test uses temporary files and ports, cleans up its own processes and does not modify your manual `work/queue.db`. Docker and Kubernetes are unnecessary for these tests.

For the complete suite:

```sh
make test race vet coverage demo
open coverage/coverage.html
```

## Manual test

Run the following from the project directory in three terminals, after `make build`.

Terminal 1:

```sh
./bin/queue --db work/queue.db --listen 127.0.0.1:8081
```

Terminal 2:

```sh
./bin/streamer --csv Problemstatement/dcgm_metrics_20250718_134233.csv \
  --queue-url http://127.0.0.1:8081 --count 100
```

Terminal 3:

```sh
mkdir -p work
./bin/testconsumer --queue-url http://127.0.0.1:8081 \
  --count 100 --timeout 30s > work/received.jsonl
wc -l work/received.jsonl
curl -s http://127.0.0.1:8081/internal/v1/stats | jq
```

Expect 100 output lines. On a fresh database, expect accepted=100, acked=100, ready=0, leased=0, completed=100. Existing database totals are cumulative. Stop the broker with Ctrl-C; restart using the same database path to retain state.

## Scope and limits

The broker is single-instance and requires its database storage to survive. Delivery is at-least-once; the future collector must commit idempotent storage writes before ACK. Completed IDs have bounded retention (default one hour). Quarantine is retained indefinitely within configured limits. Lease renewal is not implemented. Logical byte limits do not cap exact filesystem usage; bbolt reuses free pages without automatically shrinking. The diagnostic consumer writes JSON output and is not a transactional sink.

Protocol details: docs/DAY2_PROTOCOL.md. Design decision: docs/adr/0002-durable-single-broker.md. Commands and flags: README.md. AI provenance: docs/AI_USAGE.md.

Git status was reviewed. Existing Day 1 work was preserved; all changes remain unstaged and uncommitted. No Docker image publishing or Helm deployment occurred. Day 3 is the production collector and PostgreSQL integration with event_id uniqueness and commit-before-ACK.
