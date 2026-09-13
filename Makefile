GO ?= go
.PHONY: help doctor tools cluster-up cluster-down build test race coverage vet fmt integration demo
help:
	@echo 'Development: build test race coverage vet fmt integration demo demo-day2 demo-day3 migrate postgres-up openapi check-openapi demo-day4'
	@echo 'Packaging: docker-build minikube-load helm-check helm-template helm-install helm-verify helm-scale helm-package'
	@echo 'Environment: doctor tools cluster-up cluster-down'
doctor:
	bash scripts/doctor.sh
tools:
	bash scripts/install-tools.sh
cluster-up:
	bash scripts/cluster-up.sh
cluster-down:
	minikube -p gpu-telemetry stop
	colima stop gpu-telemetry
build:
	mkdir -p bin
	$(GO) build -trimpath -o bin/queue ./cmd/queue
	$(GO) build -trimpath -o bin/streamer ./cmd/streamer
	$(GO) build -trimpath -o bin/testconsumer ./cmd/testconsumer
	$(GO) build -trimpath -o bin/collector ./cmd/collector
	$(GO) build -trimpath -o bin/migrate ./cmd/migrate
	$(GO) build -trimpath -o bin/api ./cmd/api
test:
	$(GO) test ./...
race:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
fmt:
	gofmt -w cmd internal tests migrations
coverage:
	mkdir -p coverage
	$(GO) test -coverprofile=coverage/coverage.out ./...
	$(GO) tool cover -func=coverage/coverage.out
	$(GO) tool cover -html=coverage/coverage.out -o coverage/coverage.html
integration:
	$(GO) test -count=1 -tags=integration -v ./tests/system
demo: integration

.PHONY: demo-day2
demo-day2:
	$(GO) test -count=1 -tags=integration -run TestDay2CrashRestart -v ./tests/system

.PHONY: migrate demo-day3
migrate:
	$(GO) run ./cmd/migrate
demo-day3:
	$(GO) test -count=1 -tags=postgres_integration -v ./tests/day3

.PHONY: postgres-up
postgres-up:
	bash scripts/postgres-up.sh

.PHONY: openapi check-openapi demo-day4
openapi:
	$(GO) run ./cmd/openapi
check-openapi:
	$(GO) run ./cmd/openapi --check
demo-day4:
	$(GO) test -count=1 -tags=postgres_integration -v ./tests/day4

# Day 5 local packaging (no image pushes or implicit Git operations).
IMAGE_REPOSITORY ?= gpu-telemetry
IMAGE_TAG ?= dev
PLATFORM ?= linux/arm64
DOCKER_CONTEXT ?= colima-gpu-telemetry
MINIKUBE_PROFILE ?= gpu-telemetry
KUBE_CONTEXT ?= gpu-telemetry
RELEASE ?= gpu-telemetry
NAMESPACE ?= gpu-telemetry-day5
export IMAGE_REPOSITORY IMAGE_TAG PLATFORM DOCKER_CONTEXT MINIKUBE_PROFILE KUBE_CONTEXT RELEASE NAMESPACE
.PHONY: docker-build minikube-load helm-check helm-template helm-install helm-verify helm-scale
docker-build:
	bash scripts/day5-images.sh build
minikube-load:
	bash scripts/day5-images.sh load
helm-check:
	helm lint deploy/helm/gpu-telemetry
	$(GO) test -count=1 -tags=packaging -v ./tests/packaging
helm-template:
	mkdir -p work
	helm template $(RELEASE) deploy/helm/gpu-telemetry -n $(NAMESPACE) --set bootstrapOnly=false --set database.existingSecret=render-only-secret > work/day5-rendered.yaml
helm-install:
	bash scripts/day5-install.sh
helm-verify:
	bash scripts/day5-verify.sh
helm-scale:
	bash scripts/day5-scale.sh

.PHONY: helm-package
helm-package:
	mkdir -p work/charts
	helm package deploy/helm/gpu-telemetry --destination work/charts
