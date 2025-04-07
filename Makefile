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
IMAGE_TAG            ?= ""

DCGM_VERSION   := $(NEW_DCGM_VERSION)
GOLANG_VERSION := 1.23.7
VERSION        := $(NEW_EXPORTER_VERSION)
FULL_VERSION   := $(DCGM_VERSION)-$(VERSION)
OUTPUT         := type=oci,dest=/dev/null
PLATFORMS      := linux/amd64,linux/arm64
DOCKERCMD      := docker --debug buildx build
MODULE         := github.com/NVIDIA/dcgm-exporter

.PHONY: all binary install check-format local
all: ubuntu22.04 ubi9

binary:
	cd cmd/dcgm-exporter; $(GO) build -ldflags "-X main.BuildVersion=${DCGM_VERSION}-${VERSION}"

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

local:
ifeq ($(shell uname -p),aarch64)
	$(MAKE) PLATFORMS=linux/arm64 OUTPUT=type=docker DOCKERCMD='docker build'
else
	$(MAKE) PLATFORMS=linux/amd64 OUTPUT=type=docker DOCKERCMD='docker build'
endif

ubi%: DOCKERFILE = docker/Dockerfile.ubi
ubi%: --docker-build-%
	@
ubi9: BASE_IMAGE = nvcr.io/nvidia/cuda:12.8.1-base-ubi9
ubi9: IMAGE_TAG = ubi9

ubuntu%: DOCKERFILE = docker/Dockerfile.ubuntu
ubuntu%: --docker-build-%
	@
ubuntu22.04: BASE_IMAGE = nvcr.io/nvidia/cuda:12.8.1-base-ubuntu22.04
ubuntu22.04: IMAGE_TAG = ubuntu22.04


--docker-build-%:
	@echo "Building for $@"
	DOCKER_BUILDKIT=1 \
	$(DOCKERCMD) --pull \
		--output $(OUTPUT) \
		--progress=plain \
		--no-cache \
		--platform $(PLATFORMS) \
		--build-arg BASEIMAGE="$(BASE_IMAGE)" \
		--build-arg "GOLANG_VERSION=$(GOLANG_VERSION)" \
		--build-arg "DCGM_VERSION=$(DCGM_VERSION)" \
		--build-arg "VERSION=$(VERSION)" \
		--tag $(REGISTRY)/dcgm-exporter:$(FULL_VERSION)$(if $(IMAGE_TAG),-$(IMAGE_TAG)) \
		--file $(DOCKERFILE) .

.PHONY: packages package-arm64 package-amd64
packages: package-amd64 package-arm64

package-arm64:
	$(MAKE) package-build PLATFORMS=linux/arm64

package-amd64:
	$(MAKE) package-build PLATFORMS=linux/amd64

package-build: IMAGE_TAG = ubuntu22.04
package-build:
	ARCH=`echo $(PLATFORMS) | cut -d'/' -f2)`; \
	if [ "$$ARCH" = "amd64" ]; then \
		ARCH="x86-64"; \
	fi; \
	if [ "$$ARCH" = "arm64" ]; then \
		ARCH="sbsa"; \
	fi; \
	export DIST_NAME="dcgm_exporter-linux-$$ARCH-$(VERSION)"; \
	export COMPONENT_NAME="dcgm_exporter"; \
	$(MAKE) ubuntu22.04 OUTPUT=type=docker PLATFORMS=$(PLATFORMS) && \
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

test-coverage:
	sh scripts/test_coverage.sh
	gocov convert tests.cov  | gocov report

.PHONY: lint
lint:
	golangci-lint run ./... --timeout $(GOLANGCILINT_TIMEOUT)  --new-from-rev=HEAD~1

.PHONY: validate-modules
validate-modules:
	@echo "- Verifying that the dependencies have expected content..."
	go mod verify
	@echo "- Checking for any unused/missing packages in go.mod..."
	go mod tidy
	@git diff --exit-code -- go.sum go.mod

.PHONY: tools
tools: ## Install required tools and utilities
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.62.2
	go install github.com/axw/gocov/gocov@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install mvdan.cc/gofumpt@latest
	go install github.com/wadey/gocovmerge@latest

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
