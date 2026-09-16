# Test Artifacts

This directory contains the raw terminal output and JSON responses referenced by the [test plan](../TEST_PLAN.md) and [test-results summary](../DAY6_VERIFICATION.md).

| Test case | Evidence prefix | What it demonstrates |
| --- | --- | --- |
| TC01 | `TC01-*` | Build, automated tests, race detector, coverage and OpenAPI freshness |
| TC02 | `TC02-*`, `day5-*` | Helm health, persistence, queue activity and API responses |
| TC03 | `TC03-*` | GPU listing, telemetry ordering and inclusive time filtering |
| TC04 | `TC04-*` | Live scaling to three streamers and three collectors, then restoration |
| TC05 | `TC05-*` | Brief 10/10 functional exercise and restoration to 1/1 |
| TC06 | `TC06-*` | Queue backlog growth and drainage after a collector pause |
| TC07 | `TC07-*` | Queue pod replacement with PVC-backed state retained |
| TC08 | `TC08-*` | Collector replacement and unique persisted event identities |
| TC09 | `TC09-*`, `day5-*` | Final healthy 1/1 state and end-to-end verification |

These files are execution evidence, not commands to run blindly. Cluster identifiers, pod names, timestamps, counters and the recorded namespace reflect the original test environment and will differ in a new deployment.

No Kubernetes Secret values, database passwords, access tokens or kubeconfig contents are intentionally included. Review raw evidence again before publishing if files are replaced or added.
