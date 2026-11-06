# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

# This image validates, but never creates or exports, the Bazel package tar.
# UBI8 is the oldest supported glibc baseline for the host-package binary.
ARG PACKAGE_VALIDATOR_IMAGE=registry.access.redhat.com/ubi8/ubi@sha256:c322aa68f2d630b36b9faab64094357f62720be2f56f7099376ca2d6c4fe389b
# Hadolint cannot resolve the digest-pinned image supplied through this ARG.
# hadolint ignore=DL3006
FROM ${PACKAGE_VALIDATOR_IMAGE} AS package-validator

ARG TARGETARCH
ADD dist/staged/linux-${TARGETARCH}/package/dcgm_exporter.tar.gz /package-validation/
RUN /package-validation/dcgm_exporter/usr/bin/dcgm-exporter --version \
    && rm -rf /package-validation
