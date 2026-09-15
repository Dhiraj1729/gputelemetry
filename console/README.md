# Local deployment console

Apple Silicon macOS users can double-click `Start Console.command` in the repository root. The bundled executable opens the browser UI and detects that repository. No Go or Node installation is required to launch it. Apple may request approval for downloaded, unsigned software; this development binary is not Developer ID signed or notarized.

Run Check prerequisites, Install tools, Start cluster, Build and load, Deploy, Verify, then Connect API. Homebrew and Apple Command Line Tools must be installed separately when absent. Choose GPU Explorer to load GPU UUIDs and query telemetry. Commands and output appear in the deployment log. A failed operation can be retried; cancellation terminates its process group but does not roll back changes already made.

The server binds only to loopback, chooses an available port, and requires a session token plus same-origin requests for control operations. It executes only named operations using argument arrays. Selecting a repository authorizes running that repository’s existing scripts. The repository itself must be trusted.

The current console supports the fixed `gpu-telemetry` cluster, release, and namespace used by the repository defaults. Deployment settings expose replica overrides only. Blank settings use defaults for a fresh release and retain prior settings on upgrades. The temporary override does not change the chart. The GPU explorer displays tables and raw responses with optional polling. Pipeline badges record this console session’s actions; use Refresh cluster status to inspect live workloads.

Close the browser without stopping workloads. Press Ctrl-C in the launcher’s Terminal window to exit the console and stop its owned port-forward.

## Cleanup options

Minikube host-path volumes can retain an old provisioner identity after a restart. Cleanup recovers released queue/PostgreSQL volumes only in the dedicated single-node cluster, after the project namespace is gone. It validates the exact claim names and paths, reads the current identity from a temporary empty PVC, and uses UID/resource-version guarded patches to let the current provisioner reclaim the data normally. It also handles multiple verified project PV records for the same deterministic project path after a failed reclamation and redeployment. It removes the temporary probe namespace and checks volume deletion before shutdown. It never strips finalizers or manually removes volume directories. Unexpected storage providers, paths, unrelated shared volumes or changed resource versions stop recovery.

- **Stop — keep data:** stops Minikube and Colima; preserves workloads, images, and telemetry.
- **Clean deployment and shut down:** requires typing `DELETE gpu-telemetry`. Starts the cluster if needed, checks ownership of every listable namespaced resource, uninstalls Helm, deletes the dedicated namespace (including retained PVCs, Secrets and migration hooks), waits for namespace and PV deletion, and stops the cluster. Images and VM disks remain. Non-Delete PV reclaim policies cause a refusal before deletion. Kubernetes reclamation is not secure disk erasure.
- **Remove project environment:** requires typing `REMOVE gpu-telemetry`. Performs deployment cleanup, then deletes only the named Minikube profile and Colima profile, including Colima runtime data. Refuses if unrelated namespaces, default-namespace workloads, persistent volumes, Docker containers or Docker volumes are found. Installed tools, source files and host caches remain. The dedicated environment must not be shared with other applications, including custom additions in system namespaces.

Errors and cancellation stop later steps; already completed deletions cannot be rolled back. Cleanup does not strip finalizers or force-delete stuck resources. If cleanup fails, inspect the execution log and retry after resolving the cause. Missing releases and namespaces are tolerated. If profiles were already removed, cleanup may recreate the dedicated environment for verification before removing it again. Successful cleanup resets deployment badges; full environment removal also clears the saved image tag, requiring an image rebuild.

To rebuild after changing console code or UI:

```sh
cd console
go test ./...
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o telemetry-console .
```

The executable embeds the browser assets. Distribute it together with the launcher and source repository. Changes in the console do not modify the telemetry services or their deployment scripts.
