# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

# The e2e local cluster builder overrides these from hack/versions.env. Defaults keep
# ad hoc docker builds valid and avoid BuildKit empty-ARG warnings.
ARG K3S_IMAGE=docker.io/rancher/k3s:v1.37.2-k3s1@sha256:21ea6e0fac4e3930c2ce8aa40ddd41a8b18cb3e9a0222c030dc005e7e88c4332
ARG CUDA_IMAGE=nvcr.io/nvidia/cuda:13.4.3-base-ubuntu24.04@sha256:07adb6792c59025f2c45bfb1a3f1d1148bf6d48bf2326d604a6b2e24c4f04d6f
ARG NVIDIA_CONTAINER_TOOLKIT_VERSION=1.20.1-1

FROM ${K3S_IMAGE} AS k3s
FROM ${CUDA_IMAGE}
ARG NVIDIA_CONTAINER_TOOLKIT_VERSION
LABEL com.nvidia.dcgm-exporter.e2e.nvidia-container-toolkit.version="${NVIDIA_CONTAINER_TOOLKIT_VERSION}"

# Keep the nested k3d node runtime in legacy mode. CDI works for non-MIG local
# k3d, but GB200 MIG validation failed when nvidia-ctk inside the agent tried to
# generate CDI specs: it could not read parent MIG memory attributes.
# The legacy runtime reads MIG capability files through the driver root. Point
# its driver lookup at the read-only host mount so the device plugin can start
# on systems that expose those files only through the host NVIDIA driver.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gnupg2 \
    && curl -fsSL -o /tmp/nvidia-container-toolkit.gpg https://nvidia.github.io/libnvidia-container/gpgkey \
    && gpg --dearmor --yes -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg /tmp/nvidia-container-toolkit.gpg \
    && curl -fsSL -o /tmp/nvidia-container-toolkit.list https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
    && sed -i 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' /tmp/nvidia-container-toolkit.list \
    && mv /tmp/nvidia-container-toolkit.list /etc/apt/sources.list.d/nvidia-container-toolkit.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        "nvidia-container-toolkit=${NVIDIA_CONTAINER_TOOLKIT_VERSION}" \
        "nvidia-container-toolkit-base=${NVIDIA_CONTAINER_TOOLKIT_VERSION}" \
        "libnvidia-container-tools=${NVIDIA_CONTAINER_TOOLKIT_VERSION}" \
        "libnvidia-container1=${NVIDIA_CONTAINER_TOOLKIT_VERSION}" \
    && sed -i 's/^mode = .*/mode = "legacy"/' /etc/nvidia-container-runtime/config.toml \
    && sed -i 's|^#root = "/run/nvidia/driver"|root = "/run/nvidia/host-driver"|' /etc/nvidia-container-runtime/config.toml \
    && grep -qx 'mode = "legacy"' /etc/nvidia-container-runtime/config.toml \
    && grep -qx 'root = "/run/nvidia/host-driver"' /etc/nvidia-container-runtime/config.toml \
    && dpkg-query --show --showformat='${Package}=${Version}\n' \
        nvidia-container-toolkit nvidia-container-toolkit-base \
        libnvidia-container-tools libnvidia-container1 \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/* /tmp/nvidia-container-toolkit.gpg

COPY --from=k3s /bin /k3s-bin

# k3d injects /bin/k3d-entrypoint.sh and that script expects /bin/k3s and a
# BusyBox-compatible /bin/sh, matching the stock rancher/k3s image.
RUN ln -sf /k3s-bin/k3s /usr/bin/k3s \
    && ln -sf /k3s-bin/busybox /usr/bin/sh \
    && mkdir -p /etc \
    && echo 'hosts: files dns' > /etc/nsswitch.conf \
    && chmod 1777 /tmp

ENV PATH=/var/lib/rancher/k3s/data/cni:/k3s-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/k3s-bin/aux
ENV CRI_CONFIG_FILE=/var/lib/rancher/k3s/agent/etc/crictl.yaml

VOLUME ["/var/lib/kubelet", "/var/lib/rancher/k3s", "/var/lib/cni", "/var/log"]

ENTRYPOINT ["/k3s-bin/k3s"]
CMD ["agent"]
