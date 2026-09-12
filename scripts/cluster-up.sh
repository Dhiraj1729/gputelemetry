#!/usr/bin/env bash
set -euo pipefail
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"
# Named profiles isolate this project from other local clusters.
colima start gpu-telemetry --runtime docker --vm-type vz --cpus 4 --memory 6 --disk 40
export DOCKER_CONTEXT=colima-gpu-telemetry
docker info >/dev/null
minikube start -p gpu-telemetry --driver=docker --container-runtime=containerd --cpus=3 --memory=4096
kubectl --context=gpu-telemetry wait --for=condition=Ready node --all --timeout=180s
kubectl --context=gpu-telemetry get nodes -o wide
kubectl --context=gpu-telemetry get storageclass
mkdir -p work
{
  date -u
  go version
  colima version
  docker version
  minikube version
  kubectl --context=gpu-telemetry version
  helm version --short
} > work/environment-versions.txt
echo 'Cluster ready. Version evidence saved in work/environment-versions.txt.'
