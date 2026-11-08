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
# Bazel artifact stage - supplies target-architecture release binaries.
# Docker only assembles these immutable Bazel outputs; it does not compile Go.
#####
FROM scratch AS bazel-artifacts

ARG TARGETARCH
COPY dist/staged/linux-${TARGETARCH}/bin/dcgm-exporter /usr/bin/dcgm-exporter
COPY dist/staged/linux-${TARGETARCH}/bin/dcgm-exporter-notice-generator /usr/bin/dcgm-exporter-notice-generator

#####
# Named distroless base - provides the final runtime filesystem
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/distroless/cc@sha256:298fd46ce9c1f90fc0a14d8006f9adc77394f831412c4df10d9e6155cd2adb24 AS runtime-distroless-base

#####
# Distroless helper stage - builds full Ubuntu container with DCGM libraries
#####
FROM --platform=$TARGETARCH nvcr.io/nvidia/cuda@sha256:07adb6792c59025f2c45bfb1a3f1d1148bf6d48bf2326d604a6b2e24c4f04d6f AS runtime-distroless-helper

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG TARGETARCH
ARG DCGM_VERSION

ENV DEBIAN_FRONTEND=noninteractive
# VSOCK requires libdcgm.so.4 to export dcgmConnect_v3, and Grace CPU
# serial labels require dcgmGetCpuHierarchy_v2. Keep these checks in the
# helper stage because the distroless final image copies libdcgm from here.
RUN apt-get -qq update && apt-get upgrade -y --no-install-recommends && \
	printf '%s\n' 'path-include=/usr/share/doc/*/third-party-notices.txt' > /etc/dpkg/dpkg.cfg.d/zz-third-party-notices && \
	DCGM_PACKAGE_VERSION="1:${DCGM_VERSION}-2" && \
	# APT prefers newer split-package builds over the requested DCGM build.
	# Remove this pin when APT can resolve the requested build without it.
	printf '%s\n' \
	    'Package: datacenter-gpu-manager-4* libdcgm dcgmi nv-hostengine' \
	    "Pin: version ${DCGM_PACKAGE_VERSION}" \
	    'Pin-Priority: 1000' \
	    > /etc/apt/preferences.d/dcgm && \
    apt-get -qq install -y --no-install-recommends \
    binutils \
    "datacenter-gpu-manager-4-core=${DCGM_PACKAGE_VERSION}" \
	"datacenter-gpu-manager-4-proprietary=${DCGM_PACKAGE_VERSION}" \
	libcap2-bin lshw && \
	rm /etc/apt/preferences.d/dcgm && \
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

COPY --chown=root:root --chmod=755 docker/dcgm-exporter-entrypoint.sh /usr/local/dcgm/dcgm-exporter-entrypoint.sh
RUN uname -a

# Stage the helper-derived runtime files, then combine the notices for the Go
# binary, the installed DCGM package, and the other packages those files use.
COPY --from=bazel-artifacts --chmod=755 /usr/bin/dcgm-exporter-notice-generator /usr/local/bin/dcgm-exporter-notice-generator
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
	install -d -m 0755 /opt/distroless-root/etc/dcgm-exporter && \
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
LABEL org.opencontainers.image.base.name="nvcr.io/nvidia/distroless/cc:v4.2.0"
LABEL org.opencontainers.image.base.digest="sha256:298fd46ce9c1f90fc0a14d8006f9adc77394f831412c4df10d9e6155cd2adb24"
LABEL com.nvidia.dcgm.version="${DCGM_VERSION}"
LABEL release="N/A"
LABEL summary="Exports GPU Metrics to Prometheus"
LABEL description="See summary"

# Copy licenses, binary, and configs
COPY --from=runtime-distroless-helper --chown=root:root /opt/notices/image-licenses/ ./licenses/
COPY --from=bazel-artifacts --chown=root:root --chmod=755 /usr/bin/dcgm-exporter /usr/bin/
COPY --chown=root:root --chmod=644 etc/* /etc/dcgm-exporter/

# Copy the fully inventoried helper-to-runtime filesystem delta.
COPY --from=runtime-distroless-helper --chown=root:root /opt/distroless-root/ /

ENV NVIDIA_DRIVER_CAPABILITIES=compute,utility,compat32
ENV NVIDIA_DISABLE_REQUIRE="true"
ENV NVIDIA_VISIBLE_DEVICES=all
# Let the container's DCGM read profiling fields of MIG-enabled GPUs and their
# instances; without MIG monitor access DCGM returns blank values for them.
ENV NVIDIA_MIG_MONITOR_DEVICES=all

# Load the shipped default configuration. A --config-file argument or a different
# DCGM_EXPORTER_CONFIG_FILE value replaces it. If /etc/dcgm-exporter is replaced by a
# mount that has no config.yaml, startup fails with an error that names the file.
ENV DCGM_EXPORTER_CONFIG_FILE=/etc/dcgm-exporter/config.yaml

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
# The distroless base provides the root account and group.
# hadolint ignore=DL3066
USER root:root

ENTRYPOINT ["/usr/bin/dcgm-exporter-entrypoint.sh"]
