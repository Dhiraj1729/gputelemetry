# GPU Telemetry Pipeline Test Plan

This plan validates the code, Helm deployment, REST API, horizontal scaling, durable queue behavior and recovery characteristics of the GPU telemetry pipeline. The recorded results and raw command output are included in this repository.

## Evidence conventions

- Raw evidence: [`docs/test-artifacts`](test-artifacts)
- Result summary and interpretation: [`DAY6_VERIFICATION.md`](DAY6_VERIFICATION.md)
- Run commands from the repository root.
- Do not capture Kubernetes Secret values, database passwords, tokens or kubeconfig contents.
- Preserve failed output before retrying; document a limitation instead of presenting a failed attempt as a pass.

The recorded September 2026 run used the namespace `gpu-telemetry-day5`. The current deployment default is `gpu-telemetry`. This explains the older namespace in the evidence; new runs should use the current default unless testing an existing installation.

## Common setup

Set the deployment identifiers once in each new terminal:

```sh
export RELEASE=gpu-telemetry
export NAMESPACE=gpu-telemetry
export KUBE_CONTEXT=gpu-telemetry
```

Use the full `kubectl` form in saved evidence so commands remain easy to understand:

```sh
kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" get pods
```

## Test matrix

| Case | Purpose | Recorded result | Primary evidence |
| --- | --- | --- | --- |
| TC01 | Build, unit tests, race detector, coverage and OpenAPI freshness | Pass | [`TC01-terminal.txt`](test-artifacts/TC01-terminal.txt) |
| TC02 | Helm health and end-to-end ingestion | Pass | [`TC02-terminal.txt`](test-artifacts/TC02-terminal.txt) |
| TC03 | API contract, ordering and inclusive time bounds | Pass | [`TC03-terminal.txt`](test-artifacts/TC03-terminal.txt) |
| TC04 | Live scaling to three streamers and three collectors | Pass | [`TC04-helm-verify-3x3.txt`](test-artifacts/TC04-helm-verify-3x3.txt) |
| TC05 | Brief maximum exercise at 10 streamers and 10 collectors | Pass | [`TC05-ten-replica-verify.txt`](test-artifacts/TC05-ten-replica-verify.txt) |
| TC06 | Queue backlog and recovery after pausing collectors | Pass | [`TC06-queue-stat-comparison.json`](test-artifacts/TC06-queue-stat-comparison.json) |
| TC07 | Queue pod restart with PVC-backed state | Pass | [`TC07-helm-verify.txt`](test-artifacts/TC07-helm-verify.txt) |
| TC08 | Collector interruption and idempotent continuation | Pass | [`TC08-helm-verify.txt`](test-artifacts/TC08-helm-verify.txt) |
| TC09 | Restore the normal 1/1 state and capture a final snapshot | Pass | [`TC09-final-helm-verify.txt`](test-artifacts/TC09-final-helm-verify.txt) |

## TC01 — Automated regression and coverage

Purpose: confirm that the code builds, automated tests pass, the race detector reports no races, coverage is generated and the committed OpenAPI specification is current.

```sh
make build
make test
make race
make coverage
make check-openapi
```

Pass criteria:

- Every command exits successfully.
- The race detector reports no data races.
- `coverage/coverage.html` and `coverage/coverage.out` are generated.
- The generated OpenAPI contract matches `api/openapi.yaml`.

Recorded result: pass, with 69.2% total statement coverage. The raw terminal output is in [`TC01-terminal.txt`](test-artifacts/TC01-terminal.txt).

## TC02 — Helm deployment and end-to-end ingestion

Purpose: verify the installed Helm release, migrations, PVCs, application workloads, queue acknowledgements, PostgreSQL persistence and non-empty API responses.

```sh
make helm-verify
kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" \
  get pods,jobs,services,pvc -o wide
```

Pass criteria:

- `make helm-verify` ends with `PASS:`.
- The migration Job is complete and workloads are healthy.
- Queue and PostgreSQL PVCs are bound.
- Queue ACK and database row counts advance.
- The API returns GPU and telemetry data.

Evidence: [`TC02-terminal.txt`](test-artifacts/TC02-terminal.txt), [`day5-gpus.json`](test-artifacts/day5-gpus.json), [`day5-telemetry.json`](test-artifacts/day5-telemetry.json) and [`day5-queue-stats.json`](test-artifacts/day5-queue-stats.json).

## TC03 — API contract and inclusive time filtering

Purpose: verify GPU listing, ordered telemetry results and inclusive `start_time`/`end_time` filtering.

Start a port-forward:

```sh
kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" \
  port-forward service/gpu-telemetry-api 18080:8080
```

In another terminal:

```sh
curl -fsS http://127.0.0.1:18080/api/v1/gpus | jq
curl -fsS "http://127.0.0.1:18080/api/v1/gpus/GPU_UUID/telemetry" | jq
curl -fsSG "http://127.0.0.1:18080/api/v1/gpus/GPU_UUID/telemetry" \
  --data-urlencode "start_time=RFC3339_TIME" \
  --data-urlencode "end_time=RFC3339_TIME" | jq
```

Pass criteria:

- GPU listing is non-empty.
- Telemetry is ordered by `processed_at`, then `event_id`.
- Equal start and end bounds return the matching event.

Evidence: [`TC03-gpus.json`](test-artifacts/TC03-gpus.json), [`TC03-telemetry-full.json`](test-artifacts/TC03-telemetry-full.json), [`TC03-telemetry-bounded.json`](test-artifacts/TC03-telemetry-bounded.json) and [`TC03-terminal.txt`](test-artifacts/TC03-terminal.txt).

## TC04 — Live scaling to 3/3

Purpose: demonstrate that streamers and collectors scale independently while ingestion and API access continue.

```sh
helm upgrade "$RELEASE" deploy/helm/gpu-telemetry \
  --kube-context "$KUBE_CONTEXT" -n "$NAMESPACE" --reuse-values \
  --set bootstrapOnly=false \
  --set streamer.replicas=3 --set collector.replicas=3 \
  --wait --timeout 5m
make helm-verify
```

Restore the normal replica counts after the check:

```sh
helm upgrade "$RELEASE" deploy/helm/gpu-telemetry \
  --kube-context "$KUBE_CONTEXT" -n "$NAMESPACE" --reuse-values \
  --set bootstrapOnly=false \
  --set streamer.replicas=1 --set collector.replicas=1 \
  --wait --timeout 5m
```

Pass criteria: three streamer and three collector pods become Ready, verification passes at 3/3, and the deployment returns to 1/1. See [`TC04-scaled-state.txt`](test-artifacts/TC04-scaled-state.txt) and [`TC04-final-state.txt`](test-artifacts/TC04-final-state.txt).

## TC05 — Brief maximum-scale exercise at 10/10

Purpose: exercise the assignment ceiling of ten streamers and ten collectors. This is a short local capability check, not a sustained throughput benchmark.

Increase the active queue and retained-ID capacities for the brief scale test, then scale through Helm to ten replicas of each worker. Observe the system for approximately 60 seconds, run `make helm-verify`, and immediately restore one streamer and one collector. Stop early if the laptop experiences memory pressure or unstable pods.

Pass criteria:

- Ten streamer and ten collector pods become Ready.
- The queue, PostgreSQL and API remain healthy single instances.
- Queue ACKs, persistence and API responses continue.
- Verification passes at 10/10.
- The deployment is restored successfully to 1/1.

Recorded evidence: [`TC05-low-load-config.txt`](test-artifacts/TC05-low-load-config.txt), [`TC05-ten-replica-state.txt`](test-artifacts/TC05-ten-replica-state.txt), [`TC05-ten-replica-runtime-samples.txt`](test-artifacts/TC05-ten-replica-runtime-samples.txt), [`TC05-ten-replica-verify.txt`](test-artifacts/TC05-ten-replica-verify.txt) and [`TC05-final-one-replica-verify.txt`](test-artifacts/TC05-final-one-replica-verify.txt).

## TC06 — Backlog and collector recovery

Purpose: show that the queue retains messages while collectors are absent and drains after collectors return.

1. Port-forward the queue service on local port `18081`.
2. Capture `/internal/v1/stats`.
3. Upgrade the Helm release with `collector.replicas=0`.
4. Wait approximately 30 seconds and capture queue statistics again.
5. Restore `collector.replicas=1`.
6. Capture statistics until the ready backlog drains, then run `make helm-verify`.

Pass criteria: backlog grows during the pause, drains after restoration, ACK count increases, and final verification passes. See [`TC06-queue-stat-comparison.json`](test-artifacts/TC06-queue-stat-comparison.json) and [`TC06-helm-verify.txt`](test-artifacts/TC06-helm-verify.txt).

## TC07 — Queue pod restart and durable recovery

Purpose: verify that restarting the single queue pod does not delete its PVC-backed durable state.

```sh
kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" \
  get pod "$RELEASE-queue-0",pvc "$RELEASE-queue-data" -o wide
kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" \
  delete pod "$RELEASE-queue-0"
make helm-verify
```

Pass criteria: Kubernetes recreates the pod, the same PVC remains bound, durable statistics survive and end-to-end verification passes. See [`TC07-pre-restart-state.txt`](test-artifacts/TC07-pre-restart-state.txt), [`TC07-post-restart-state.txt`](test-artifacts/TC07-post-restart-state.txt) and [`TC07-helm-verify.txt`](test-artifacts/TC07-helm-verify.txt).

## TC08 — Collector interruption and safe continuation

Purpose: verify that Kubernetes replaces a deleted collector and that event-ID uniqueness prevents duplicate persistence during recovery.

1. Capture the collector pod and PostgreSQL total/distinct event counts.
2. Delete one collector pod.
3. Wait for its replacement to become Ready.
4. Run `make helm-verify`.
5. Capture the database counts again.

Pass criteria: the replacement becomes Ready, ingestion resumes, verification passes, row counts advance and total rows equal distinct event IDs. See [`TC08-pre-interruption-state.txt`](test-artifacts/TC08-pre-interruption-state.txt), [`TC08-post-interruption-db-counts.txt`](test-artifacts/TC08-post-interruption-db-counts.txt) and [`TC08-helm-verify.txt`](test-artifacts/TC08-helm-verify.txt).

## TC09 — Final restore and snapshot

Purpose: leave the deployment in its normal one-streamer/one-collector state and capture a final reviewable snapshot.

```sh
kubectl --context "$KUBE_CONTEXT" -n "$NAMESPACE" \
  get deployments,statefulsets,pods,pvc -o wide
make helm-verify
helm list --kube-context "$KUBE_CONTEXT" -n "$NAMESPACE"
helm get values "$RELEASE" --kube-context "$KUBE_CONTEXT" -n "$NAMESPACE"
```

Pass criteria: streamer and collector desired/Ready counts are 1/1, the queue remains one replica, both PVCs remain bound, and final verification passes. See [`TC09-final-kubernetes-state.txt`](test-artifacts/TC09-final-kubernetes-state.txt), [`TC09-final-helm-state.txt`](test-artifacts/TC09-final-helm-state.txt) and [`TC09-final-helm-verify.txt`](test-artifacts/TC09-final-helm-verify.txt).

## Interpretation limits

- TC05 demonstrates a short 10/10 functional run on one laptop; it is not a throughput or endurance benchmark.
- Equal PostgreSQL total/distinct event counts demonstrate unique persisted event identities, not exactly-once message delivery.
- Queue statistics and backlog drainage demonstrate recovery behavior but do not constitute an exhaustive no-loss proof.
- The queue and PostgreSQL each remain single-replica StatefulSets and are not highly available.
