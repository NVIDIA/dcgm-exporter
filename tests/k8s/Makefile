# Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

include ../../hack/versions.env

# Keep `make` equivalent to `make test-integration-k8s` even though this file exposes help.
.DEFAULT_GOAL := test-integration-k8s

# ------------------------------------------------------------------------------
# Configuration
# ------------------------------------------------------------------------------

GO_CMD              ?= go
E2E_HELM_NAMESPACE           ?= dcgm-exporter
CHART               ?= ./../../deployment/
IMAGE               ?= nvcr.io/nvidia/k8s/dcgm-exporter:$(DCGM_VERSION)-$(EXPORTER_VERSION)-distroless
IMAGE_PULL_SECRET   ?=
KUBECONFIG          ?= $(HOME)/.kube/config
RUNTIME_CLASS       ?=
NODE_SELECTOR_KEY   ?=
NODE_SELECTOR_VALUE ?=
REMOTE_DCGM         ?=
DCGM_IMAGE          ?=
BUSYBOX_IMAGE       ?= busybox:$(BUSYBOX_IMAGE_TAG)
CUDA_WORKLOAD_IMAGE  ?= nvcr.io/nvidia/cuda:$(CUDA_BASE_TAG)-ubuntu$(CUDA_UBUNTU_TAG)
E2E_DCGM_NAMESPACE  ?=
DCGM_NAME           ?=
DCGM_PORT           ?= 5555
MIG_INSTANCE_ENTITY_ID ?=
MIG_INSTANCE_NVML_ID ?=
UNSUPPORTED_FIELD_CANDIDATE ?=
NO_CLEANUP          ?= false

# ------------------------------------------------------------------------------
# Shared Test Command
# ------------------------------------------------------------------------------

E2E_GPU_COUNTER_ARGS := -arguments="{-f=/etc/dcgm-exporter/default-counters.csv}"
E2E_K8S_GPU_LABELS ?= default || tls || basicAuth || configMatrix
E2E_K8S_VALIDATION_LABELS ?= default || configMatrix || configMapData || yamlConfig || oldNamespace || clockEventsCounters || tls || basicAuth || tlsBasicAuth || serviceAccess || podLabels || podUID || podLabelAllowlist || kubernetesGpuId || noHostname || modelName || pprof || debugDump || hpcJobMapping || customResourceNames

define TEST_CMD
	@if [ -z "$(KUBECONFIG)" ]; then \
		echo "[ERR] KUBECONFIG is missing, must be set"; \
		exit 1; \
	fi
	$(GO_CMD) test --tags=e2e -v . \
		-args \
		--ginkgo.v \
		--ginkgo.no-color \
		-kubeconfig="$(KUBECONFIG)" \
		-chart="$(CHART)" \
		-namespace="$(E2E_HELM_NAMESPACE)" \
		-exporter-image="$(IMAGE)" \
		-k8s-image-pull-secret="$(IMAGE_PULL_SECRET)" \
		-runtime-class="$(RUNTIME_CLASS)" \
		-node-selector-key="$(NODE_SELECTOR_KEY)" \
		-node-selector-value="$(NODE_SELECTOR_VALUE)" \
		-remote-dcgm="$(REMOTE_DCGM)" \
		-dcgm-image="$(DCGM_IMAGE)" \
		-busybox-image="$(BUSYBOX_IMAGE)" \
		-cuda-workload-image="$(CUDA_WORKLOAD_IMAGE)" \
		-dcgm-namespace="$(E2E_DCGM_NAMESPACE)" \
		-dcgm-name="$(DCGM_NAME)" \
		-dcgm-port="$(DCGM_PORT)" \
		-mig-instance-entity-id="$(MIG_INSTANCE_ENTITY_ID)" \
		-mig-instance-nvml-id="$(MIG_INSTANCE_NVML_ID)" \
		-unsupported-field-candidate="$(UNSUPPORTED_FIELD_CANDIDATE)" \
		-no-cleanup="$(NO_CLEANUP)"
endef

# ------------------------------------------------------------------------------
# Help
# ------------------------------------------------------------------------------

.PHONY: help
help: ## Show available make targets
	@grep -hE '^[a-zA-Z0-9_.%/-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "%-32s %s\n", $$1, $$2}'

# ------------------------------------------------------------------------------
# Kubernetes Integration Tests
# ------------------------------------------------------------------------------

.PHONY: test-integration-k8s test-k8s-gpu test-k8s-no-profiling
.PHONY: test-k8s-basic-auth test-k8s-tls test-k8s-default test-k8s-labels
test-integration-k8s: ## Run the default Kubernetes integration command
	@$(TEST_CMD)

test-k8s-gpu: ## Run GPU Kubernetes integration tests
	@$(TEST_CMD) \
		$(E2E_GPU_COUNTER_ARGS) \
		--ginkgo.label-filter='$(E2E_K8S_GPU_LABELS)'

.PHONY: test-e2e
test-e2e:
	@$(TEST_CMD) \
		$(E2E_GPU_COUNTER_ARGS) \
		--ginkgo.label-filter='$(E2E_K8S_VALIDATION_LABELS)'

test-k8s-no-profiling: ## Run Kubernetes tests with default counters and no profiling
	@$(TEST_CMD) \
		$(E2E_GPU_COUNTER_ARGS)

test-k8s-basic-auth: ## Run basic-auth Kubernetes tests
	@$(TEST_CMD) \
		$(E2E_GPU_COUNTER_ARGS) \
		--ginkgo.label-filter=basicAuth

test-k8s-tls: ## Run TLS Kubernetes tests
	@$(TEST_CMD) \
		$(E2E_GPU_COUNTER_ARGS) \
		--ginkgo.label-filter=tls

test-k8s-default: ## Run default-configuration Kubernetes tests
	@$(TEST_CMD) \
		$(E2E_GPU_COUNTER_ARGS) \
		--ginkgo.label-filter=default

test-k8s-labels: ## Run label Kubernetes tests
	@$(TEST_CMD) \
		$(E2E_GPU_COUNTER_ARGS) \
		--ginkgo.label-filter=podLabels

# ------------------------------------------------------------------------------
# Build
# ------------------------------------------------------------------------------

.PHONY: binary
binary: ## Compile the Kubernetes test binary
	go test -c --tags="e2e" .
