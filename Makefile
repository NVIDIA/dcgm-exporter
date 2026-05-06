# Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

include hack/VERSION

REGISTRY             ?= nvidia
GO                   ?= go
GOBIN_DIR            := $(or $(shell $(GO) env GOBIN),$(shell $(GO) env GOPATH)/bin)
MKDIR                ?= mkdir
GOLANGCILINT_TIMEOUT ?= 10m
IMAGE_TAG            ?= ""

export PATH := $(GOBIN_DIR):$(PATH)

DCGM_VERSION   := $(NEW_DCGM_VERSION)
GOLANG_VERSION := 1.26.2
VERSION        := $(NEW_EXPORTER_VERSION)
FULL_VERSION   := $(DCGM_VERSION)-$(VERSION)
OUTPUT         := type=oci,dest=/dev/null
PLATFORMS      := linux/amd64,linux/arm64
DOCKERCMD      := docker --debug buildx build
MODULE         := github.com/NVIDIA/dcgm-exporter
CONTAINER      ?= all

.PHONY: all binary install check-format local
all: ubuntu22.04 ubi9 distroless

binary:
	cd cmd/dcgm-exporter; $(GO) build -trimpath -ldflags "-X main.BuildVersion=${DCGM_VERSION}-${VERSION}"

test-main: generate
	$(GO) test ./... -short

install: binary
	install -m 755 cmd/dcgm-exporter/dcgm-exporter /usr/bin/dcgm-exporter
	install -m 644 -D ./etc/default-counters.csv /etc/dcgm-exporter/default-counters.csv

check-format:
	test $$(gofmt -l pkg | tee /dev/stderr | wc -l) -eq 0
	test $$(gofmt -l cmd | tee /dev/stderr | wc -l) -eq 0

push:
	$(MAKE) ubuntu22.04 OUTPUT=type=registry
	$(MAKE) ubi9 OUTPUT=type=registry
	$(MAKE) distroless OUTPUT=type=registry

local:
ifeq ($(shell uname -p),aarch64)
	$(MAKE) $(CONTAINER) PLATFORMS=linux/arm64 OUTPUT=type=docker DOCKERCMD='docker build'
else
	$(MAKE) $(CONTAINER) PLATFORMS=linux/amd64 OUTPUT=type=docker DOCKERCMD='docker build'
endif

ubi%: DOCKERFILE = docker/Dockerfile
ubi%: BUILD_TARGET = runtime-ubi
ubi%: --docker-build-%
	@
ubi9: BASE_IMAGE = nvcr.io/nvidia/cuda:13.2.1-base-ubi9
ubi9: IMAGE_TAG = ubi9

ubuntu%: DOCKERFILE = docker/Dockerfile
ubuntu%: BUILD_TARGET = runtime-ubuntu
ubuntu%: --docker-build-%
	@
ubuntu22.04: BASE_IMAGE = nvcr.io/nvidia/cuda:13.2.1-base-ubuntu22.04
ubuntu22.04: IMAGE_TAG = ubuntu22.04

distroless: DOCKERFILE = docker/Dockerfile
distroless: BUILD_TARGET = runtime-distroless
distroless: IMAGE_TAG = distroless
distroless: --docker-build-distroless

--docker-build-%:
	@echo "Building for $@ with target $(BUILD_TARGET)"
	mkdir -p .go/compiler .go/pkg/mod
	docker buildx inspect
	DOCKER_BUILDKIT=1 \
	$(DOCKERCMD) --pull \
		--output $(OUTPUT) \
		--progress=plain \
		--no-cache \
		--platform $(PLATFORMS) \
		$(if $(BUILD_TARGET),--target $(BUILD_TARGET)) \
		--build-arg BASEIMAGE="$(BASE_IMAGE)" \
		--build-arg "GOLANG_VERSION=$(GOLANG_VERSION)" \
		--build-arg "DCGM_VERSION=$(DCGM_VERSION)" \
		--build-arg "VERSION=$(VERSION)" \
		$(if $(GOPROXY),--build-arg "GOPROXY=$(GOPROXY)") \
		$(if $(GONOSUMDB),--build-arg "GONOSUMDB=$(GONOSUMDB)") \
		$(if $(GOSUMDB),--build-arg "GOSUMDB=$(GOSUMDB)") \
		--tag $(REGISTRY)/dcgm-exporter:$(FULL_VERSION)$(if $(IMAGE_TAG),-$(IMAGE_TAG)) \
		--file $(DOCKERFILE) .

.PHONY: packages package-arm64 package-amd64
packages: package-amd64 package-arm64

package-arm64:
	$(MAKE) package-build PLATFORMS=linux/arm64

package-amd64:
	$(MAKE) package-build PLATFORMS=linux/amd64

ifeq ($(GOPROXY_ENABLED),true)
package-build: BUILD_TYPE = distroless
package-build: IMAGE_TAG = distroless
DIST_PREFIX = stig-
else
package-build: BUILD_TYPE = ubuntu22.04
package-build: IMAGE_TAG = ubuntu22.04
DIST_PREFIX =
endif

package-build:
	ARCH=`echo $(PLATFORMS) | cut -d'/' -f2`; \
	if [ "$$ARCH" = "amd64" ]; then \
		ARCH="x86-64"; \
	fi; \
	if [ "$$ARCH" = "arm64" ]; then \
		ARCH="sbsa"; \
	fi; \
	export DIST_NAME="dcgm_exporter-$(DIST_PREFIX)linux-$$ARCH-$(VERSION)"; \
	export COMPONENT_NAME="dcgm_exporter"; \
	$(MAKE) $(BUILD_TYPE) OUTPUT=type=docker PLATFORMS=$(PLATFORMS) && \
	$(MKDIR) -p /tmp/$$DIST_NAME/$$COMPONENT_NAME && \
	$(MKDIR) -p /tmp/$$DIST_NAME/$$COMPONENT_NAME/usr/bin && \
	$(MKDIR) -p /tmp/$$DIST_NAME/$$COMPONENT_NAME/etc/dcgm-exporter && \
	I=`docker create $(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-$(IMAGE_TAG)` && \
	docker cp $$I:/usr/bin/dcgm-exporter /tmp/$$DIST_NAME/$$COMPONENT_NAME/usr/bin/ && \
	docker cp $$I:/etc/dcgm-exporter /tmp/$$DIST_NAME/$$COMPONENT_NAME/etc/ && \
	cp ./LICENSE /tmp/$$DIST_NAME/$$COMPONENT_NAME && \
	mkdir -p /tmp/$$DIST_NAME/$$COMPONENT_NAME/lib/systemd/system/ && \
	cp ./packaging/config-files/systemd/nvidia-dcgm-exporter.service \
		/tmp/$$DIST_NAME/$$COMPONENT_NAME/lib/systemd/system/nvidia-dcgm-exporter.service && \
	docker rm -f $$I && \
	$(MKDIR) -p $(CURDIR)/dist && \
	cd "/tmp/$$DIST_NAME" && tar -czf $(CURDIR)/dist/$$DIST_NAME.tar.gz `ls -A` && \
	rm -rf "/tmp/$$DIST_NAME";

.PHONY: integration
test-integration: generate
	go test -race -count=1 -timeout 5m -v $(TEST_ARGS) ./tests/integration/

.PHONY: test-coverage
test-coverage:
	@echo "Preparing coverage data directories..."
	@rm -rf .coverdata
	@mkdir -p .coverdata/unit .coverdata/integration .coverdata/merged
	@echo "Running unit tests..."
	gotestsum --format testname -- \
		$$($(GO) list ./... | grep -v "/tests/e2e/") \
		-count=1 -timeout 5m \
		-cover -covermode=count \
		--short \
		-args -test.gocoverdir=$(CURDIR)/.coverdata/unit
	@echo "Running integration tests..."
	gotestsum --format testname -- \
		./internal/pkg/integration_test/... \
		-count=1 -timeout 5m \
		-cover -covermode=count \
		-coverpkg=./internal/pkg/... \
		--short \
		-args -test.gocoverdir=$(CURDIR)/.coverdata/integration
	@echo "Merging coverage data..."
	$(GO) tool covdata merge \
		-i=$(CURDIR)/.coverdata/unit,$(CURDIR)/.coverdata/integration \
		-o=$(CURDIR)/.coverdata/merged
	@echo "Coverage summary (pre-filter):"
	$(GO) tool covdata percent -i=$(CURDIR)/.coverdata/merged
	$(GO) tool covdata textfmt \
		-i=$(CURDIR)/.coverdata/merged \
		-o=combined_coverage.out.tmp
	grep -v "mock_" combined_coverage.out.tmp > tests.cov
	rm -rf combined_coverage.out.tmp .coverdata
	$(GO) tool cover -func=tests.cov

# Unit tests only with coverage (for CI without GPU/DCGM)
# Skips integration tests that require DCGM library
# Skips nvmlprovider tests that require NVML library (GPU)
# Emits a single coverage profile directly (no merge step)
# Generates test_results.json for SonarQube integration
.PHONY: unit-test-coverage
unit-test-coverage:
	@echo "Running unit tests only (skipping integration tests and nvmlprovider)..."
	gotestsum --format testname --jsonfile test_results.json -- \
		$$(go list ./... | grep -v -E "(tests/e2e|integration_test|nvmlprovider)") \
		-count=1 -timeout 5m \
		-covermode=count \
		-coverprofile=tests.cov \
		--short
	@echo "Filtering out mock files from coverage..."
	@if [ -f tests.cov ]; then \
		grep -v "mock_" tests.cov > tests.cov.tmp && mv tests.cov.tmp tests.cov || true; \
	fi
	@echo "Unit test coverage completed"
	go tool cover -func=tests.cov

.PHONY: lint
lint:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 GOGC=50 \
		golangci-lint run ./... --timeout $(GOLANGCILINT_TIMEOUT) --new-from-rev=HEAD~1 --concurrency=2

.PHONY: hadolint lint-dockerfiles
hadolint lint-dockerfiles: ## Lint Dockerfiles with hadolint
	@echo "Linting Dockerfiles with hadolint..."
	@if command -v hadolint > /dev/null 2>&1; then \
		hadolint docker/Dockerfile.ubuntu docker/Dockerfile.ubi docker/Dockerfile.distroless; \
	elif docker inspect hadolint/hadolint > /dev/null 2>&1; then \
		docker run --rm -i -v "$(CURDIR)/.hadolint.yaml:/.config/hadolint.yaml" \
			hadolint/hadolint < docker/Dockerfile.ubuntu && \
		docker run --rm -i -v "$(CURDIR)/.hadolint.yaml:/.config/hadolint.yaml" \
			hadolint/hadolint < docker/Dockerfile.ubi && \
		docker run --rm -i -v "$(CURDIR)/.hadolint.yaml:/.config/hadolint.yaml" \
			hadolint/hadolint < docker/Dockerfile.distroless; \
	else \
		echo "Error: hadolint not found. Install it or run: docker pull hadolint/hadolint"; \
		exit 1; \
	fi
	@echo "✓ All Dockerfiles passed hadolint checks"

.PHONY: validate-modules
validate-modules:
	@echo "- Verifying that the dependencies have expected content..."
	go mod verify
	@echo "- Checking for any unused/missing packages in go.mod..."
	go mod tidy
	@git diff --exit-code -- go.sum go.mod

.PHONY: validate
validate: validate-modules hadolint check-fmt ## Run all validation checks
	@echo "✓ All validation checks passed"

.PHONY: tools
tools: ## Install required tools and utilities
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(GOBIN_DIR) v2.11.4
	$(GO) install golang.org/x/tools/cmd/goimports@v0.44.0
	$(GO) install mvdan.cc/gofumpt@v0.9.2
	$(GO) install gotest.tools/gotestsum@v1.13.0

fmt:
	find . -path './.go' -prune -o -name '*.go' -print | xargs gofumpt -l -w

goimports:
	go list -f {{.Dir}} $(MODULE)/... \
		| xargs goimports -local $(MODULE) -w

check-fmt:
	@echo "Checking code formatting.  Any listed files don't match goimports:"
	! (find . -path './.go' -prune -o -path './internal/mocks' -prune -o -path './third_party' -prune -o -path './examples' -prune -o -iname "*.go" -print \
		| xargs goimports -l -local $(MODULE) | grep .)

.PHONY: e2e-test
e2e-test:
	cd tests/e2e && make e2e-test

# Command for in-place substitution
SED_CMD := sed -i$(shell uname -s | grep -q Darwin && echo " ''")

# Create list of paths to YAML, README.md, and Makefile files
FILE_PATHS := $(shell find . -type f \( -name "*.yaml" -o -name "README.md" -o -name "Makefile" \))

# Replace the old version with the new version in specified files
update-version:
	@echo "Updating DCGM version in files from $(OLD_DCGM_VERSION) to $(NEW_DCGM_VERSION)..."
	@$(foreach file,$(FILE_PATHS),$(SED_CMD) "s/$(OLD_DCGM_VERSION)/$(NEW_DCGM_VERSION)/g" $(file);)
	@echo "Updating DCGM Exporter version in files from $(OLD_EXPORTER_VERSION) to $(NEW_EXPORTER_VERSION)..."
	@$(foreach file,$(FILE_PATHS),$(SED_CMD) "s/$(OLD_EXPORTER_VERSION)/$(NEW_EXPORTER_VERSION)/g" $(file);)

# Update DCGM and DCGM Exporter versions
update-versions: update-version

.PHONY: generate
# Generate code (Mocks)
generate:
	go generate ./...

.PHONY: test-images
test-images: ## Run Docker image validation tests (requires local images built with 'make local')
	@echo "Running Docker image tests..."
	cd tests/docker && $(MAKE) docker-test
