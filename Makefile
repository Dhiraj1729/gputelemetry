.PHONY: help doctor tools cluster-up cluster-down
help:
	@echo 'Bootstrap targets: doctor, tools, cluster-up, cluster-down'
	@echo 'Application build/test/coverage/openapi/image/helm targets will be added with implementation.'
doctor:
	bash scripts/doctor.sh
tools:
	bash scripts/install-tools.sh
cluster-up:
	bash scripts/cluster-up.sh
cluster-down:
	minikube -p gpu-telemetry stop
	colima stop gpu-telemetry
