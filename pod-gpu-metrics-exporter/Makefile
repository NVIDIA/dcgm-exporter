# Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.

.PHONY: all build
.DEFAULT_GOAL := all


##### Global variables #####

DOCKERFILE ?= $(CURDIR)/Dockerfile

REGISTRY ?= nvidia
TAG      ?= latest
TARGET   ?= pod-gpu-metrics-exporter


##### Public rules #####

all: build

build:
	docker build --pull \
		--tag $(REGISTRY)/$(TARGET):$(TAG) \
		--file $(DOCKERFILE) .

push:
	docker push $(REGISTRY)/$(TARGET):$(TAG)
