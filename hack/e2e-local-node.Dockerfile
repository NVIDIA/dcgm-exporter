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
ARG K3S_IMAGE=rancher/k3s:v1.36.2-k3s1
ARG CUDA_IMAGE=nvcr.io/nvidia/cuda:13.3.0-base-ubuntu24.04

FROM ${K3S_IMAGE} AS k3s
FROM ${CUDA_IMAGE}

# Keep the nested k3d node runtime in legacy mode. CDI works for non-MIG local
# k3d, but GB200 MIG validation failed when nvidia-ctk inside the agent tried to
# generate CDI specs: it could not read parent MIG memory attributes.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gnupg2 \
    && curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
        | gpg --dearmor --yes -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg \
    && curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
        | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
        > /etc/apt/sources.list.d/nvidia-container-toolkit.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends nvidia-container-toolkit \
    && sed -i 's/^mode = .*/mode = "legacy"/' /etc/nvidia-container-runtime/config.toml \
    && sed -i 's|^#root = "/run/nvidia/driver"|root = "/run/nvidia/host-driver"|' /etc/nvidia-container-runtime/config.toml \
    && grep -qx 'mode = "legacy"' /etc/nvidia-container-runtime/config.toml \
    && grep -qx 'root = "/run/nvidia/host-driver"' /etc/nvidia-container-runtime/config.toml \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

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
