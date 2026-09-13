# GPU Telemetry chart

Use `make helm-install` from the repository root after building/loading the five application images. A fresh installation first bootstraps PostgreSQL (bootstrapOnly=true), then upgrades the same release with bootstrapOnly=false so its migration hook can connect before the applications start. Plain helm install with defaults provisions only the database. Do not directly install full mode into an empty namespace.

Generated credentials are development-only and retained with the PVCs; existing Secret injection is supported. Queue replicas are fixed at one. All services are internal. See docs/DAY5_PACKAGING.md in the project repository for the complete runbook, configuration, security and retention limits. This chart has no dependencies, Ingress or automatic data deletion.
