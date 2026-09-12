#!/usr/bin/env bash
set -uo pipefail
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"
missing=0
for tool in go git colima docker minikube kubectl helm jq rg; do
  if command -v "$tool" >/dev/null 2>&1; then
    printf 'FOUND %s: %s\n' "$tool" "$(command -v "$tool")"
  else
    printf 'MISSING %s\n' "$tool"
    missing=1
  fi
done
if ! xcode-select -p; then missing=1; fi
if [[ "$missing" -ne 0 ]]; then exit 1; fi
go version
git --version
colima version
docker --version
docker buildx version
minikube version
kubectl version --client
helm version --short
