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
MKDIR                ?= mkdir
GOLANGCILINT_TIMEOUT ?= 10m

DCGM_VERSION   := $(NEW_DCGM_VERSION)
GOLANG_VERSION := 1.22.5
VERSION        := $(NEW_EXPORTER_VERSION)
FULL_VERSION   := $(DCGM_VERSION)-$(VERSION)
OUTPUT         := type=oci,dest=/dev/null
PLATFORMS      := linux/amd64,linux/arm64
DOCKERCMD      := docker buildx build
MODULE         := github.com/NVIDIA/dcgm-exporter


.PHONY: all binary install check-format local
all: update-version ubuntu22.04 ubi9

binary: generate update-version
	cd cmd/dcgm-exporter; $(GO) build -ldflags "-X main.BuildVersion=${DCGM_VERSION}-${VERSION}"

test-main:
	$(GO) test ./... -short

install: binary
	install -m 755 cmd/dcgm-exporter/dcgm-exporter /usr/bin/dcgm-exporter
	install -m 644 -D ./etc/default-counters.csv /etc/dcgm-exporter/default-counters.csv
	install -m 644 -D ./etc/dcp-metrics-included.csv /etc/dcgm-exporter/dcp-metrics-included.csv

check-format:
	test $$(gofmt -l pkg | tee /dev/stderr | wc -l) -eq 0
	test $$(gofmt -l cmd | tee /dev/stderr | wc -l) -eq 0

push: update-version
	$(MAKE) ubuntu22.04 OUTPUT=type=registry
	$(MAKE) ubi9 OUTPUT=type=registry

local:
ifeq ($(shell uname -p),aarch64)
	$(MAKE) PLATFORMS=linux/arm64 OUTPUT=type=docker DOCKERCMD='docker build'
else
	$(MAKE) PLATFORMS=linux/amd64 OUTPUT=type=docker DOCKERCMD='docker build'
endif

TARGETS = ubuntu22.04 ubi9

DOCKERFILE.ubuntu22.04 = docker/Dockerfile.ubuntu22.04
DOCKERFILE.ubi9 = docker/Dockerfile.ubi9

$(TARGETS):
	$(DOCKERCMD) --pull \
		--output $(OUTPUT) \
		--platform $(PLATFORMS) \
		--build-arg "GOLANG_VERSION=$(GOLANG_VERSION)" \
		--build-arg "DCGM_VERSION=$(DCGM_VERSION)" \
		--build-arg "VERSION=$(VERSION)" \
		--tag "$(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-$@" \
		--file $(DOCKERFILE.$@) .

.PHONY: integration
test-integration:
	go test -race -count=1 -timeout 5m -v $(TEST_ARGS) ./tests/integration/

test-coverage:
	sh scripts/test_coverage.sh
	gocov convert tests.cov  | gocov report

.PHONY: lint
lint:
	golangci-lint run ./... --timeout $(GOLANGCILINT_TIMEOUT)  --new-from-rev=HEAD~1 --fix

.PHONY: validate-modules
validate-modules:
	@echo "- Verifying that the dependencies have expected content..."
	go mod verify
	@echo "- Checking for any unused/missing packages in go.mod..."
	go mod tidy
	@git diff --exit-code -- go.sum go.mod

.PHONY: tools
tools: ## Install required tools and utilities
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.55.2
	go install github.com/axw/gocov/gocov@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install mvdan.cc/gofumpt@latest

fmt:
	find . -name '*.go' | xargs gofumpt -l -w

goimports:
	go list -f {{.Dir}} $(MODULE)/... \
		| xargs goimports -local $(MODULE) -w

check-fmt:
	@echo "Checking code formatting.  Any listed files don't match goimports:"
	! (find . -iname "*.go" \
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
