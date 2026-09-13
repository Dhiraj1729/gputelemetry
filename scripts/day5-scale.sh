#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
release="${RELEASE:-gpu-telemetry}"
namespace="${NAMESPACE:-gpu-telemetry-day5}"
context="${KUBE_CONTEXT:-gpu-telemetry}"
k=(kubectl --context "$context" -n "$namespace")
streamers=$("${k[@]}" get deployment "$release-streamer" -o jsonpath='{.spec.replicas}')
collectors=$("${k[@]}" get deployment "$release-collector" -o jsonpath='{.spec.replicas}')
# Update Helm's values as well as live replicas to avoid configuration drift.
helm upgrade "$release" deploy/helm/gpu-telemetry --kube-context "$context" -n "$namespace" \
 --reuse-values --set bootstrapOnly=false --set streamer.replicas=2 --set collector.replicas=2 --wait --timeout 5m
bash scripts/day5-verify.sh
helm upgrade "$release" deploy/helm/gpu-telemetry --kube-context "$context" -n "$namespace" \
 --reuse-values --set bootstrapOnly=false --set "streamer.replicas=$streamers" --set "collector.replicas=$collectors" --wait --timeout 5m
[[ $("${k[@]}" get statefulset "$release-queue" -o jsonpath='{.spec.replicas}') == 1 ]]
printf 'PASS: scaled streamer/collector to two and restored their initial replica counts; queue remained one.\n'
