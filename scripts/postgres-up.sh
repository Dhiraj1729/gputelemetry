#!/usr/bin/env bash
set -euo pipefail
context="${DAY3_DOCKER_CONTEXT:-colima-gpu-telemetry}"
image="${DAY3_POSTGRES_IMAGE:-postgres:18}"
name=gpu-telemetry-postgres
if ! docker --context "$context" info >/dev/null 2>&1; then
  echo "Start the existing VM with: colima start gpu-telemetry" >&2
  exit 1
fi
if docker --context "$context" container inspect "$name" >/dev/null 2>&1; then
  role=$(docker --context "$context" inspect --format '{{ index .Config.Labels "io.gpu-telemetry.role" }}' "$name")
  if [[ "$role" != postgres-day3 ]]; then
    echo "Existing container $name is not managed by this script; refusing to change it." >&2
    exit 1
  fi
  docker --context "$context" start "$name"
else
  docker --context "$context" run -d --name "$name" \
    --label io.gpu-telemetry.role=postgres-day3 \
    --memory 512m --cpus 1 \
    -p 127.0.0.1:15432:5432 \
    -e POSTGRES_DB=telemetry -e POSTGRES_USER=telemetry \
    -e "POSTGRES_PASSWORD=${POSTGRES_PASSWORD:-gpu-local-dev}" \
    --mount type=volume,source=gpu-telemetry-postgres18,target=/var/lib/postgresql \
    "$image"
fi
for ((i=0; i<60; i++)); do
  if docker --context "$context" exec "$name" pg_isready -U telemetry -d telemetry >/dev/null 2>&1; then
    echo 'PostgreSQL ready on 127.0.0.1:15432. Data is retained in volume gpu-telemetry-postgres18.'
    exit 0
  fi
  sleep 1
done
echo 'PostgreSQL did not become ready within 60 seconds; inspect container logs.' >&2
exit 1
