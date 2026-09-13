# syntax=docker/dockerfile:1
ARG GO_IMAGE=golang:1.27.1-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
ARG TARGETOS=linux
ARG TARGETARCH=arm64
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    for app in queue streamer collector api migrate; do \
      CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w' -o /out/$app ./cmd/$app || exit 1; \
    done && mkdir -p /out/queue-data && chmod 0770 /out/queue-data

FROM ${RUNTIME_IMAGE} AS runtime
USER 65532:65532
WORKDIR /

FROM runtime AS queue
COPY --from=build --chown=65532:65532 /out/queue /app/queue
COPY --from=build --chown=65532:65532 /out/queue-data /var/lib/gpu-telemetry
EXPOSE 8081
ENTRYPOINT ["/app/queue"]
CMD ["--listen=:8081", "--db=/var/lib/gpu-telemetry/queue.db"]

FROM runtime AS streamer
COPY --from=build --chown=65532:65532 /out/streamer /app/streamer
COPY --chown=65532:65532 Problemstatement/dcgm_metrics_20250718_134233.csv /app/data/metrics.csv
ENTRYPOINT ["/app/streamer"]
CMD ["--csv=/app/data/metrics.csv", "--count=0"]

FROM runtime AS collector
COPY --from=build --chown=65532:65532 /out/collector /app/collector
EXPOSE 8082
ENTRYPOINT ["/app/collector"]
CMD ["--listen=:8082"]

FROM runtime AS api
COPY --from=build --chown=65532:65532 /out/api /app/api
EXPOSE 8080
ENTRYPOINT ["/app/api"]
CMD ["--listen=:8080"]

FROM runtime AS migrate
COPY --from=build --chown=65532:65532 /out/migrate /app/migrate
ENTRYPOINT ["/app/migrate"]
