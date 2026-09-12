# Elastic GPU Telemetry Pipeline

Status: environment bootstrap and design scaffold. Application binaries, tests, Dockerfiles, and Helm resources have not yet been implemented.

Requirements and sample input remain in `Problemstatement/`. See `docs/EXECUTION_PLAN.md` for the requirement analysis, architecture, and delivery plan.

## Local environment

Apple Silicon macOS: Apple Command Line Tools, Homebrew, Go, Colima, Docker CLI and Buildx, Minikube, kubectl, Helm. The included Brewfile lists tools.

1. Run `xcode-select --install` and complete the macOS installer.
2. Install Homebrew using the official instructions at https://brew.sh. Its initial installation may require administrator authentication in your own terminal.
3. Run `bash scripts/install-tools.sh`.
4. Run `bash scripts/cluster-up.sh`.

Use `export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"` in a terminal where Homebrew is not yet on PATH. Follow Homebrew's printed shell setup instructions to make this persistent.

The named Colima profile is `gpu-telemetry`; its Docker context is `colima-gpu-telemetry`. Use `DOCKER_CONTEXT=colima-gpu-telemetry docker ...` when building images. The Kubernetes context and Minikube profile are both `gpu-telemetry`.

`make cluster-down` stops the project cluster and VM without deleting them. Do not delete the cluster if you need its persistent data.

## Planned commands

As implementation lands, add `make build`, `make test`, `make race`, `make coverage`, `make openapi`, `make images`, `make image-load`, `make helm-lint`, `make deploy`, `make smoke`, and `make benchmark`. These targets are not implemented in this bootstrap.

All four custom applications will be Go programs. PostgreSQL is a proposed infrastructure dependency, using an upstream container image.
