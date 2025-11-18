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

ARG BASEIMAGE=nvcr.io/nvidia/cuda:13.0.1-base-ubuntu22.04

#####
# Common builder stage - compiles dcgm-exporter with CGO for all targets
#####
FROM --platform=$BUILDPLATFORM ubuntu:22.04 AS builder

ARG GOLANG_VERSION=1.24.5

WORKDIR /go/src/github.com/NVIDIA/dcgm-exporter

# Install build dependencies with cache
RUN --mount=type=cache,sharing=locked,target=/var/cache/apt \
    --mount=type=cache,sharing=locked,target=/var/lib/apt/lists \
    apt-get update \
    && apt-get install -y --no-install-recommends \
    wget \
    ca-certificates \
    git \
    build-essential \
    gcc \
    gcc-aarch64-linux-gnu \
    qemu-user \
    qemu-system-arm \
    libc6-dev-arm64-cross \
    && apt-get autoremove -y \
    && ln -sf /usr/aarch64-linux-gnu/lib/ld-linux-aarch64.so.1 /lib/ld-linux-aarch64.so.1

# Install Go
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; arch="${arch##*-}"; \
    url=; \
    case "$arch" in \
    'amd64') \
    url="https://dl.google.com/go/go${GOLANG_VERSION}.linux-amd64.tar.gz"; \
    ;; \
    'arm64') \
    url="https://dl.google.com/go/go${GOLANG_VERSION}.linux-arm64.tar.gz"; \
    ;; \
    *) echo >&2 "error: unsupported architecture '$arch' (likely packaging update needed)"; exit 1 ;; \
    esac; \
    wget -O go.tgz "$url" --progress=dot:giga; \
    tar -C /usr/local -xzf go.tgz; \
    rm go.tgz

ENV GOTOOLCHAIN=local GOPATH=/go
ENV PATH=$GOPATH/bin:/usr/local/go/bin:$PATH
RUN mkdir -p "$GOPATH/src" "$GOPATH/bin" && chmod -R 700 "$GOPATH"

# Download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY internal/ internal/
COPY Makefile ./
COPY hack/ hack/
COPY etc/ etc/

# Copy and execute build script
COPY docker/build-cross.sh /usr/local/bin/build-cross.sh
RUN chmod +x /usr/local/bin/build-cross.sh

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    TARGETOS=$TARGETOS TARGETARCH=$TARGETARCH /usr/local/bin/build-cross.sh

# Verify binary
RUN apt-get update && apt-get install -y --no-install-recommends file && \
    rm -rf /var/lib/apt/lists/* && \
    file /usr/bin/dcgm-exporter

#####
# Ubuntu 22.04 runtime target
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/cuda:13.0.1-base-ubuntu22.04 AS runtime-ubuntu

ARG VERSION
ARG DCGM_VERSION
ARG TARGETARCH

LABEL io.k8s.display-name="NVIDIA DCGM Exporter"
LABEL name="NVIDIA DCGM Exporter"
LABEL vendor="NVIDIA"
LABEL version="${VERSION}"
LABEL release="N/A"
LABEL summary="Exports GPU Metrics to Prometheus"
LABEL description="See summary"

# Create non-root user for running the exporter
RUN groupadd -r dcgm-exporter -g 1000 \
    && useradd -r -g dcgm-exporter -u 1000 -m -s /sbin/nologin dcgm-exporter

# Copy binary and configs
WORKDIR /
COPY --chown=root:root --chmod=644 ./LICENSE ./licenses/LICENSE
COPY --from=builder --chown=root:root --chmod=755 /usr/bin/dcgm-exporter /usr/bin/
COPY --chown=root:root --chmod=755 etc /etc/dcgm-exporter
RUN chmod 644 /etc/dcgm-exporter/*

# Install DCGM packages
ENV DEBIAN_FRONTEND=noninteractive
RUN echo "$TARGETARCH" && apt-get -qq update && apt-get upgrade -y --no-install-recommends && \
    apt-get -qq install -y --no-install-recommends \
    datacenter-gpu-manager-4-core datacenter-gpu-manager-4-proprietary libcap2-bin && \
    apt-get -qq clean && apt-get -qq autoclean && apt-get -qq autoremove -y && \
    rm -rf /var/lib/apt/lists/* /var/log/* /tmp/* /var/tmp/* && \
    # ldconfig may segfault under QEMU ARM64 emulation during cross-platform builds.
    # The || true prevents build failures; ldconfig cache is optional (dynamic linker works without it).
    ldconfig || true

ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility,compat32
ENV NVIDIA_DISABLE_REQUIRE="true"
ENV NVIDIA_VISIBLE_DEVICES=all

COPY --chown=root:root --chmod=755 docker/dcgm-exporter-entrypoint.sh /usr/local/dcgm/dcgm-exporter-entrypoint.sh
RUN uname -a

# Security Note: Default USER
#
# This container runs as root by default because:
# 1. Docker's --cap-add only grants capabilities to root (UID 0), not non-root users
# 2. Profiling metrics (DCGM_FI_PROF_*) require CAP_SYS_ADMIN capability
# 3. Without root, profiling metrics cannot be collected even with --cap-add SYS_ADMIN
#
# Capability checking is done in Go code (internal/pkg/capabilities):
# - The application detects if CAP_SYS_ADMIN is available
# - Warns at startup if profiling metrics are requested but capability is missing
# - Basic metrics work as root without additional capabilities
# - Profiling metrics require: root + --cap-add SYS_ADMIN
#
# For maximum security with profiling metrics:
#   Docker: docker run --user 0 --cap-add SYS_ADMIN --cap-drop ALL ...
#   Kubernetes securityContext:
#     runAsUser: 0
#     capabilities:
#       add: ["SYS_ADMIN"]  # Required for profiling metrics
#       drop: ["ALL"]
#     allowPrivilegeEscalation: false
#
# For non-root without profiling metrics:
#   Docker: docker run --user 1000 ...
#   Kubernetes: runAsNonRoot: true, runAsUser: 1000
#   (Profiling metrics will not be available, but basic metrics will work)
#
# Security measures in place:
# - Non-root user (dcgm-exporter:1000) available for basic metrics
# - All files have restrictive permissions (644 for data, 755 for executables/directories)
# - Runtime capability checking with clear warnings
# - Deployment configs drop all unnecessary capabilities
USER root

ENTRYPOINT ["/usr/local/dcgm/dcgm-exporter-entrypoint.sh"]

#####
# UBI9 runtime target
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/cuda:13.0.1-base-ubi9 AS runtime-ubi

ARG VERSION
ARG DCGM_VERSION

LABEL io.k8s.display-name="NVIDIA DCGM Exporter"
LABEL name="NVIDIA DCGM Exporter"
LABEL vendor="NVIDIA"
LABEL version="${VERSION}"
LABEL release="N/A"
LABEL summary="Exports GPU Metrics to Prometheus"
LABEL description="See summary"

# Create non-root user for running the exporter
RUN groupadd -r dcgm-exporter -g 1000 \
    && useradd -r -g dcgm-exporter -u 1000 -m -s /sbin/nologin dcgm-exporter

# Install DCGM packages
RUN dnf update --disablerepo=* --enablerepo=ubi-9-appstream-rpms --enablerepo=ubi-9-baseos-rpms -y \
    && dnf upgrade --disablerepo=* --enablerepo=ubi-9-appstream-rpms --enablerepo=ubi-9-baseos-rpms -y \
    && dnf install --nodocs --setopt=install_weak_deps=False -y \
    datacenter-gpu-manager-4-core datacenter-gpu-manager-4-proprietary libcap \
    && dnf clean all \
    && rm -rf /var/cache/* /var/lib/dnf/history.* /var/log/* /tmp/* /var/tmp/* \
    # ldconfig may segfault under QEMU ARM64 emulation during cross-platform builds.
    # The || true prevents build failures; ldconfig cache is optional (dynamic linker works without it).
    && ldconfig || true

# Copy binary and configs
COPY --chown=root:root --chmod=644 ./LICENSE ./licenses/LICENSE
COPY --from=builder --chown=root:root --chmod=755 /usr/bin/dcgm-exporter /usr/bin/
COPY --chown=root:root --chmod=755 etc /etc/dcgm-exporter
RUN chmod 644 /etc/dcgm-exporter/*

ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DISABLE_REQUIRE="true"
ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility,compat32

COPY --chown=root:root --chmod=755 docker/dcgm-exporter-entrypoint.sh /usr/local/dcgm/dcgm-exporter-entrypoint.sh

# Security Note: Default USER
#
# This container runs as root by default because:
# 1. Docker's --cap-add only grants capabilities to root (UID 0), not non-root users
# 2. Profiling metrics (DCGM_FI_PROF_*) require CAP_SYS_ADMIN capability
# 3. Without root, profiling metrics cannot be collected even with --cap-add SYS_ADMIN
#
# Capability checking is done in Go code (internal/pkg/capabilities):
# - The application detects if CAP_SYS_ADMIN is available
# - Warns at startup if profiling metrics are requested but capability is missing
# - Basic metrics work as root without additional capabilities
# - Profiling metrics require: root + --cap-add SYS_ADMIN
#
# For maximum security with profiling metrics:
#   Docker: docker run --user 0 --cap-add SYS_ADMIN --cap-drop ALL ...
#   Kubernetes securityContext:
#     runAsUser: 0
#     capabilities:
#       add: ["SYS_ADMIN"]  # Required for profiling metrics
#       drop: ["ALL"]
#     allowPrivilegeEscalation: false
#
# For non-root without profiling metrics:
#   Docker: docker run --user 1000 ...
#   Kubernetes: runAsNonRoot: true, runAsUser: 1000
#   (Profiling metrics will not be available, but basic metrics will work)
#
# Security measures in place:
# - Non-root user (dcgm-exporter:1000) available for basic metrics
# - All files have restrictive permissions (644 for data, 755 for executables/directories)
# - Runtime capability checking with clear warnings
# - Deployment configs drop all unnecessary capabilities
USER root

ENTRYPOINT ["/usr/local/dcgm/dcgm-exporter-entrypoint.sh"]

#####
# Distroless helper stage - builds full Ubuntu container with DCGM libraries
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/cuda:13.0.1-base-ubuntu22.04 AS runtime-distroless-helper

ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get -qq update && apt-get upgrade -y --no-install-recommends && \
    apt-get -qq install -y --no-install-recommends \
    datacenter-gpu-manager-4-core datacenter-gpu-manager-4-proprietary libcap2-bin && \
    apt-get -qq clean && apt-get -qq autoclean && apt-get -qq autoremove -y && \
    rm -rf /var/lib/apt/lists/* /var/log/* /tmp/* /var/tmp/* && \
    # ldconfig may segfault under QEMU ARM64 emulation during cross-platform builds.
    # The || true prevents build failures; ldconfig cache is optional (dynamic linker works without it).
    ldconfig || true && \
    ls -l /usr/lib/*-linux-gnu/libdcgm*

COPY docker/dcgm-exporter-entrypoint.sh /usr/local/dcgm/dcgm-exporter-entrypoint.sh
RUN chmod +x /usr/local/dcgm/dcgm-exporter-entrypoint.sh
RUN uname -a

#####
# Distroless runtime target - minimal container image
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/distroless/cc:v3.1.13 AS runtime-distroless

ARG VERSION
ARG TARGETARCH

LABEL io.k8s.display-name="NVIDIA DCGM Exporter"
LABEL name="NVIDIA DCGM Exporter"
LABEL vendor="NVIDIA"
LABEL version="${VERSION}"
LABEL release="N/A"
LABEL summary="Exports GPU Metrics to Prometheus"
LABEL description="See summary"

# Copy binary and configs
COPY --chown=root:root --chmod=644 ./LICENSE ./licenses/LICENSE
COPY --from=builder --chown=root:root --chmod=755 /usr/bin/dcgm-exporter /usr/bin/
COPY --chown=root:root --chmod=755 etc /etc/dcgm-exporter

# Copy DCGM libraries from helper stage
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/lib/*-linux-gnu/libdcgm* /usr/lib/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/lib/*-linux-gnu/libnvperf_dcgm* /usr/lib/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/local/dcgm/dcgm-exporter-entrypoint.sh /usr/bin/

# Copy required utilities for runtime
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/sh /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/sh /bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/echo /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/sbin/setcap /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/lib/*-linux-gnu/libcap.so* /usr/lib/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/lib/*-linux-gnu/libtinfo.so* /usr/lib/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/env /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/bash /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/sbin/ldconfig.real /sbin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=644 /etc/passwd /etc/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=644 /etc/group /etc/

ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility,compat32
ENV NVIDIA_DISABLE_REQUIRE="true"
ENV NVIDIA_VISIBLE_DEVICES=all

# Security Note: Default USER
#
# This container runs as root by default because:
# 1. Docker's --cap-add only grants capabilities to root (UID 0), not non-root users
# 2. Profiling metrics (DCGM_FI_PROF_*) require CAP_SYS_ADMIN capability
# 3. Without root, profiling metrics cannot be collected even with --cap-add SYS_ADMIN
#
# Capability checking is done in Go code (internal/pkg/capabilities):
# - The application detects if CAP_SYS_ADMIN is available
# - Warns at startup if profiling metrics are requested but capability is missing
# - Basic metrics work as root without additional capabilities
# - Profiling metrics require: root + --cap-add SYS_ADMIN
#
# For maximum security with profiling metrics:
#   Docker: docker run --user 0 --cap-add SYS_ADMIN --cap-drop ALL ...
#   Kubernetes securityContext:
#     runAsUser: 0
#     capabilities:
#       add: ["SYS_ADMIN"]  # Required for profiling metrics
#       drop: ["ALL"]
#     allowPrivilegeEscalation: false
#
# For non-root without profiling metrics:
#   Docker: docker run --user 1000 ...
#   Kubernetes: runAsNonRoot: true, runAsUser: 1000
#   (Profiling metrics will not be available, but basic metrics will work)
#
# Security measures in place:
# - Non-root user (UID 1000) available in distroless base for basic metrics
# - All files have restrictive permissions (644 for data, 755 for executables/directories)
# - Runtime capability checking with clear warnings
# - Deployment configs drop all unnecessary capabilities
# - Distroless base image (minimal attack surface)
USER root:root

ENTRYPOINT ["/usr/bin/dcgm-exporter-entrypoint.sh"]

