#!/usr/bin/env bash
# Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
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

# Run Bazel from the repository root with the configured cache.

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if bazel_command="$(command -v bazel)"; then
    :
else
    printf 'Bazel is not installed; run task deps:install first.\n' >&2
    exit 1
fi

cd "${ROOT_DIR}"

declare -a bazelrc_options=()
cache_rc="${DCGM_EXPORTER_BAZEL_CACHE_RC:-${ROOT_DIR}/.workspace/bazel-cache.bazelrc}"
if [[ -f "${cache_rc}" ]]; then
    bazelrc_options+=("--bazelrc=${cache_rc}")
fi

exec "${bazel_command}" "${bazelrc_options[@]}" "$@"
