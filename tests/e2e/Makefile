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

GO_CMD ?= go
NAMESPACE ?= "dcgm-exporter"
CHART ?= "./../../deployment/"
IMAGE_REPOSITORY ?= "nvcr.io/nvidia/k8s/dcgm-exporter"
IMAGE_TAG ?= "4.2.3-4.1.3-ubuntu22.04"
KUBECONFIG ?= "~/.kube/config"
RUNTIME_CLASS ?= ""
NO_CLEANUP ?= "false"

define TEST_CMD
	@if [ -z ${KUBECONFIG} ]; then \
		echo "[ERR] KUBECONFIG is missing, must be set"; \
		exit 1; \
	fi
	$(GO_CMD) test --tags=e2e -v . \
		-args \
		--ginkgo.v \
		--ginkgo.no-color \
		-kubeconfig=$(KUBECONFIG) \
		-chart="$(CHART)" \
		-namespace=$(NAMESPACE) \
		-image-repository=$(IMAGE_REPOSITORY) \
		-image-tag=$(IMAGE_TAG) \
		-runtime-class=$(RUNTIME_CLASS) \
		-no-cleanup=$(NO_CLEANUP)
endef

.PHONY: e2e-test
e2e-test:
	@$(TEST_CMD)


.PHONY: e2e-test-no-profiling
e2e-test-no-profiling:
	@$(TEST_CMD) \
		-arguments="{-f=/etc/dcgm-exporter/default-counters.csv}"

.PHONY: e2e-basic-auth
e2e-basic-auth:
	@$(TEST_CMD) \
		-arguments="{-f=/etc/dcgm-exporter/default-counters.csv}" \
		--ginkgo.label-filter=basicAuth

.PHONY: e2e-tls
e2e-tls:
	@$(TEST_CMD) \
		-arguments="{-f=/etc/dcgm-exporter/default-counters.csv}" \
		--ginkgo.label-filter=tls

.PHONY: e2e-default
e2e-default:
	@$(TEST_CMD) \
		-arguments="{-f=/etc/dcgm-exporter/default-counters.csv}" \
		--ginkgo.label-filter=default

binary:
	go test -c --tags="e2e" .
