# Workflow naming migration

Public commands now describe their function rather than implementation days. Historical records, test package directories and evidence filenames in this documentation retain their original names so earlier results remain traceable.

## Existing deployment: preserve namespace and data

No deployed namespace or resource has been changed by this repository edit. The new default namespace is `gpu-telemetry`. The original installation uses `gpu-telemetry-day5`. For that existing installation, run from the repository root:

```sh
export NAMESPACE=gpu-telemetry-day5
make helm-verify
# Only when an upgrade is intended:
make helm-install
```

Keep this environment variable in every terminal used for deployment operations. Release/context names remain `gpu-telemetry`. Changing a namespace creates a separate release/storage scope; it does not migrate data or rename the existing namespace. Do not delete PVCs or reinstall merely to change a cosmetic name. A fresh clone acceptance test should state whether it targets a new installation or the original release.

## Mapping

| Previous | Current |
| --- | --- |
| `scripts/day5-images.sh` | `scripts/images.sh` |
| `scripts/day5-install.sh` | `scripts/helm-install.sh` |
| `scripts/day5-verify.sh` | `scripts/helm-verify.sh` |
| `scripts/day5-scale.sh` | `scripts/helm-scale.sh` |
| `make demo-day2` | `make test-queue-recovery` |
| `make demo-day3` | `make test-collector-integration` |
| `make demo-day4` | `make test-api-integration` |
| `work/day5-rendered.yaml` | `work/helm-rendered.yaml` |
| `work/day5-*.json`, port-forward logs | `work/verification-*.json`, port-forward logs |
| `DAY3_DOCKER_CONTEXT`, `DAY4_DOCKER_CONTEXT` | `DOCKER_CONTEXT` |
| `DAY3_POSTGRES_IMAGE`, `DAY4_POSTGRES_IMAGE` | `POSTGRES_IMAGE` |

Historical Make target aliases still work. Direct calls to renamed script paths must use the new paths. Test environment variables retain fallback support for the historical names; the neutral variables take precedence. The standalone PostgreSQL helper accepts the previous ownership label for existing containers and uses `postgres-dev` for new containers; container and volume names are unchanged.

Operational runbooks have updated commands. Historical verification reports, prompt records and workStructure remain historical evidence, not the preferred current command reference. No images, application logic, storage paths, release names or Kubernetes selectors changed. No Git commit/push or live deployment was performed.
