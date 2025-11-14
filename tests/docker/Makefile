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

GO_CMD ?= go

# Default image configuration
# Note: FULL_VERSION should match root Makefile's DCGM_VERSION-VERSION format
# This gets updated automatically by 'make update-version' from root
REGISTRY ?= nvidia
FULL_VERSION ?= 4.4.2-4.7.0

# Override specific images (optional)
# If not set, defaults to: $(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-<variant>
IMAGE_UBUNTU ?=
IMAGE_UBI ?=
IMAGE_DISTROLESS ?=

.PHONY: docker-test
docker-test: ## Run tests for all configured images
	@echo "Running Docker image tests..."
	@REGISTRY=$(REGISTRY) VERSION=$(FULL_VERSION) \
		IMAGE_UBUNTU=$(IMAGE_UBUNTU) IMAGE_UBI=$(IMAGE_UBI) IMAGE_DISTROLESS=$(IMAGE_DISTROLESS) \
		$(GO_CMD) test --tags=docker -v . \
		-args \
		--ginkgo.v \
		--ginkgo.no-color

.PHONY: docker-test-ubuntu
docker-test-ubuntu: ## Test only Ubuntu image
	@echo "Testing Ubuntu image only..."
	@REGISTRY=$(REGISTRY) VERSION=$(FULL_VERSION) \
		IMAGE_UBUNTU=$(IMAGE_UBUNTU) IMAGE_UBI="" IMAGE_DISTROLESS="" \
		$(GO_CMD) test --tags=docker -v . \
		-args \
		--ginkgo.v \
		--ginkgo.no-color

.PHONY: docker-test-ubi
docker-test-ubi: ## Test only UBI image
	@echo "Testing UBI image only..."
	@REGISTRY=$(REGISTRY) VERSION=$(FULL_VERSION) \
		IMAGE_UBUNTU="" IMAGE_UBI=$(IMAGE_UBI) IMAGE_DISTROLESS="" \
		$(GO_CMD) test --tags=docker -v . \
		-args \
		--ginkgo.v \
		--ginkgo.no-color

.PHONY: docker-test-distroless
docker-test-distroless: ## Test only distroless image
	@echo "Testing distroless image only..."
	@REGISTRY=$(REGISTRY) VERSION=$(FULL_VERSION) \
		IMAGE_UBUNTU="" IMAGE_UBI="" IMAGE_DISTROLESS=$(IMAGE_DISTROLESS) \
		$(GO_CMD) test --tags=docker -v . \
		-args \
		--ginkgo.v \
		--ginkgo.no-color

.PHONY: docker-test-verbose
docker-test-verbose: ## Run Docker image tests with verbose output
	@$(GO_CMD) test --tags=docker -v . \
		-args \
		--ginkgo.v \
		--ginkgo.vv \
		--ginkgo.trace

.PHONY: docker-test-focus
docker-test-focus: ## Run specific test cases (use FOCUS="pattern")
	@$(GO_CMD) test --tags=docker -v . \
		-args \
		--ginkgo.v \
		--ginkgo.focus="$(FOCUS)"

.PHONY: help
help: ## Display this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

