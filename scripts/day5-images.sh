#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
context="${DOCKER_CONTEXT:-colima-gpu-telemetry}"
repo="${IMAGE_REPOSITORY:-gpu-telemetry}"
tag="${IMAGE_TAG:-dev}"
platform="${PLATFORM:-linux/arm64}"
profile="${MINIKUBE_PROFILE:-gpu-telemetry}"
case "$platform" in linux/arm64|linux/amd64) ;; *) echo 'Supported local platforms: linux/arm64 or linux/amd64' >&2; exit 1;; esac
case "${1:-}" in
 build)
  docker --context "$context" info >/dev/null
  for app in queue streamer collector api migrate; do
    docker --context "$context" build --platform "$platform" --target "$app" \
      -f deploy/docker/Dockerfile -t "$repo/$app:$tag" .
  done
  docker --context "$context" image inspect --format '{{.RepoTags}} {{.Os}}/{{.Architecture}} user={{.Config.User}} id={{.Id}}' \
    "$repo/queue:$tag" "$repo/streamer:$tag" "$repo/collector:$tag" "$repo/api:$tag" "$repo/migrate:$tag"
  ;;
 load)
  for app in queue streamer collector api migrate; do
    DOCKER_CONTEXT="$context" minikube -p "$profile" image load --daemon "$repo/$app:$tag"
  done
  ;;
 *) echo 'Usage: bash scripts/day5-images.sh build|load' >&2; exit 1;;
esac
