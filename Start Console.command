#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
if [[ "$(uname -m)" != arm64 ]]; then
  echo 'This release supports Apple Silicon Macs only.'
  read -r -p 'Press Return to close.'
  exit 1
fi
exec "$ROOT/console/telemetry-console" --root "$ROOT"
