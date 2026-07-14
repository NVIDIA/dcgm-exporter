#!/usr/bin/env bash
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

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HELPER="${ROOT_DIR}/hack/ci/e2e-image.sh"
test_count=0

assert_equals() {
    local want="$1"
    local got="$2"
    if [[ "${got}" != "${want}" ]]; then
        echo "Expected ${want}, got ${got}" >&2
        return 1
    fi
}

run_test() {
    local name="$1"
    shift
    test_count=$((test_count + 1))
    printf '=== RUN   %s\n' "${name}"
    "$@"
    printf -- '--- PASS: %s\n' "${name}"
}

reset_image_env() {
    unset ARCHITECTURE CONTAINER_REGISTRY CONTAINER_IMAGE_NAME IMAGE_VARIANT
    unset CI_COMMIT_TAG CI_COMMIT_BRANCH CI_DEFAULT_BRANCH CI_COMMIT_REF_SLUG CI_PIPELINE_IID CI_COMMIT_SHORT_SHA
    unset IMAGE_REPOSITORY IMAGE_TAG IMAGE_REF LATEST_TAG
}

test_branch_image_ref() {
    reset_image_env
    export ARCHITECTURE=amd64
    export CONTAINER_REGISTRY=registry.example
    export CONTAINER_IMAGE_NAME=dcgm/dcgm-exporter-dev
    export CI_COMMIT_BRANCH=feature
    export CI_DEFAULT_BRANCH=main
    export CI_COMMIT_REF_SLUG=feature-x
    export CI_PIPELINE_IID=42
    # shellcheck disable=SC1090,SC1091
    source "${HELPER}"

    assert_equals "registry.example/dcgm/dcgm-exporter-dev" "${IMAGE_REPOSITORY}"
    assert_equals "feature-x-42-distroless-amd64" "${IMAGE_TAG}"
    assert_equals "registry.example/dcgm/dcgm-exporter-dev:feature-x-42-distroless-amd64" "${IMAGE_REF}"
    assert_equals "" "${LATEST_TAG}"
}

test_default_branch_image_ref() {
    reset_image_env
    export ARCHITECTURE=arm64
    export CONTAINER_REGISTRY=registry.example
    export CONTAINER_IMAGE_NAME=dcgm/dcgm-exporter-dev
    export CI_COMMIT_BRANCH=main
    export CI_DEFAULT_BRANCH=main
    export CI_COMMIT_REF_SLUG=main
    export CI_PIPELINE_IID=43
    export CI_COMMIT_SHORT_SHA=abc1234
    # shellcheck disable=SC1090,SC1091
    source "${HELPER}"

    assert_equals "main-43-abc1234-distroless-arm64" "${IMAGE_TAG}"
    assert_equals "registry.example/dcgm/dcgm-exporter-dev:main-43-abc1234-distroless-arm64" "${IMAGE_REF}"
    assert_equals "registry.example/dcgm/dcgm-exporter-dev:latest-distroless-arm64" "${LATEST_TAG}"
}

test_release_tag_image_ref() {
    reset_image_env
    export ARCHITECTURE=amd64
    export CONTAINER_REGISTRY=registry.example
    export CONTAINER_IMAGE_NAME=dcgm/dcgm-exporter-dev
    export CI_COMMIT_TAG=v4.5.3-4.8.2
    # shellcheck disable=SC1090,SC1091
    source "${HELPER}"

    assert_equals "4.5.3-4.8.2-distroless-amd64" "${IMAGE_TAG}"
    assert_equals "registry.example/dcgm/dcgm-exporter-dev:4.5.3-4.8.2-distroless-amd64" "${IMAGE_REF}"
    assert_equals "" "${LATEST_TAG}"
}

test_branch_ubuntu_image_ref() {
    reset_image_env
    export ARCHITECTURE=amd64
    export IMAGE_VARIANT=ubuntu24.04
    export CONTAINER_REGISTRY=registry.example
    export CONTAINER_IMAGE_NAME=dcgm/dcgm-exporter-dev
    export CI_COMMIT_BRANCH=feature
    export CI_DEFAULT_BRANCH=main
    export CI_COMMIT_REF_SLUG=feature-x
    export CI_PIPELINE_IID=42
    # shellcheck disable=SC1090,SC1091
    source "${HELPER}"

    assert_equals "feature-x-42-ubuntu24.04-amd64" "${IMAGE_TAG}"
    assert_equals "registry.example/dcgm/dcgm-exporter-dev:feature-x-42-ubuntu24.04-amd64" "${IMAGE_REF}"
}

run_test test_branch_image_ref test_branch_image_ref
run_test test_default_branch_image_ref test_default_branch_image_ref
run_test test_release_tag_image_ref test_release_tag_image_ref
run_test test_branch_ubuntu_image_ref test_branch_ubuntu_image_ref
