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

MKDIR    ?= mkdir
REGISTRY ?= nvidia

DCGM_VERSION   := 3.3.5
GOLANG_VERSION := 1.21.5
VERSION        := 3.4.0
FULL_VERSION   := $(DCGM_VERSION)-$(VERSION)
OUTPUT         := type=oci,dest=/tmp/dcgm-exporter.tar
PLATFORMS      := linux/amd64,linux/arm64
DOCKERCMD      := docker buildx build

.PHONY: all binary install check-format local
all: ubuntu22.04 ubi9

binary:
	cd cmd/dcgm-exporter; go build -ldflags "-X main.BuildVersion=${DCGM_VERSION}-${VERSION}"

test-main:
	go test ./... -short

install: binary
	install -m 755 cmd/dcgm-exporter/dcgm-exporter /usr/bin/dcgm-exporter
	install -m 644 -D ./etc/default-counters.csv /etc/dcgm-exporter/default-counters.csv
	install -m 644 -D ./etc/dcp-metrics-included.csv /etc/dcgm-exporter/dcp-metrics-included.csv

check-format:
	test $$(gofmt -l pkg | tee /dev/stderr | wc -l) -eq 0
	test $$(gofmt -l cmd | tee /dev/stderr | wc -l) -eq 0

push:
	$(MAKE) ubuntu22.04 OUTPUT=type=registry
	$(MAKE) ubi9 OUTPUT=type=registry

local:
ifeq ($(shell uname -p),aarch64)
	$(MAKE) PLATFORMS=linux/arm64 OUTPUT=type=docker DOCKERCMD='docker build'
else
	$(MAKE) PLATFORMS=linux/amd64 OUTPUT=type=docker DOCKERCMD='docker build'
endif

ubuntu22.04:
	$(DOCKERCMD) --pull \
		--output $(OUTPUT) \
		--platform $(PLATFORMS) \
		--build-arg "GOLANG_VERSION=$(GOLANG_VERSION)" \
		--build-arg "DCGM_VERSION=$(DCGM_VERSION)" \
		--tag "$(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-ubuntu22.04" \
		--file docker/Dockerfile.ubuntu22.04 .

ubi9:
	$(DOCKERCMD) --pull \
		--output $(OUTPUT) \
		--platform $(PLATFORMS) \
		--build-arg "GOLANG_VERSION=$(GOLANG_VERSION)" \
		--build-arg "DCGM_VERSION=$(DCGM_VERSION)" \
		--build-arg "VERSION=$(FULL_VERSION)" \
		--tag "$(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-ubi9" \
		--file docker/Dockerfile.ubi9 .

.PHONY: integration
test-integration:
	go test -race -count=1 -timeout 5m -v $(TEST_ARGS) ./tests/integration/

test-coverage:
	gocov test ./... | gocov report

.PHONY: lint
lint:
	golangci-lint run ./...

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
