#!/usr/bin/env bash
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

# This script builds dcgm-exporter inside the cross-build container for the requested platform.

set -euo pipefail

TARGETOS="${TARGETOS:-linux}"
TARGETARCH="${TARGETARCH:-amd64}"

host_arch="$(uname -m)"
if [[ "${TARGETARCH}" == "arm64" && "${host_arch}" != "aarch64" ]]; then
    export CC=aarch64-linux-gnu-gcc
    export LD_LIBRARY_PATH="/usr/aarch64-linux-gnu/lib:${LD_LIBRARY_PATH:-}"
else
    export CC=gcc
fi

if [[ "${GOPROXY_ENABLED:-}" == "true" ]]; then
    module_cache="${GOMODCACHE:-/go/pkg/mod}"
    if [[ ! -d "${module_cache}" ]] || [[ -z "$(ls -A "${module_cache}" 2>/dev/null)" ]]; then
        echo "ERROR: hermetic build requires prepared Go modules" >&2
        exit 1
    fi

    echo "Hermetic build: using prepared modules in offline mode"
    export GOPROXY=off
    export GOSUMDB=off
    export GONOSUMDB='*'
fi

echo "Building dcgm-exporter for ${TARGETOS}/${TARGETARCH} using CC=${CC}"
GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" CGO_ENABLED=1 CC="${CC}" make install
echo "Build completed successfully"
