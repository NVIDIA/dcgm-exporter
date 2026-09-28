# Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

# Keep `make` equivalent to `make container-test` even though this file exposes help.
.DEFAULT_GOAL := container-test

# ------------------------------------------------------------------------------
# Configuration
# ------------------------------------------------------------------------------

GO_CMD ?= go

# Default image configuration.
REGISTRY ?= nvidia
FULL_VERSION ?= $(DCGM_VERSION)-$(EXPORTER_VERSION)

# Override specific images when testing locally built tags.
# If not set, defaults to: $(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-<variant>
DCGM_IMAGE ?=
EXPORTER_UBUNTU_IMAGE ?=
EXPORTER_DISTROLESS_IMAGE ?=

# ------------------------------------------------------------------------------
# Help
# ------------------------------------------------------------------------------

.PHONY: help
help: ## Show available make targets
	@grep -hE '^[a-zA-Z0-9_.%/-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "%-32s %s\n", $$1, $$2}'

# ------------------------------------------------------------------------------
# Container Runtime Tests
# ------------------------------------------------------------------------------

.PHONY: container-test container-test-ubuntu container-test-distroless
.PHONY: container-test-verbose container-test-focus
container-test: ## Run tests for all configured container images
	@echo "Running container runtime tests..."
	@REGISTRY=$(REGISTRY) \
		VERSION=$(FULL_VERSION) \
		DCGM_IMAGE=$(DCGM_IMAGE) \
		EXPORTER_UBUNTU_IMAGE=$(EXPORTER_UBUNTU_IMAGE) \
		EXPORTER_DISTROLESS_IMAGE=$(EXPORTER_DISTROLESS_IMAGE) \
		$(GO_CMD) test --tags=container -v . \
		-args \
		--ginkgo.v \
		--ginkgo.no-color

container-test-ubuntu: ## Test only Ubuntu image
	@echo "Testing Ubuntu image only..."
	@REGISTRY=$(REGISTRY) \
		VERSION=$(FULL_VERSION) \
		DCGM_IMAGE=$(DCGM_IMAGE) \
		EXPORTER_UBUNTU_IMAGE=$(EXPORTER_UBUNTU_IMAGE) \
		EXPORTER_DISTROLESS_IMAGE="" \
		$(GO_CMD) test --tags=container -v . \
		-args \
		--ginkgo.v \
		--ginkgo.no-color

container-test-distroless: ## Test only distroless image
	@echo "Testing distroless image only..."
	@REGISTRY=$(REGISTRY) \
		VERSION=$(FULL_VERSION) \
		DCGM_IMAGE=$(DCGM_IMAGE) \
		EXPORTER_UBUNTU_IMAGE="" \
		EXPORTER_DISTROLESS_IMAGE=$(EXPORTER_DISTROLESS_IMAGE) \
		$(GO_CMD) test --tags=container -v . \
		-args \
		--ginkgo.v \
		--ginkgo.no-color

container-test-verbose: ## Run container runtime tests with verbose output
	@DCGM_IMAGE=$(DCGM_IMAGE) \
		EXPORTER_UBUNTU_IMAGE=$(EXPORTER_UBUNTU_IMAGE) \
		EXPORTER_DISTROLESS_IMAGE=$(EXPORTER_DISTROLESS_IMAGE) \
		$(GO_CMD) test --tags=container -v . \
		-args \
		--ginkgo.v \
		--ginkgo.vv \
		--ginkgo.trace

container-test-focus: ## Run specific test cases (use FOCUS="pattern")
	@if [ -z "$(FOCUS)" ]; then \
		echo "Error: FOCUS is required. Example: make container-test-focus FOCUS=smoke"; \
		exit 1; \
	fi
	@DCGM_IMAGE=$(DCGM_IMAGE) \
		EXPORTER_UBUNTU_IMAGE=$(EXPORTER_UBUNTU_IMAGE) \
		EXPORTER_DISTROLESS_IMAGE=$(EXPORTER_DISTROLESS_IMAGE) \
		$(GO_CMD) test --tags=container -v . \
		-args \
		--ginkgo.v \
		--ginkgo.focus="$(FOCUS)"
