#!/usr/bin/env bash
# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=hack/versions.env
source "${ROOT_DIR}/hack/versions.env"

# main prints Bazel's stable workspace metadata.
main() {
    printf 'STABLE_BUILD_VERSION %s\n' "${EXPORTER_VERSION}"
    printf 'STABLE_DCGM_VERSION %s\n' "${DCGM_VERSION}"
    printf 'STABLE_SOURCE_REVISION %s\n' "$(git -C "${ROOT_DIR}" rev-parse HEAD 2>/dev/null || printf 'unknown')"
    printf 'STABLE_IMAGE_CREATED %s\n' "$(git -C "${ROOT_DIR}" show -s --format=%cI HEAD 2>/dev/null || printf '1970-01-01T00:00:00Z')"
}

main "$@"
