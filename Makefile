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

DCGM_VERSION   := 3.0.4
GOLANG_VERSION := 1.17
VERSION        := 3.0.0
FULL_VERSION   := $(DCGM_VERSION)-$(VERSION)
OUTPUT         := type=oci,dest=/tmp/dcgm-exporter.tar
PLATFORMS      := linux/amd64,linux/arm64
DOCKERCMD      := docker buildx build

NON_TEST_FILES  := pkg/dcgmexporter/dcgm.go pkg/dcgmexporter/gpu_collector.go pkg/dcgmexporter/parser.go
NON_TEST_FILES  += pkg/dcgmexporter/pipeline.go pkg/dcgmexporter/server.go pkg/dcgmexporter/system_info.go
NON_TEST_FILES  += pkg/dcgmexporter/types.go pkg/dcgmexporter/utils.go pkg/dcgmexporter/kubernetes.go
NON_TEST_FILES  += cmd/dcgm-exporter/main.go
MAIN_TEST_FILES := pkg/dcgmexporter/system_info_test.go

.PHONY: all binary install check-format local
all: ubuntu20.04 ubi8

binary:
	cd cmd/dcgm-exporter; go build -ldflags "-X main.BuildVersion=${DCGM_VERSION}-${VERSION}"

test-main: $(NON_TEST_FILES) $(MAIN_TEST_FILES)
	go test ./...

install: binary
	install -m 557 cmd/dcgm-exporter/dcgm-exporter /usr/bin/dcgm-exporter
	install -m 557 -D ./etc/default-counters.csv /etc/dcgm-exporter/default-counters.csv
	install -m 557 -D ./etc/dcp-metrics-included.csv /etc/dcgm-exporter/dcp-metrics-included.csv

check-format:
	test $$(gofmt -l pkg | tee /dev/stderr | wc -l) -eq 0
	test $$(gofmt -l cmd | tee /dev/stderr | wc -l) -eq 0

push:
	$(MAKE) ubuntu20.04 OUTPUT=type=registry
	$(MAKE) ubi8 OUTPUT=type=registry

local:
ifeq ($(shell uname -p),aarch64)
	$(MAKE) PLATFORMS=linux/arm64 OUTPUT=type=docker DOCKERCMD='docker build'
else
	$(MAKE) PLATFORMS=linux/amd64 OUTPUT=type=docker DOCKERCMD='docker build'
endif

ubuntu20.04:
	$(DOCKERCMD) --pull \
		--output $(OUTPUT) \
		--platform $(PLATFORMS) \
		--build-arg "GOLANG_VERSION=$(GOLANG_VERSION)" \
		--build-arg "DCGM_VERSION=$(DCGM_VERSION)" \
		--tag "$(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-ubuntu20.04" \
		--file docker/Dockerfile.ubuntu20.04 .

ubi8:
	$(DOCKERCMD) --pull \
		--output $(OUTPUT) \
		--platform $(PLATFORMS) \
		--build-arg "GOLANG_VERSION=$(GOLANG_VERSION)" \
		--build-arg "DCGM_VERSION=$(DCGM_VERSION)" \
		--build-arg "VERSION=$(FULL_VERSION)" \
		--tag "$(REGISTRY)/dcgm-exporter:$(FULL_VERSION)-ubi8" \
		--file docker/Dockerfile.ubi8 .
