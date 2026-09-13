#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
release="${RELEASE:-gpu-telemetry}"
namespace="${NAMESPACE:-gpu-telemetry-day5}"
context="${KUBE_CONTEXT:-gpu-telemetry}"
api_port="${LOCAL_API_PORT:-18080}"
queue_port="${LOCAL_QUEUE_PORT:-18081}"
k=(kubectl --context "$context" -n "$namespace")
for app in postgresql queue; do "${k[@]}" rollout status "statefulset/$release-$app" --timeout=180s; done
for app in streamer collector api; do "${k[@]}" rollout status "deployment/$release-$app" --timeout=180s; done
"${k[@]}" wait --for=condition=complete "job/$release-migrate" --timeout=180s
[[ $("${k[@]}" get statefulset "$release-queue" -o jsonpath='{.spec.replicas}') == 1 ]]
for app in postgresql queue; do [[ $("${k[@]}" get pvc "$release-$app-data" -o jsonpath='{.status.phase}') == Bound ]]; done
"${k[@]}" get pods,jobs,services,pvc -l "app.kubernetes.io/instance=$release"
"${k[@]}" logs "job/$release-migrate"
mkdir -p work
"${k[@]}" port-forward "service/$release-api" "$api_port:8080" >work/day5-api-port-forward.log 2>&1 &
api_pid=$!
"${k[@]}" port-forward "service/$release-queue" "$queue_port:8081" >work/day5-queue-port-forward.log 2>&1 &
queue_pid=$!
trap 'kill "$api_pid" "$queue_pid" 2>/dev/null || true; wait "$api_pid" "$queue_pid" 2>/dev/null || true' EXIT
for ((i=0; i<60; i++)); do
 if curl -fsS "http://127.0.0.1:$api_port/readyz" >/dev/null 2>&1; then break; fi
 sleep 1
done
kill -0 "$api_pid"
kill -0 "$queue_pid"
curl -fsS "http://127.0.0.1:$api_port/healthz"
curl -fsS "http://127.0.0.1:$api_port/readyz"
baseline_rows=$("${k[@]}" exec "$release-postgresql-0" -- sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM telemetry"')
baseline_acks=$(curl -fsS "http://127.0.0.1:$queue_port/internal/v1/stats" | jq -er '.acked')
progress=false
for ((i=0; i<60; i++)); do
 current_rows=$("${k[@]}" exec "$release-postgresql-0" -- sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM telemetry"')
 current_acks=$(curl -fsS "http://127.0.0.1:$queue_port/internal/v1/stats" | jq -er '.acked')
 if (( current_rows > baseline_rows && current_acks > baseline_acks )); then progress=true; break; fi
 sleep 1
done
[[ "$progress" == true ]] || { echo 'No new persisted/ACKed events observed within 60 seconds' >&2; exit 1; }
for ((i=0; i<60; i++)); do
 curl -fsS "http://127.0.0.1:$api_port/api/v1/gpus" >work/day5-gpus.json
 if jq -e 'length > 0' work/day5-gpus.json >/dev/null; then break; fi
 sleep 1
done
uuid=$(jq -er '.[0].uuid' work/day5-gpus.json)
uuid_encoded=$(jq -rn --arg id "$uuid" '$id|@uri')
curl -fsS "http://127.0.0.1:$api_port/api/v1/gpus/$uuid_encoded/telemetry" >work/day5-telemetry.json
jq -e 'length > 0' work/day5-telemetry.json >/dev/null
curl -fsS "http://127.0.0.1:$queue_port/internal/v1/stats" >work/day5-queue-stats.json
jq -e '.acked > 0' work/day5-queue-stats.json >/dev/null
"${k[@]}" exec "$release-postgresql-0" -- sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -c "SELECT count(*) AS events, count(DISTINCT event_id) AS unique_events FROM telemetry"'
"${k[@]}" logs "deployment/$release-streamer" --tail=5
"${k[@]}" logs "deployment/$release-collector" --tail=5
printf 'PASS: PVCs bound, migration complete, API data present, queue ACKs observed, queue replicas=1.\n'
