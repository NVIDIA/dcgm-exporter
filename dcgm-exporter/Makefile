# Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.

.PHONY: all build
.DEFAULT_GOAL := all


##### Global variables #####

DOCKERFILE ?= $(CURDIR)/Dockerfile

REGISTRY ?= nvidia
TAG      ?= latest
TARGET   ?= dcgm-exporter
DCGM     ?= 1.7.1

##### Public rules #####

all: build

build:
	docker build --pull \
		--build-arg "DCGM_VERSION=$(DCGM)" \
		--tag $(REGISTRY)/$(TARGET):$(TAG) \
		--file $(DOCKERFILE) .

push:
	docker push $(REGISTRY)/$(TARGET):$(TAG)
