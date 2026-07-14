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

include hack/versions.env

# Keep `make` equivalent to `make all` even though this file also exposes help.
.DEFAULT_GOAL := all

# ------------------------------------------------------------------------------
# Configuration
# ------------------------------------------------------------------------------

# User-overridable tools.
REGISTRY             ?= nvidia
GO                   ?= go
GO_ENV_GOBIN         := $(shell command -v $(GO) >/dev/null 2>&1 && $(GO) env GOBIN 2>/dev/null)
GO_ENV_GOPATH        := $(shell command -v $(GO) >/dev/null 2>&1 && $(GO) env GOPATH 2>/dev/null)
GOBIN_DIR            := $(or $(GO_ENV_GOBIN),$(if $(GO_ENV_GOPATH),$(GO_ENV_GOPATH)/bin),$(HOME)/go/bin)
MKDIR                ?= mkdir
GOLANGCILINT_TIMEOUT ?= 10m
GOLANGCILINT_TMPDIR  ?= $(CURDIR)/.cache/golangci-lint-tmp
GOLANGCILINT_CACHE   ?= $(CURDIR)/.cache/golangci-lint-cache
LINT_BASE_REV        ?= HEAD~1
FUZZ_TIME             ?= 10s
FUZZ_PARALLEL         ?= 2
FUZZ_MINIMIZE_TIME    ?= 5s
FUZZ_TIMEOUT          ?= 2m
COMMA                := ,

export PATH := $(GOBIN_DIR):$(PATH)

# Build metadata.
GOLANG_VERSION := $(GO_VERSION)
VERSION        := $(EXPORTER_VERSION)
FULL_VERSION   := $(DCGM_VERSION)-$(VERSION)
PACKAGE_VERSION := $(EXPORTER_VERSION).$(PACKAGE_REVISION)
UBUNTU_IMAGE   ?= ubuntu:26.04
PACKAGE_BUILDER_IMAGE ?= registry.access.redhat.com/ubi8/ubi:latest
PACKAGE_COMPONENT_DIR ?= dcgm_exporter
MODULE         := github.com/NVIDIA/dcgm-exporter

# Docker build defaults.
OUTPUT         := type=oci,dest=/dev/null
PLATFORMS      := linux/amd64,linux/arm64
DOCKERCMD      := docker --debug buildx build
# Keep release and CI image builds clean by default; use DOCKER_NO_CACHE= for faster local rebuilds.
DOCKER_NO_CACHE ?= --no-cache
IMAGE_TAG      ?= ""
IMAGE_REF      ?= $(REGISTRY)/dcgm-exporter:$(FULL_VERSION)$(if $(IMAGE_TAG),-$(IMAGE_TAG))
CONTAINER      ?= all

# Local e2e validation defaults.
E2E_EXPORTER_UBUNTU_IMAGE ?=
E2E_DCGM_IMAGE ?= nvcr.io/nvidia/cloud-native/dcgm:$(DCGM_VERSION)-$(DCGM_IMAGE_TAG_SUFFIX)
E2E_K3S_IMAGE ?= rancher/k3s:$(K3S_VERSION)
E2E_K3D_NODE_BASE_IMAGE ?= nvcr.io/nvidia/cuda:$(CUDA_BASE_TAG)-ubuntu$(K3D_NODE_BASE_UBUNTU_TAG)
E2E_K3D_NODE_OUTPUT_IMAGE ?= dcgm-exporter/k3s-nvidia:$(K3S_VERSION)-cuda$(CUDA_BASE_TAG)-ubuntu$(K3D_NODE_BASE_UBUNTU_TAG)
E2E_BUSYBOX_IMAGE ?= busybox:$(BUSYBOX_IMAGE_TAG)
E2E_CONTAINER_TOOLKIT_TEST_IMAGE ?= nvcr.io/nvidia/cuda:$(CUDA_BASE_TAG)-ubuntu$(CUDA_UBUNTU_TAG)
E2E_CUDA_WORKLOAD_IMAGE ?= $(E2E_CONTAINER_TOOLKIT_TEST_IMAGE)
E2E_LOCAL_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
E2E_LOCAL_DIRTY ?= $(shell test -z "$$(git status --porcelain 2>/dev/null)" || echo -dirty)
ifeq ($(origin E2E_LOCAL_BUILD_ID), undefined)
E2E_LOCAL_BUILD_ID := $(shell date -u +%Y%m%dT%H%M%SZ)
endif
E2E_LOCAL_EXPORTER_BASE_IMAGE ?= $(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-local-$(E2E_LOCAL_COMMIT)$(E2E_LOCAL_DIRTY)-$(E2E_LOCAL_BUILD_ID)-distroless

export DCGM_VERSION K3D_VERSION K3S_VERSION HELM_VERSION
export NVIDIA_DEVICE_PLUGIN_VERSION GPU_OPERATOR_VERSION NVIDIA_DRA_DRIVER_VERSION
export K3D_LINUX_AMD64_SHA256 K3D_LINUX_ARM64_SHA256
export KUBECTL_LINUX_AMD64_SHA256 KUBECTL_LINUX_ARM64_SHA256
export HELM_LINUX_AMD64_SHA256 HELM_LINUX_ARM64_SHA256
export E2E_DCGM_IMAGE E2E_K3S_IMAGE E2E_K3D_NODE_BASE_IMAGE E2E_K3D_NODE_OUTPUT_IMAGE
export E2E_BUSYBOX_IMAGE E2E_CONTAINER_TOOLKIT_TEST_IMAGE E2E_CUDA_WORKLOAD_IMAGE

# Release helper defaults.
GO_DCGM_OUTPUT               ?= text
GO_DCGM_DRY_RUN              ?= false
GO_DCGM_ALLOW_FIELD_REMOVALS ?= false

# Coverage filters for generated, wrapper, and mock-heavy paths.
DCGMPROVIDER_COVERAGE_EXCLUDE := internal/pkg/dcgmprovider/(dcgm|smart_init)\.go
COVERAGE_EXCLUDE_PATTERN := mock_
COVERAGE_EXCLUDE_PATTERN := $(COVERAGE_EXCLUDE_PATTERN)|cmd/dcgm-exporter/main\.go
COVERAGE_EXCLUDE_PATTERN := $(COVERAGE_EXCLUDE_PATTERN)|$(DCGMPROVIDER_COVERAGE_EXCLUDE)

# ------------------------------------------------------------------------------
# Help
# ------------------------------------------------------------------------------

.PHONY: help
help: ## Show available make targets
	@grep -hE '^[a-zA-Z0-9_.%/-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "%-32s %s\n", $$1, $$2}'

# ------------------------------------------------------------------------------
# Build
# ------------------------------------------------------------------------------

.PHONY: all binary build-e2e install push push-dockerhub local
all: ubuntu26.04 distroless ## Build the default Ubuntu and distroless images

binary: ## Build the dcgm-exporter binary
	cd cmd/dcgm-exporter; \
		$(GO) build \
			-trimpath \
			-ldflags "-X main.BuildVersion=${DCGM_VERSION}-${VERSION}"

build-e2e: ## Build the e2e validation CLI
	$(MKDIR) -p bin
	CGO_ENABLED=0 $(GO) build \
		-buildvcs=false \
		-trimpath \
		-o bin/e2e \
		./cmd/e2e

install: binary ## Install dcgm-exporter and default counters into system paths
	install -m 755 -D cmd/dcgm-exporter/dcgm-exporter $(DESTDIR)/usr/bin/dcgm-exporter
	install -m 644 -D ./etc/default-counters.csv $(DESTDIR)/etc/dcgm-exporter/default-counters.csv

push: ## Build and push Ubuntu and distroless images
	$(MAKE) ubuntu26.04 OUTPUT=type=registry
	$(MAKE) distroless OUTPUT=type=registry

push-dockerhub: ## Build and push the Docker Hub distroless image
	$(MAKE) REGISTRY=nvidia distroless OUTPUT=type=registry

local: ## Build the configured image for the local host architecture
ifeq ($(shell uname -p),aarch64)
	$(MAKE) $(CONTAINER) \
		PLATFORMS=linux/arm64 \
		OUTPUT=type=docker
else
	$(MAKE) $(CONTAINER) \
		PLATFORMS=linux/amd64 \
		OUTPUT=type=docker
endif

.PHONY: ubuntu26.04 distroless package-image
ubuntu%: DOCKERFILE = docker/Dockerfile
ubuntu%: BUILD_TARGET = runtime-ubuntu
ubuntu%: --docker-build-%
	@
ubuntu26.04: IMAGE_TAG = ubuntu26.04
ubuntu26.04: --docker-build-ubuntu26.04 ## Build the Ubuntu runtime image
	@

distroless: DOCKERFILE = docker/Dockerfile
distroless: BUILD_TARGET = runtime-distroless
distroless: IMAGE_TAG = distroless
distroless: --docker-build-distroless ## Build the distroless runtime image

package-image: DOCKERFILE = docker/package.Dockerfile
package-image: BUILD_TARGET = package-artifact
package-image: IMAGE_TAG = package
package-image: --docker-build-package ## Build the host-package payload image

--docker-build-%:
	@echo "Building for $@ with target $(BUILD_TARGET)"
	mkdir -p .go/compiler .go/pkg/mod
	if echo "$(DOCKERCMD)" | grep -q 'buildx'; then docker buildx inspect; fi
	DOCKER_BUILDKIT=1 \
	$(DOCKERCMD) --pull \
		--provenance=false \
		--output $(OUTPUT) \
		--progress=plain \
		$(if $(DOCKER_NO_CACHE),$(DOCKER_NO_CACHE) )--platform $(PLATFORMS) \
		$(if $(BUILD_TARGET),--target $(BUILD_TARGET)) \
		--build-arg "UBUNTU_IMAGE=$(UBUNTU_IMAGE)" \
		--build-arg "PACKAGE_BUILDER_IMAGE=$(PACKAGE_BUILDER_IMAGE)" \
		--build-arg "GOLANG_VERSION=$(GOLANG_VERSION)" \
		--build-arg "DCGM_VERSION=$(DCGM_VERSION)" \
		--build-arg "VERSION=$(VERSION)" \
		$(if $(GOPROXY_ENABLED),--build-arg "GOPROXY_ENABLED=$(GOPROXY_ENABLED)") \
		$(if $(GOPROXY),--secret id=goproxy$(COMMA)env=GOPROXY) \
		$(if $(GONOSUMDB),--build-arg "GONOSUMDB=$(GONOSUMDB)") \
		$(if $(GOSUMDB),--build-arg "GOSUMDB=$(GOSUMDB)") \
		--tag $(IMAGE_REF) \
		--file $(DOCKERFILE) .

# ------------------------------------------------------------------------------
# Packaging
# ------------------------------------------------------------------------------

.PHONY: packages package-arm64 package-amd64 package-build stage-package-payload test-package
packages: package-amd64 package-arm64 ## Build packages for all supported architectures

package-arm64: ## Build the arm64 package artifact
	$(MAKE) package-build PLATFORMS=linux/arm64

package-amd64: ## Build the amd64 package artifact
	$(MAKE) package-build PLATFORMS=linux/amd64

stage-package-payload: ## Stage the host-package payload under PACKAGE_PAYLOAD_ROOT
	@test -n "$(PACKAGE_PAYLOAD_ROOT)" || \
		(echo "PACKAGE_PAYLOAD_ROOT is required" >&2; exit 1)
	PACKAGE_COMPONENT_DIR="$(PACKAGE_COMPONENT_DIR)" \
		hack/package/stage-payload.sh "$(PACKAGE_PAYLOAD_ROOT)"

package-build: BUILD_TYPE = package-image
package-build: IMAGE_TAG = package

DIST_PREFIX ?=

package-build:
	ARCH=`echo $(PLATFORMS) | cut -d'/' -f2`; \
	if [ "$$ARCH" = "amd64" ]; then \
		ARCH="x86-64"; \
	fi; \
	if [ "$$ARCH" = "arm64" ]; then \
		ARCH="sbsa"; \
	fi; \
	export DIST_NAME="dcgm_exporter-$(DIST_PREFIX)linux-$$ARCH-$(PACKAGE_VERSION)"; \
	export COMPONENT_NAME="$(PACKAGE_COMPONENT_DIR)"; \
	$(MAKE) $(BUILD_TYPE) OUTPUT=type=docker PLATFORMS=$(PLATFORMS) && \
	$(MKDIR) -p /tmp/$$DIST_NAME && \
	I=`docker create --platform $(PLATFORMS) $(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-$(IMAGE_TAG)` && \
	docker cp $$I:/package-payload/$$COMPONENT_NAME /tmp/$$DIST_NAME/ && \
	docker rm -f $$I && \
	$(MKDIR) -p $(CURDIR)/dist && \
	cd "/tmp/$$DIST_NAME" && tar -czf $(CURDIR)/dist/$$DIST_NAME.tar.gz `ls -A` && \
	rm -rf "/tmp/$$DIST_NAME";

test-package: ## Validate package tarballs through RPM/DEB install smoke tests
	bash hack/package/test.sh

# ------------------------------------------------------------------------------
# Tests and Coverage
# ------------------------------------------------------------------------------

.PHONY: test-main test-fuzz test-integration-host integration-inprocess-coverage
.PHONY: test-coverage unit-test-coverage
test-main: generate ## Run the short Go test suite
	$(GO) test ./... -short

test-fuzz: ## Run bounded native Go fuzz tests
	$(GO) test ./internal/pkg/appconfig -run='^$$' -fuzz='^FuzzParseYAMLConfig$$' -fuzztime=$(FUZZ_TIME) -fuzzminimizetime=$(FUZZ_MINIMIZE_TIME) -parallel=$(FUZZ_PARALLEL) -timeout=$(FUZZ_TIMEOUT)
	$(GO) test ./internal/pkg/counters -run='^$$' -fuzz='^FuzzExtractCountersFromCSV$$' -fuzztime=$(FUZZ_TIME) -fuzzminimizetime=$(FUZZ_MINIMIZE_TIME) -parallel=$(FUZZ_PARALLEL) -timeout=$(FUZZ_TIMEOUT)
	$(GO) test ./internal/pkg/devicewatchlistmanager -run='^$$' -fuzz='^FuzzValidateWatchGroups$$' -fuzztime=$(FUZZ_TIME) -fuzzminimizetime=$(FUZZ_MINIMIZE_TIME) -parallel=$(FUZZ_PARALLEL) -timeout=$(FUZZ_TIMEOUT)
	$(GO) test ./internal/pkg/hostname -run='^$$' -fuzz='^FuzzParseRemoteHostname$$' -fuzztime=$(FUZZ_TIME) -fuzzminimizetime=$(FUZZ_MINIMIZE_TIME) -parallel=$(FUZZ_PARALLEL) -timeout=$(FUZZ_TIMEOUT)
	$(GO) test ./pkg/cmd -run='^$$' -fuzz='^FuzzParseDeviceOptions$$' -fuzztime=$(FUZZ_TIME) -fuzzminimizetime=$(FUZZ_MINIMIZE_TIME) -parallel=$(FUZZ_PARALLEL) -timeout=$(FUZZ_TIMEOUT)
	$(GO) test ./internal/pkg/rendermetrics -run='^$$' -fuzz='^FuzzRenderGroup$$' -fuzztime=$(FUZZ_TIME) -fuzzminimizetime=$(FUZZ_MINIMIZE_TIME) -parallel=$(FUZZ_PARALLEL) -timeout=$(FUZZ_TIMEOUT)
	$(GO) test ./internal/pkg/transformation -run='^$$' -fuzz='^FuzzContainerGPUKeys$$' -fuzztime=$(FUZZ_TIME) -fuzzminimizetime=$(FUZZ_MINIMIZE_TIME) -parallel=$(FUZZ_PARALLEL) -timeout=$(FUZZ_TIMEOUT)

test-integration-host: generate ## Run host GPU/DCGM integration tests with binary coverage
	@rm -rf .coverdata/integration_binary
	@mkdir -p .coverdata/integration_binary
	@CGO_ENABLED=1 $(GO) build \
		-buildvcs=false \
		-cover -covermode=atomic -coverpkg=./internal/...,./pkg/... \
		-trimpath \
		-ldflags "-X main.BuildVersion=$(DCGM_VERSION)-$(VERSION)" \
		-o .coverdata/integration_binary/dcgm-exporter ./cmd/dcgm-exporter
	@GOCOVERDIR=$(CURDIR)/.coverdata/integration_binary $(GO) test -race -count=1 -timeout 5m -v $(TEST_ARGS) \
		./tests/host/ \
		-args -exporter-binary=$(CURDIR)/.coverdata/integration_binary/dcgm-exporter; \
	status=$$?; \
	if [ -n "$$(ls -A $(CURDIR)/.coverdata/integration_binary 2>/dev/null)" ]; then \
		$(GO) tool covdata textfmt \
			-i=$(CURDIR)/.coverdata/integration_binary \
			-o=integration_binary.cov.tmp && \
		grep -v "mock_" integration_binary.cov.tmp > integration_binary.cov && \
		rm -f integration_binary.cov.tmp && \
		$(GO) tool cover -func=integration_binary.cov | tee integration_binary.cov.func.txt; \
	else \
		echo "WARNING: no coverage data emitted (tests likely failed before any package was exercised)"; \
	fi; \
	exit $$status

integration-inprocess-coverage: generate ## Run in-process integration tests with coverage
	@rm -rf .coverdata/integration_inprocess
	@mkdir -p .coverdata/integration_inprocess
	@go test -count=1 -timeout 5m -short \
		-cover -covermode=count -coverpkg=./internal/pkg/... \
		./internal/pkg/integration_test/... \
		-args -test.gocoverdir=$(CURDIR)/.coverdata/integration_inprocess; \
	status=$$?; \
	if [ -n "$$(ls -A $(CURDIR)/.coverdata/integration_inprocess 2>/dev/null)" ]; then \
		$(GO) tool covdata textfmt \
			-i=$(CURDIR)/.coverdata/integration_inprocess \
			-o=integration_inprocess.cov.tmp && \
		grep -v "mock_" integration_inprocess.cov.tmp > integration_inprocess.cov && \
		rm -f integration_inprocess.cov.tmp && \
		$(GO) tool cover -func=integration_inprocess.cov | tee integration_inprocess.cov.func.txt; \
	else \
		echo "WARNING: no coverage data emitted (tests likely failed before any package was exercised)"; \
	fi; \
	exit $$status

test-coverage: ## Run unit and integration coverage, then merge profiles
	@echo "Preparing coverage data directories..."
	@rm -rf .coverdata
	@mkdir -p .coverdata/unit .coverdata/integration .coverdata/merged
	@echo "Running unit tests..."
	gotestsum --format testname -- \
		$$($(GO) list ./... | grep -v "/tests/k8s/") \
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
	grep -v -E "$(COVERAGE_EXCLUDE_PATTERN)" combined_coverage.out.tmp > tests.cov
	rm -rf combined_coverage.out.tmp .coverdata
	$(GO) tool cover -func=tests.cov

unit-test-coverage: ## Run CI-safe unit coverage without GPU/DCGM/NVML packages
	@echo "Running unit tests only (skipping integration tests and nvmlprovider)..."
	gotestsum --format testname --jsonfile test_results.json -- \
		$$($(GO) list ./... | grep -v -E "(tests/k8s|integration_test|nvmlprovider)") \
		-count=1 -timeout 5m \
		-covermode=count \
		-coverprofile=tests.cov \
		--short
	@echo "Filtering out mock files and non-unit-testable DCGM/main wrappers from coverage..."
	@if [ -f tests.cov ]; then \
		grep -v -E "$(COVERAGE_EXCLUDE_PATTERN)" tests.cov > tests.cov.tmp && \
			mv tests.cov.tmp tests.cov || true; \
	fi
	@echo "Unit test coverage completed"
	$(GO) tool cover -func=tests.cov

# ------------------------------------------------------------------------------
# Validation and Formatting
# ------------------------------------------------------------------------------

.PHONY: lint lint-shell hadolint lint-dockerfiles validate-modules validate
.PHONY: tools fmt goimports check-format check-fmt
lint: ## Run golangci-lint against changed Go code
	$(MKDIR) -p "$(GOLANGCILINT_TMPDIR)" "$(GOLANGCILINT_CACHE)"
	TMPDIR="$(GOLANGCILINT_TMPDIR)" GOLANGCI_LINT_CACHE="$(GOLANGCILINT_CACHE)" \
		GOOS=linux GOARCH=amd64 CGO_ENABLED=1 GOGC=50 \
		golangci-lint run ./... \
			--timeout $(GOLANGCILINT_TIMEOUT) \
			--new-from-rev="$(LINT_BASE_REV)" \
			--concurrency=2

lint-shell: ## Run shellcheck against repository shell scripts
	shellcheck -x \
		hack/utils.sh \
		hack/ci/e2e-image.sh \
		hack/ci/retry.sh \
		internal/e2e/capability/dcgmi_probe.sh \
		hack/package/*.sh \
		docker/build-cross.sh \
		docker/dcgm-exporter-entrypoint.sh

hadolint lint-dockerfiles: ## Lint Dockerfiles with hadolint
	@echo "Linting Dockerfiles with hadolint..."
	@if command -v hadolint > /dev/null 2>&1; then \
		hadolint docker/Dockerfile docker/package.Dockerfile; \
	elif docker inspect hadolint/hadolint > /dev/null 2>&1; then \
		docker run --rm -i -v "$(CURDIR)/.hadolint.yaml:/.config/hadolint.yaml:ro" \
			hadolint/hadolint < docker/Dockerfile && \
		docker run --rm -i -v "$(CURDIR)/.hadolint.yaml:/.config/hadolint.yaml:ro" \
			hadolint/hadolint < docker/package.Dockerfile; \
	else \
		echo "Error: hadolint not found. Install it or run: docker pull hadolint/hadolint"; \
		exit 1; \
	fi
	@echo "✓ All Dockerfiles passed hadolint checks"

validate-modules: ## Verify module contents and go.mod/go.sum tidiness
	@echo "- Verifying that the dependencies have expected content..."
	go mod verify
	@echo "- Checking for any unused/missing packages in go.mod..."
	go mod tidy
	@git diff --exit-code -- go.sum go.mod

validate: validate-modules hadolint check-fmt ## Run all validation checks
	@echo "✓ All validation checks passed"

tools: ## Install required tools and utilities
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)
	$(GO) install golang.org/x/tools/cmd/goimports@v$(GOIMPORTS_VERSION)
	$(GO) install mvdan.cc/gofumpt@v$(GOFUMPT_VERSION)
	$(GO) install gotest.tools/gotestsum@v$(GOTESTSUM_VERSION)

fmt: ## Format Go files with gofumpt
	find . -path './.go' -prune -o -name '*.go' -print | xargs gofumpt -l -w

goimports: ## Apply goimports using the module import prefix
	go list -f {{.Dir}} $(MODULE)/... \
		| xargs goimports -local $(MODULE) -w

check-format: ## Check gofmt formatting for pkg and cmd
	test $$(gofmt -l pkg | tee /dev/stderr | wc -l) -eq 0
	test $$(gofmt -l cmd | tee /dev/stderr | wc -l) -eq 0

check-fmt: ## Check goimports formatting for repository Go files
	@echo "Checking code formatting.  Any listed files don't match goimports:"
	! (find . \
		-path './.go' -prune -o \
		-path './internal/mocks' -prune -o \
		-path './third_party' -prune -o \
		-path './examples' -prune -o \
		-iname "*.go" -print \
		| xargs goimports -l -local $(MODULE) | grep .)

# ------------------------------------------------------------------------------
# Test Entrypoints and Local GPU Workflows
# ------------------------------------------------------------------------------

.PHONY: help-test
help-test: ## Show curated test target help
	@echo "Test targets:"
	@echo "  test-unit             No-GPU unit coverage used by default CI"
	@echo "  test-fuzz             Bounded native Go fuzz tests"
	@echo "  test-static           No-live-infra chart/source/package checks"
	@echo "  test-integration-host  Direct exporter/DCGM host integration suite"
	@echo "  test-integration-container  Container runtime integration suite"
	@echo "  test-integration-k8s  Kubernetes integration suite against a cluster"
	@echo "  test-e2e             Run e2e validation"

.PHONY: test-unit
test-unit: unit-test-coverage

.PHONY: test-static
test-static: ## Run no-live-infra static checks
	$(GO) test ./tests/static

.PHONY: test-integration-k8s
test-integration-k8s: ## Run the reusable Kubernetes integration suite
	cd tests/k8s && $(MAKE) test-integration-k8s

# ------------------------------------------------------------------------------
# Version Maintenance
# ------------------------------------------------------------------------------

.PHONY: install-uv sync-versions validate-versions
.PHONY: check-versions check-versions-apply
install-uv: ## Install uv at the pinned UV_VERSION
	@if command -v uv >/dev/null 2>&1 \
	   && [ "$$(uv --version 2>/dev/null | awk '{print $$2}')" = "$(UV_VERSION)" ]; then \
	  echo "uv $(UV_VERSION) already installed"; \
	else \
	  echo "Installing uv $(UV_VERSION)..."; \
	  curl -LsSf https://astral.sh/uv/$(UV_VERSION)/install.sh | sh; \
	fi

sync-versions: ## Propagate hack/versions.env to derived files (fetches Go SHA256s)
	@hack/sync-versions.py -v

validate-versions: ## Fail if derived files drift from hack/versions.env
	@hack/sync-versions.py --check

check-versions: ## Report outdated pinned versions
	@hack/check-versions.py

check-versions-apply: ## Apply available version updates to hack/versions.env
	@hack/check-versions.py apply

# ------------------------------------------------------------------------------
# Release Maintenance
# ------------------------------------------------------------------------------

.PHONY: bump-go-dcgm
# Set GO_DCGM_VERSION to a tag, branch, commit, pseudo-version, or "latest".
bump-go-dcgm: ## Bump go-dcgm after reporting DCGM field-map changes
	$(GO) run ./hack/bump-go-dcgm \
		-version="$(GO_DCGM_VERSION)" \
		-output="$(GO_DCGM_OUTPUT)" \
		-dry-run="$(GO_DCGM_DRY_RUN)" \
		-allow-field-removals="$(GO_DCGM_ALLOW_FIELD_REMOVALS)"

# ------------------------------------------------------------------------------
# Code Generation and Docker Image Tests
# ------------------------------------------------------------------------------

.PHONY: generate
generate: ## Generate code and mocks
	go generate ./...

.PHONY: test-integration-container
test-integration-container: ## Run container runtime validation tests
	@echo "Running container runtime tests..."
	cd tests/container && $(MAKE) container-test

.PHONY: test-e2e
test-e2e: build-e2e ## Build local prerequisites and run e2e validation
	$(MKDIR) -p bin
	CGO_ENABLED=1 $(GO) build \
		-buildvcs=false \
		-trimpath \
		-ldflags "-X main.BuildVersion=$(DCGM_VERSION)-$(VERSION)" \
		-o bin/dcgm-exporter ./cmd/dcgm-exporter
	@set -eu; \
	base_image="$(E2E_LOCAL_EXPORTER_BASE_IMAGE)"; \
	$(MAKE) local CONTAINER=distroless IMAGE_REF="$${base_image}"; \
	image_id="$$(docker image inspect --format '{{.Id}}' "$${base_image}")"; \
	image_suffix="$${image_id#sha256:}"; \
	image_suffix="$$(printf '%s' "$${image_suffix}" | cut -c1-12)"; \
	final_image="$${base_image}-$${image_suffix}"; \
	docker tag "$${base_image}" "$${final_image}"; \
	E2E_EXPORTER_IMAGE="$${final_image}" ./bin/e2e tests
