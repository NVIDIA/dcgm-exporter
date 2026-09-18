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
ARG UBUNTU_IMAGE=docker.io/library/ubuntu:26.04@sha256:cd21a4f68a617580279d4b091cb18e3af9fa8a87500665f0ae5f7f757d17d367
FROM --platform=$BUILDPLATFORM ${UBUNTU_IMAGE} AS builder

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG GOLANG_VERSION=1.27.1
ARG GO_LINUX_AMD64_SHA256=63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445
ARG GO_LINUX_ARM64_SHA256=3450b45a3f9ee8568792736a5c5e70a1f2e9b36c35a8f74958c03e51d7d92bec
ARG BUILDARCH
ARG TARGETARCH
ARG GOPROXY_ENABLED

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
    && if [ "$BUILDARCH" != "$TARGETARCH" ]; then \
        if [ "$BUILDARCH" = amd64 ] && [ "$TARGETARCH" = arm64 ]; then \
            apt-get install -y --no-install-recommends \
                gcc-aarch64-linux-gnu \
                libc6-dev-arm64-cross \
            && ln -sf /usr/aarch64-linux-gnu/lib/ld-linux-aarch64.so.1 /lib/ld-linux-aarch64.so.1; \
        else \
            echo >&2 "unsupported cross-build pair: ${BUILDARCH} -> ${TARGETARCH}"; \
            exit 1; \
        fi; \
    fi \
    && apt-get autoremove -y

# Copy cached Go compiler and modules for offline hermetic builds.
# In regular mode these directories exist but are empty (created by Makefile/CI).
COPY .go/compiler/ .go/compiler/
COPY .go/pkg/mod/ /go/pkg/mod/

# Install Go - uses a cached compiler for offline builds, otherwise downloads
# with SHA256 verification for standard builds.
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; arch="${arch##*-}"; \
    case "$arch" in \
        amd64) expected_sha256="${GO_LINUX_AMD64_SHA256}" ;; \
        arm64) expected_sha256="${GO_LINUX_ARM64_SHA256}" ;; \
        *) echo >&2 "unsupported Go compiler architecture: $arch"; exit 1 ;; \
    esac; \
    if [ "${GOPROXY_ENABLED:-}" = "true" ] && [ ! -s ".go/compiler/go${GOLANG_VERSION}.linux-${arch}.tar.gz" ]; then \
        echo >&2 "error: hermetic container build requires the prepared Go ${GOLANG_VERSION} ${arch} compiler"; \
        exit 1; \
    fi; \
    if [ -f ".go/compiler/go${GOLANG_VERSION}.linux-${arch}.tar.gz" ]; then \
        echo "Using pre-cached Go compiler (hermetic build)"; \
        tar -C /usr/local -xzf ".go/compiler/go${GOLANG_VERSION}.linux-${arch}.tar.gz"; \
    else \
        echo "Downloading Go compiler from dl.google.com"; \
        filename="go${GOLANG_VERSION}.linux-${arch}.tar.gz"; \
        url="https://dl.google.com/go/${filename}"; \
        wget -O go.tgz "$url" --progress=dot:giga; \
        echo "Verifying SHA256 checksum..."; \
        actual_sha256=$(sha256sum go.tgz | awk '{print $1}'); \
        if [ "$expected_sha256" != "$actual_sha256" ]; then \
            echo >&2 "error: SHA256 checksum verification failed"; \
            echo >&2 "expected: $expected_sha256"; \
            echo >&2 "actual:   $actual_sha256"; \
            exit 1; \
        fi; \
        echo "SHA256 checksum verified successfully"; \
        tar -C /usr/local -xzf go.tgz; \
        rm go.tgz; \
    fi

ENV GOTOOLCHAIN=local GOPATH=/go
ENV PATH=$GOPATH/bin:/usr/local/go/bin:$PATH
RUN mkdir -p "$GOPATH/src" "$GOPATH/bin" && chmod -R 700 "$GOPATH"

# Go module settings for hermetic builds. Credentialed GOPROXY values are
# consumed from a BuildKit secret during module download and are never persisted.
ARG GONOSUMDB
ARG GOSUMDB
ARG GOWORK=off
ENV GONOSUMDB=${GONOSUMDB}
ENV GOSUMDB=${GOSUMDB}
ENV GOWORK=${GOWORK}

# Download dependencies - skipped when pre-cached modules exist (hermetic build)
COPY go.mod go.sum ./
COPY .workspace/ .workspace/
RUN --mount=type=secret,id=goproxy \
    set -eu; \
    if [ -s /run/secrets/goproxy ]; then \
        GOPROXY="$(cat /run/secrets/goproxy)"; \
        export GOPROXY; \
    fi; \
    set -x; \
    if [ -d "/go/pkg/mod" ] && [ "$(ls -A /go/pkg/mod 2>/dev/null)" ]; then \
        echo "Using pre-cached Go modules (hermetic build)"; \
    elif [ "${GOPROXY_ENABLED:-}" = "true" ]; then \
        echo >&2 "error: hermetic container build requires prepared Go modules"; \
        exit 1; \
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
    TARGETOS=$TARGETOS TARGETARCH=$TARGETARCH GOPROXY_ENABLED=$GOPROXY_ENABLED /usr/local/bin/build-cross.sh

# Build the target-architecture notice assembler that runs in the distroless
# helper stage. It uses only the Go standard library and is statically linked.
RUN --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 \
    go build -buildvcs=false -trimpath \
        -o /usr/bin/dcgm-exporter-notice-generator \
        ./hack/licenses

# Verify binary
RUN apt-get update && apt-get upgrade -y --no-install-recommends && \
    apt-get install -y --no-install-recommends file && \
    rm -rf /var/lib/apt/lists/* && \
    file /usr/bin/dcgm-exporter /usr/bin/dcgm-exporter-notice-generator

#####
# Ubuntu runtime target
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/cuda:13.3.1-base-ubuntu26.04@sha256:30f7a2345973fbb584f4c8bc9a7c16c7bae6d0ec08883df4e030387d74756292 AS runtime-ubuntu

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG VERSION
ARG DCGM_VERSION
ARG SOURCE_REVISION
ARG TARGETARCH

LABEL io.k8s.display-name="NVIDIA DCGM Exporter"
LABEL name="NVIDIA DCGM Exporter"
LABEL vendor="NVIDIA"
LABEL version="${VERSION}"
LABEL org.opencontainers.image.title="NVIDIA DCGM Exporter"
LABEL org.opencontainers.image.description="Exports NVIDIA GPU metrics in Prometheus format"
LABEL org.opencontainers.image.vendor="NVIDIA"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${SOURCE_REVISION}"
LABEL org.opencontainers.image.source="https://github.com/NVIDIA/dcgm-exporter"
LABEL org.opencontainers.image.documentation="https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html"
LABEL org.opencontainers.image.url="https://catalog.ngc.nvidia.com/orgs/nvidia/teams/k8s/containers/dcgm-exporter"
LABEL org.opencontainers.image.base.name="nvcr.io/nvidia/cuda:13.3.1-base-ubuntu26.04"
LABEL org.opencontainers.image.base.digest="sha256:30f7a2345973fbb584f4c8bc9a7c16c7bae6d0ec08883df4e030387d74756292"
LABEL com.nvidia.dcgm.version="${DCGM_VERSION}"
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
# Named distroless base - provides the final runtime filesystem
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/distroless/cc:v4.1.4@sha256:b1deb9e97732ab5da2f1f6e0d3ac1e56bdc2df0a5bf900180662a4740f05957c AS runtime-distroless-base

#####
# Distroless helper stage - builds full Ubuntu container with DCGM libraries
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/cuda:13.3.1-base-ubuntu24.04@sha256:22dc6de51e33966f735440e9601a3006c38a8e6da4b4896b8ebc826e41252ae1 AS runtime-distroless-helper

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG TARGETARCH
ARG DCGM_VERSION

ENV DEBIAN_FRONTEND=noninteractive
# VSOCK requires libdcgm.so.4 to export dcgmConnect_v3, and Grace CPU
# serial labels require dcgmGetCpuHierarchy_v2. Keep these checks in the
# helper stage because the distroless final image copies libdcgm from here.
RUN apt-get -qq update && apt-get upgrade -y --no-install-recommends && \
	printf '%s\n' 'path-include=/usr/share/doc/*/third-party-notices.txt' > /etc/dpkg/dpkg.cfg.d/zz-third-party-notices && \
	DCGM_PACKAGE_VERSION="1:${DCGM_VERSION}-1" && \
    apt-get -qq install -y --no-install-recommends \
    binutils \
    "datacenter-gpu-manager-4-core=${DCGM_PACKAGE_VERSION}" \
	"datacenter-gpu-manager-4-proprietary=${DCGM_PACKAGE_VERSION}" \
	libcap2-bin lshw && \
	test "$(dpkg-query -W -f='${Version}' datacenter-gpu-manager-4-core)" = "${DCGM_PACKAGE_VERSION}" && \
	test -s /usr/share/doc/datacenter-gpu-manager-4/third-party-notices.txt && \
	nm -D --defined-only /usr/lib/*-linux-gnu/libdcgm.so.4 | grep -w dcgmConnect_v3 && \
	nm -D --defined-only /usr/lib/*-linux-gnu/libdcgm.so.4 | grep -w dcgmGetCpuHierarchy_v2 && \
	rm /etc/dpkg/dpkg.cfg.d/zz-third-party-notices && \
	apt-get -qq purge -y --auto-remove binutils && \
    apt-get -qq clean && apt-get -qq autoclean && apt-get -qq autoremove -y && \
    rm -rf /var/log/* /tmp/* /var/tmp/* && \
    # Package installation creates the cache. The final image ships it, so
    # verify the startup lookup before staging.
    test -s /etc/ld.so.cache && \
    grep -aFq 'libdcgm.so.4' /etc/ld.so.cache && \
    ls -l /usr/lib/*-linux-gnu/libdcgm*

COPY docker/dcgm-exporter-entrypoint.sh /usr/local/dcgm/dcgm-exporter-entrypoint.sh
RUN chmod +x /usr/local/dcgm/dcgm-exporter-entrypoint.sh
RUN uname -a

# Stage the helper-derived runtime files, then combine the notices for the Go
# binary, the installed DCGM package, and the other packages those files use.
COPY --from=builder --chmod=755 /usr/bin/dcgm-exporter-notice-generator /usr/local/bin/dcgm-exporter-notice-generator
COPY --chmod=755 hack/licenses/stage-container-runtime.sh /usr/local/bin/stage-container-runtime.sh
COPY --chmod=644 THIRD_PARTY_NOTICES /opt/notices/go/THIRD_PARTY_NOTICES
COPY --chmod=644 LICENSE /opt/notices/image-licenses/LICENSE
RUN /usr/local/bin/stage-container-runtime.sh \
		/opt/distroless-root \
		/opt/notices/runtime-manifest.tsv && \
	/usr/local/bin/dcgm-exporter-notice-generator \
		-mode container \
		-architecture "linux/${TARGETARCH}" \
		-go-notices /opt/notices/go/THIRD_PARTY_NOTICES \
		-dcgm-notices /usr/share/doc/datacenter-gpu-manager-4/third-party-notices.txt \
		-runtime-manifest /opt/notices/runtime-manifest.tsv \
		-helper-root / \
		-notices /opt/notices/final/THIRD_PARTY_NOTICES && \
	test -s /opt/notices/final/THIRD_PARTY_NOTICES && \
	install -d -m 0755 /opt/notices/image-licenses && \
	install -m 0644 /opt/notices/final/THIRD_PARTY_NOTICES /opt/notices/image-licenses/THIRD_PARTY_NOTICES && \
	chmod 0644 /opt/notices/image-licenses/LICENSE && \
	rm -rf /var/lib/apt/lists/*

#####
# Distroless runtime target - minimal container image
#####
FROM runtime-distroless-base AS runtime-distroless

ARG VERSION
ARG DCGM_VERSION
ARG SOURCE_REVISION
ARG TARGETARCH

WORKDIR /

LABEL io.k8s.display-name="NVIDIA DCGM Exporter"
LABEL name="NVIDIA DCGM Exporter"
LABEL vendor="NVIDIA"
LABEL version="${VERSION}"
LABEL org.opencontainers.image.title="NVIDIA DCGM Exporter"
LABEL org.opencontainers.image.description="Exports NVIDIA GPU metrics in Prometheus format"
LABEL org.opencontainers.image.vendor="NVIDIA"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${SOURCE_REVISION}"
LABEL org.opencontainers.image.source="https://github.com/NVIDIA/dcgm-exporter"
LABEL org.opencontainers.image.documentation="https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html"
LABEL org.opencontainers.image.url="https://catalog.ngc.nvidia.com/orgs/nvidia/teams/k8s/containers/dcgm-exporter"
LABEL org.opencontainers.image.base.name="nvcr.io/nvidia/distroless/cc:v4.1.4"
LABEL org.opencontainers.image.base.digest="sha256:b1deb9e97732ab5da2f1f6e0d3ac1e56bdc2df0a5bf900180662a4740f05957c"
LABEL com.nvidia.dcgm.version="${DCGM_VERSION}"
LABEL release="N/A"
LABEL summary="Exports GPU Metrics to Prometheus"
LABEL description="See summary"

# Copy licenses, binary, and configs
COPY --from=runtime-distroless-helper --chown=root:root /opt/notices/image-licenses/ ./licenses/
COPY --from=builder --chown=root:root --chmod=755 /usr/bin/dcgm-exporter /usr/bin/
COPY --chown=root:root --chmod=755 etc /etc/dcgm-exporter

# Copy the fully inventoried helper-to-runtime filesystem delta.
COPY --from=runtime-distroless-helper --chown=root:root /opt/distroless-root/ /

ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility,compat32
ENV NVIDIA_DISABLE_REQUIRE="true"
ENV NVIDIA_VISIBLE_DEVICES=all

# Remove unused helper binaries from the base image that are built with an
# older Go toolchain and carry known CVEs (e.g. CVE-2025-68121).
# dcgm-exporter does not reference these binaries.
COPY --from=runtime-distroless-helper --chown=root:root --chmod=755 /bin/rm /bin/rm
RUN rm -f /usr/bin/shelless_ulimit_* /usr/bin/sleep_* /bin/rm

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
