#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
release="${RELEASE:-gpu-telemetry}"
namespace="${NAMESPACE:-gpu-telemetry}"
context="${KUBE_CONTEXT:-gpu-telemetry}"
repo="${IMAGE_REPOSITORY:-gpu-telemetry}"
tag="${IMAGE_TAG:-dev}"
chart=deploy/helm/gpu-telemetry
# Keep script and chart names identical (the chart caps release names at 40).
[[ "$release" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && ${#release} -le 40 ]] || { echo 'Release must be a DNS label of at most 40 characters' >&2; exit 1; }
kubectl --context "$context" cluster-info >/dev/null
helm_args=(--kube-context "$context" -n "$namespace")
value_args=(--set-string "images.repository=$repo" --set-string "images.tag=$tag")
if [[ -n "${VALUES_FILE:-}" ]]; then value_args+=(-f "$VALUES_FILE"); fi
# Helm list failing (e.g. RBAC/network) is fatal, not treated as a new release.
releases=$(helm list "${helm_args[@]}" --filter "^$release$" --deployed --failed --pending --uninstalling --output json)
if ! jq -e --arg name "$release" '.[] | select(.name == $name)' <<<"$releases" >/dev/null; then
  helm install "$release" "$chart" "${helm_args[@]}" --create-namespace \
    "${value_args[@]}" --set bootstrapOnly=true --wait --timeout 5m
fi
# PostgreSQL must already exist before the pre-upgrade migration hook.
kubectl --context "$context" -n "$namespace" rollout status "statefulset/$release-postgresql" --timeout=180s
helm upgrade "$release" "$chart" "${helm_args[@]}" --reuse-values \
  "${value_args[@]}" --set bootstrapOnly=false --wait --timeout 5m
kubectl --context "$context" -n "$namespace" wait --for=condition=complete "job/$release-migrate" --timeout=180s
