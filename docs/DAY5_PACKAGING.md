# Day 5: Docker images and Helm packaging

Current command names and defaults are shown below. Existing installations must keep their original namespace; see [naming migration](NAMING_MIGRATION.md) before running commands. Historical execution evidence remains in the verification documents.

The chart packages the existing applications without changing queue semantics, API contracts, collector behavior or database schema. The reference requests a dedicated migrate image, so there are **five custom images**, plus upstream PostgreSQL.

## Build inputs and image selection

One multi-stage Dockerfile at deploy/docker/Dockerfile shares the Go dependency/compiler stages and has final targets queue, streamer, collector, api and migrate. Each final image contains its own executable; streamer also contains the original CSV at /app/data/metrics.csv. Migration SQL remains embedded in the migrate binary. The CSV is not a ConfigMap.

The declared Go 1.27.1 toolchain is used in a Bookworm builder. Distroless static Debian 13 supplies CA certificates and a non-root user without a shell. All custom final images run as UID/GID 65532. The registry image indexes below were successfully inspected and include linux/arm64 and linux/amd64:

| Purpose | Pinned reference |
| --- | --- |
| Builder | golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b |
| Runtime | gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7 |
| PostgreSQL | postgres:18.6@sha256:4ef4dbc939d61acea57712655ddb4b4ab27419c913f94cca0cd57cb3ea3c2280 |

Builds use CGO_ENABLED=0, Linux target settings, -trimpath, -buildvcs=false and stripped binaries. These are reproducibility-oriented settings, not a claim of byte-identical OCI outputs across all build environments. .dockerignore allows only application sources, module files, embedded migrations, the Dockerfile and the required CSV; Git, work, coverage, credentials and unrelated project inputs stay outside the build context.

```sh
cd /Users/dhirajsriharsha/Preparation/ProjectMsgQueue
# Existing environment workflow: starts Colima and the named Minikube cluster.
make cluster-up
make docker-build
make minikube-load
make helm-check helm-template helm-package
```

Default image names:

```text
gpu-telemetry/queue:dev
gpu-telemetry/streamer:dev
gpu-telemetry/collector:dev
gpu-telemetry/api:dev
gpu-telemetry/migrate:dev
```

They are produced locally only when docker-build succeeds. No push command is part of this workflow. Registry-style naming does not publish images:

```sh
make docker-build IMAGE_REPOSITORY=ghcr.io/YOUR_USER/gpu-telemetry IMAGE_TAG=v1.0.0
make minikube-load IMAGE_REPOSITORY=ghcr.io/YOUR_USER/gpu-telemetry IMAGE_TAG=v1.0.0
make helm-install IMAGE_REPOSITORY=ghcr.io/YOUR_USER/gpu-telemetry IMAGE_TAG=v1.0.0
```

PLATFORM defaults to linux/arm64 for this Mac. PLATFORM=linux/amd64 cross-builds a separate single-platform image, but AMD64 runtime validation and multi-platform publishing are not performed. DOCKER_CONTEXT defaults to colima-gpu-telemetry; MINIKUBE_PROFILE and KUBE_CONTEXT default to gpu-telemetry. Minikube's image store is separate from Colima's; the load command uses --daemon explicitly. Use a new IMAGE_TAG for code changes: rebuilding/reloading dev alone does not change an already-running pod or trigger a Helm rollout.

## Install sequence and migrations

A pre-install hook executes before ordinary chart resources exist. A migration cannot connect to a PostgreSQL StatefulSet that Helm has not created yet. To retain the requested pre-install/pre-upgrade hook contract with one chart, **the supported fresh install is two-phase**:

1. Install the chart with bootstrapOnly=true. This creates the database Secret (or references an existing one), PostgreSQL Service/PVC/StatefulSet and waits for PostgreSQL readiness. No applications or migration Job start yet.
2. Upgrade the same release with bootstrapOnly=false. The migration pre-upgrade hook runs against that ready database. Only after its success does Helm create/update the queue, streamer, collector and API.

`make helm-install` performs both phases on a fresh release and only the full upgrade on subsequent runs:

```sh
make helm-install
make helm-verify
make helm-scale
```

The intended release is **gpu-telemetry**, namespace **gpu-telemetry**, context **gpu-telemetry**. Override RELEASE/NAMESPACE/KUBE_CONTEXT explicitly when needed. Release names are limited to 40 characters to avoid truncated-name collisions. No namespace deletion or automatic failure rollback is performed.

Equivalent initial commands:

```sh
helm install gpu-telemetry deploy/helm/gpu-telemetry \
  --kube-context gpu-telemetry -n gpu-telemetry --create-namespace \
  --set bootstrapOnly=true --wait --timeout 5m
helm upgrade gpu-telemetry deploy/helm/gpu-telemetry \
  --kube-context gpu-telemetry -n gpu-telemetry --reuse-values \
  --set bootstrapOnly=false --wait --timeout 5m
```

Do not fresh-install bootstrapOnly=false into an empty namespace: its pre-install migration would have no database/Secret. Do not switch a completed release back to bootstrapOnly=true: that phase intentionally omits application resources. The bootstrap default is deliberate; plain helm install alone provisions only the database.

For normal upgrades:

```sh
helm upgrade gpu-telemetry deploy/helm/gpu-telemetry \
  --kube-context gpu-telemetry -n gpu-telemetry --reuse-values \
  --set bootstrapOnly=false --set-string images.tag=v1.0.1 --wait --timeout 5m
```

Build/load that tag first. The migration Job is annotated pre-install,pre-upgrade, uses the same database-url Secret key as collector/API, and runs the existing idempotent cmd/migrate. before-hook-creation replaces the preceding hook Job at the next upgrade; the latest successful or failed Job is retained for logs. A failed migration blocks application updates. Existing application replicas may remain available during an upgrade; new application changes are not applied until the hook succeeds. No concurrent automatic migrations run inside collectors or API.

## Resources and persistence

| Component | Workload | Default replicas | Service/storage |
| --- | --- | --- | --- |
| queue | StatefulSet | Exactly 1 | Internal headless ClusterIP-type Service, port 8081; 1Gi PVC |
| streamer | Deployment | 1 | CSV in image; internal queue DNS; continuous count=0 |
| collector | Deployment | 1 | Internal Service/health listener 8082; database Secret |
| api | Deployment | 1 | Internal ClusterIP Service 8080; database Secret; disk emptyDir |
| PostgreSQL | StatefulSet | Exactly 1 | Official image; headless ClusterIP-type Service 5432; 2Gi PVC |
| migrate | Hook Job | One execution per full upgrade | Dedicated image and database Secret |

The queue persists /var/lib/gpu-telemetry/queue.db; the PVC mount gets fsGroup 65532. PostgreSQL runs as UID/GID/fsGroup 999 with PGDATA=/var/lib/postgresql/18/docker, a PVC mounted at /var/lib/postgresql and writable socket/tmp emptyDirs. The pinned upstream image is used directly; there is no PostgreSQL Dockerfile. PostgreSQL major-version upgrades are not automated or supported by simply changing this image value.

All pods disable automatic service-account token mounting and use RuntimeDefault seccomp. Containers disallow privilege escalation, drop capabilities and use read-only root filesystems. The API receives a 384Mi disk emptyDir at /tmp for its bounded response buffers (64Mi per response, four active requests by default). Raising its concurrency/response budget requires matching temporary-storage and ephemeral-storage settings.

Streamers have no invented HTTP health checks. Queue probes use /healthz. Collector/API liveness uses /healthz and readiness uses /readyz, which checks the migration marker/database. PostgreSQL startup/readiness uses pg_isready over TCP; it does not report its temporary initialization socket server as ready. Pods get 30-second termination grace against the default 20-second Go shutdown budget.

StorageClass is omitted by default to select Minikube's default class. Override queue.storage.storageClass and postgresql.storage.storageClass if needed. Queue PVC creation is deferred until the full phase, avoiding a bootstrap wait on an unused claim with WaitForFirstConsumer storage. Resource defaults request about 352Mi RAM and 275m CPU across the five steady workloads; these are scheduling budgets, not measured consumption. Limits and replica counts are configurable in values.yaml. Chart validation rejects queue replicas other than one, invalid application bounds and dedup capacity below queue capacity.

Both PVCs and the generated Secret carry helm.sh/resource-policy: keep. Uninstall stops/removes managed workloads while retaining those resources:

```sh
helm uninstall gpu-telemetry --kube-context gpu-telemetry -n gpu-telemetry
kubectl --context gpu-telemetry -n gpu-telemetry get pvc,secret
```

Do not delete the namespace, Minikube profile or retained PVCs when retaining data. Keep the same release name/namespace and compatible settings for reinstall. The latest migration hook Job may also remain after uninstall; hooks are not ordinary release-managed resources. No automated purge is supplied. PVC retention is not backup and does not protect against node/VM/volume loss or manual namespace deletion. Pod restart/uninstall retention has not been claimed as runtime-verified until the local deployment checks pass.

## Secrets and configuration

The default database.developmentSecret=true is **local development only**: Helm generates a random 48-character password and stores it with the connection URL in a Kubernetes Secret. lookup reuses it on upgrade/reinstall. No password is in values.yaml, source, build arguments or images. Helm's release record still contains the generated Secret manifest; restrict Kubernetes/Helm access appropriately. Do not print live Secret manifests into logs.

For an existing Secret, create it through your normal secret-management process before installation and provide a non-secret values file:

```yaml
bootstrapOnly: false
database:
  developmentSecret: false
  existingSecret: my-database-secret
  username: telemetry
  name: telemetry
```

It must contain postgres-password and database-url. The URL must point to the chart's internal PostgreSQL Service, use the configured username/database and a correctly URI-encoded matching password. With the default release: postgres://USER:PASSWORD@gpu-telemetry-postgresql:5432/DB?sslmode=disable. Collector/API/migrate get DATABASE_URL via secretKeyRef; PostgreSQL gets POSTGRES_PASSWORD from that same Secret. No external PostgreSQL mode is implemented in this milestone.

Pass the file with `make helm-install VALUES_FILE=/path/to/non-secret-values.yaml`. The script forces the correct bootstrap/full phase. Never change database name/username or rotate a password merely by changing values on an initialized volume; database credential/data migrations require a separate reviewed procedure. Supplying database.password in ordinary values is rejected by schema validation.

Helm rendering examples use an existing-secret placeholder so generated passwords are not written to output:

```sh
make helm-template
# Inspect work/helm-rendered.yaml; render-only-secret is a placeholder, not runnable credentials.
```

## Inspect, access and verify

```sh
kubectl --context gpu-telemetry -n gpu-telemetry get pods,jobs,services,pvc
kubectl --context gpu-telemetry -n gpu-telemetry logs job/gpu-telemetry-migrate
kubectl --context gpu-telemetry -n gpu-telemetry logs deployment/gpu-telemetry-streamer --tail=20
kubectl --context gpu-telemetry -n gpu-telemetry logs deployment/gpu-telemetry-collector --tail=20
kubectl --context gpu-telemetry -n gpu-telemetry logs statefulset/gpu-telemetry-queue --tail=20
kubectl --context gpu-telemetry -n gpu-telemetry port-forward service/gpu-telemetry-api 8080:8080
```

In another terminal:

```sh
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS http://127.0.0.1:8080/api/v1/gpus | jq
GPU_UUID='replace-with-a-returned-UUID'
curl -fsS "http://127.0.0.1:8080/api/v1/gpus/$GPU_UUID/telemetry" | jq
curl -fsS http://127.0.0.1:8080/openapi.json >/dev/null
```

`make helm-verify` checks bound PVCs, ready workloads, successful migration, queue replicas=1, advancing PostgreSQL row and queue ACK counts, and nonempty API results. It uses its own port-forwards on 18080/18081 and stops only those processes afterward. Override LOCAL_API_PORT/LOCAL_QUEUE_PORT if occupied. Verification requires active continuous streamers and collectors. It saves evidence under work/verification-*.json/log and prints database row/unique-ID counts. Default queue retention/capacity are unchanged: a sustained long run can exhaust the retained-ID budget and apply backpressure; that is not a packaging throughput guarantee.

`make helm-scale` upgrades streamer/collector to two replicas, runs verification, then restores their initial replica counts through Helm. It checks that queue remains one. If a check fails, the script stops for investigation rather than claiming success or silently resetting a failed deployment. Manual equivalent:

```sh
helm upgrade gpu-telemetry deploy/helm/gpu-telemetry --kube-context gpu-telemetry \
 -n gpu-telemetry --reuse-values --set bootstrapOnly=false \
 --set streamer.replicas=2 --set collector.replicas=2 --wait --timeout 5m
# Restore the initial one-replica defaults after the check:
helm upgrade gpu-telemetry deploy/helm/gpu-telemetry --kube-context gpu-telemetry \
 -n gpu-telemetry --reuse-values --set bootstrapOnly=false \
 --set streamer.replicas=1 --set collector.replicas=1 --wait --timeout 5m
```

Queue replicas cannot be raised; it is a single durable broker, not HA. Delivery remains at-least-once and PostgreSQL event_id uniqueness makes collector persistence idempotent. No Ingress, HPA, dashboard, replication, retention cleanup, image push or chart publication is added. Broader 1/3/10-replica throughput and failure measurements remain Day 6.

After your session, `make cluster-down` stops the named Minikube/Colima profiles while retaining their disk state. See DAY5_VERIFICATION.md for which checks actually ran.

References: [Helm hook ordering and lifecycle](https://helm.sh/de/docs/v3/topics/charts_hooks/), [distroless images/platforms](https://github.com/GoogleContainerTools/distroless), [official PostgreSQL image storage layout](https://hub.docker.com/_/postgres).
