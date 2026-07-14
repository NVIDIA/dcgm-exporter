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

#####
# Common builder stage - compiles dcgm-exporter with CGO for all targets
#####
ARG UBUNTU_IMAGE=ubuntu:26.04
FROM --platform=$BUILDPLATFORM ${UBUNTU_IMAGE} AS builder

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG GOLANG_VERSION=1.26.4

WORKDIR /go/src/github.com/NVIDIA/dcgm-exporter

# Install build dependencies with cache
RUN --mount=type=cache,sharing=locked,target=/var/cache/apt \
    --mount=type=cache,sharing=locked,target=/var/lib/apt/lists \
    apt-get update \
    && apt-get upgrade -y --no-install-recommends \
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

# Copy cached Go compiler and modules for offline hermetic builds.
# In regular mode these directories exist but are empty (created by Makefile/CI).
COPY .go/compiler/ .go/compiler/
COPY .go/pkg/mod/ /go/pkg/mod/

# Install Go - uses a cached compiler for offline builds, otherwise downloads
# with SHA256 verification for standard builds.
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; arch="${arch##*-}"; \
    if [ -f ".go/compiler/go${GOLANG_VERSION}.linux-${arch}.tar.gz" ]; then \
        echo "Using pre-cached Go compiler (hermetic build)"; \
        tar -C /usr/local -xzf ".go/compiler/go${GOLANG_VERSION}.linux-${arch}.tar.gz"; \
    else \
        echo "Downloading Go compiler from dl.google.com"; \
        filename="go${GOLANG_VERSION}.linux-${arch}.tar.gz"; \
        url="https://dl.google.com/go/${filename}"; \
        wget -O go.tgz "$url" --progress=dot:giga; \
        echo "Verifying SHA256 checksum..."; \
        wget -q -O go.sha256 "https://dl.google.com/go/${filename}.sha256"; \
        expected_sha256=$(cat go.sha256); \
        actual_sha256=$(sha256sum go.tgz | awk '{print $1}'); \
        if [ "$expected_sha256" != "$actual_sha256" ]; then \
            echo >&2 "error: SHA256 checksum verification failed"; \
            echo >&2 "expected: $expected_sha256"; \
            echo >&2 "actual:   $actual_sha256"; \
            exit 1; \
        fi; \
        echo "SHA256 checksum verified successfully"; \
        rm go.sha256; \
        tar -C /usr/local -xzf go.tgz; \
        rm go.tgz; \
    fi

ENV GOTOOLCHAIN=local GOPATH=/go
ENV PATH=$GOPATH/bin:/usr/local/go/bin:$PATH
RUN mkdir -p "$GOPATH/src" "$GOPATH/bin" && chmod -R 700 "$GOPATH"

# Go module settings for hermetic builds. Credentialed GOPROXY values are
# consumed from a BuildKit secret during module download and are never persisted.
ARG GOPROXY_ENABLED
ARG GONOSUMDB
ARG GOSUMDB
ENV GONOSUMDB=${GONOSUMDB}
ENV GOSUMDB=${GOSUMDB}

# Download dependencies - skipped when pre-cached modules exist (hermetic build)
COPY go.mod go.sum ./
RUN --mount=type=secret,id=goproxy \
    set -eu; \
    if [ -s /run/secrets/goproxy ]; then \
        GOPROXY="$(cat /run/secrets/goproxy)"; \
        export GOPROXY; \
    fi; \
    set -x; \
    if [ -d "/go/pkg/mod" ] && [ "$(ls -A /go/pkg/mod 2>/dev/null)" ]; then \
        echo "Using pre-cached Go modules (hermetic build)"; \
    else \
        echo "Downloading Go modules..."; \
        go mod download; \
    fi

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
    TARGETOS=$TARGETOS TARGETARCH=$TARGETARCH GOPROXY_ENABLED=$GOPROXY_ENABLED /usr/local/bin/build-cross.sh

# Verify binary
RUN apt-get update && apt-get upgrade -y --no-install-recommends && \
    apt-get install -y --no-install-recommends file && \
    rm -rf /var/lib/apt/lists/* && \
    file /usr/bin/dcgm-exporter

#####
# Ubuntu runtime target
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/cuda:13.3.0-base-ubuntu26.04 AS runtime-ubuntu

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

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

# Ensure UID/GID 1000 can be used for non-root runs; some base images already provide them.
RUN set -eux; \
    group_entry="$(getent group 1000 || true)"; \
    group_name="${group_entry%%:*}"; \
    if [ -z "$group_name" ]; then \
        groupadd -r dcgm-exporter -g 1000; \
        group_name=dcgm-exporter; \
    fi; \
    if ! id -u dcgm-exporter >/dev/null 2>&1 && ! getent passwd 1000 >/dev/null; then \
        useradd -r -g "$group_name" -u 1000 -m -s /sbin/nologin dcgm-exporter; \
    fi

# Copy binary and configs
WORKDIR /
COPY --chown=root:root --chmod=644 ./LICENSE ./licenses/LICENSE
COPY --from=builder --chown=root:root --chmod=755 /usr/bin/dcgm-exporter /usr/bin/
COPY --chown=root:root --chmod=755 etc /etc/dcgm-exporter
RUN chmod 644 /etc/dcgm-exporter/*

# Install DCGM packages
# VSOCK requires libdcgm.so.4 to export dcgmConnect_v3, and Grace CPU
# serial labels require dcgmGetCpuHierarchy_v2. Keep the symbol checks below
# so apt repository changes cannot silently ship an older library.
ENV DEBIAN_FRONTEND=noninteractive
RUN echo "$TARGETARCH" && apt-get -qq update && apt-get upgrade -y --no-install-recommends && \
    DCGM_PACKAGE_VERSION="1:${DCGM_VERSION}-1" && \
    apt-get -qq install -y --no-install-recommends \
    binutils \
    "datacenter-gpu-manager-4-core=${DCGM_PACKAGE_VERSION}" \
    "datacenter-gpu-manager-4-proprietary=${DCGM_PACKAGE_VERSION}" \
    libcap2-bin lshw && \
    nm -D --defined-only /usr/lib/*-linux-gnu/libdcgm.so.4 | grep -w dcgmConnect_v3 && \
    nm -D --defined-only /usr/lib/*-linux-gnu/libdcgm.so.4 | grep -w dcgmGetCpuHierarchy_v2 && \
    apt-get -qq purge -y --auto-remove binutils && \
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
# - UID/GID 1000 non-root identity available for basic metrics
# - All files have restrictive permissions (644 for data, 755 for executables/directories)
# - Runtime capability checking with clear warnings
# - Deployment configs drop all unnecessary capabilities
USER root

ENTRYPOINT ["/usr/local/dcgm/dcgm-exporter-entrypoint.sh"]

#####
# Distroless helper stage - builds full Ubuntu container with DCGM libraries
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/cuda:13.3.0-base-ubuntu24.04 AS runtime-distroless-helper

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG TARGETARCH
ARG DCGM_VERSION

ENV DEBIAN_FRONTEND=noninteractive
# VSOCK requires libdcgm.so.4 to export dcgmConnect_v3, and Grace CPU
# serial labels require dcgmGetCpuHierarchy_v2. Keep these checks in the
# helper stage because the distroless final image copies libdcgm from here.
RUN apt-get -qq update && apt-get upgrade -y --no-install-recommends && \
    DCGM_PACKAGE_VERSION="1:${DCGM_VERSION}-1" && \
    apt-get -qq install -y --no-install-recommends \
    binutils \
    "datacenter-gpu-manager-4-core=${DCGM_PACKAGE_VERSION}" \
    "datacenter-gpu-manager-4-proprietary=${DCGM_PACKAGE_VERSION}" \
    libcap2-bin lshw && \
    nm -D --defined-only /usr/lib/*-linux-gnu/libdcgm.so.4 | grep -w dcgmConnect_v3 && \
    nm -D --defined-only /usr/lib/*-linux-gnu/libdcgm.so.4 | grep -w dcgmGetCpuHierarchy_v2 && \
    apt-get -qq purge -y --auto-remove binutils && \
    apt-get -qq clean && apt-get -qq autoclean && apt-get -qq autoremove -y && \
    rm -rf /var/lib/apt/lists/* /var/log/* /tmp/* /var/tmp/* && \
    # ldconfig may segfault under QEMU ARM64 emulation during cross-platform builds.
    # The || true prevents build failures; ldconfig cache is optional (dynamic linker works without it).
    ldconfig || true && \
    ls -l /usr/lib/*-linux-gnu/libdcgm*

COPY docker/dcgm-exporter-entrypoint.sh /usr/local/dcgm/dcgm-exporter-entrypoint.sh
RUN chmod +x /usr/local/dcgm/dcgm-exporter-entrypoint.sh
RUN uname -a

# Prepare libraries in a staging directory with correct architecture-specific structure
RUN set -e; \
    ARCH_DIR=$(ls -d /usr/lib/*-linux-gnu | head -n1 | xargs basename); \
    mkdir -p /opt/distroless-libs/$ARCH_DIR; \
    cp -P /usr/lib/$ARCH_DIR/libdcgm* /opt/distroless-libs/$ARCH_DIR/ || true; \
    cp -P /usr/lib/$ARCH_DIR/libnvperf_dcgm* /opt/distroless-libs/$ARCH_DIR/; \
    ln -s libnvperf_dcgm_host.so /opt/distroless-libs/$ARCH_DIR/libnvperf_host.so; \
    test -e /opt/distroless-libs/$ARCH_DIR/libnvperf_host.so; \
    cp -P /usr/lib/$ARCH_DIR/libcap.so* /opt/distroless-libs/$ARCH_DIR/ || true; \
    cp -P /usr/lib/$ARCH_DIR/libtinfo.so* /opt/distroless-libs/$ARCH_DIR/ || true; \
    install -D -m 755 "$(if [ -e /sbin/ldconfig.real ]; then echo /sbin/ldconfig.real; else echo /sbin/ldconfig; fi)" /opt/distroless-bin/ldconfig; \
    ls -la /opt/distroless-libs/$ARCH_DIR/

#####
# Distroless runtime target - minimal container image
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/distroless/cc:v4.0.8 AS runtime-distroless

ARG VERSION
ARG TARGETARCH

WORKDIR /

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

# Copy libraries with correct architecture-specific directory structure from helper stage
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /opt/distroless-libs/ /usr/lib/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/local/dcgm/dcgm-exporter-entrypoint.sh /usr/bin/

# Copy required utilities for runtime
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/sh /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/sh /bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/lshw /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/lshw /bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/sbin/setcap /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/env /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /usr/bin/bash /usr/bin/
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /opt/distroless-bin/ldconfig /sbin/
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
# Security measures in place:
# - All files have restrictive permissions (644 for data, 755 for executables/directories)
# - Runtime capability checking with clear warnings
# - Deployment configs drop all unnecessary capabilities
# - Distroless base image (minimal attack surface)
USER root:root

ENTRYPOINT ["/usr/bin/dcgm-exporter-entrypoint.sh"]
