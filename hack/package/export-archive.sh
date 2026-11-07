#!/usr/bin/env bash
# Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Export the Bazel-owned package tar under the historical release filename.
# Docker validates that payload on the oldest supported UBI/glibc baseline; it
# does not build, mutate, or export any package files.

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=hack/versions.env
source "${ROOT_DIR}/hack/versions.env"

architecture="${1:-}"
case "${architecture}" in
    amd64) archive_architecture=x86-64 ;;
    arm64) archive_architecture=sbsa ;;
    *) printf 'Usage: %s amd64|arm64\n' "$(basename "$0")" >&2; exit 2 ;;
esac

platform="linux/${architecture}"
validator_image="${PACKAGE_VALIDATOR_IMAGE:-${UBI8_IMAGE_REPOSITORY}:${UBI8_IMAGE_TAG}@${UBI8_IMAGE_DIGEST}}"
package_version="${PACKAGE_VERSION:-${EXPORTER_VERSION}}"
dist_name="dcgm_exporter-${DIST_PREFIX:-}linux-${archive_architecture}-${package_version}.tar.gz"
staged_archive="${ROOT_DIR}/dist/staged/linux-${architecture}/package/dcgm_exporter.tar.gz"

cd "${ROOT_DIR}"
bash hack/bazel/stage-artifacts.sh "${architecture}" package

validation_args=(
    --pull
    --provenance=false
    --output type=cacheonly
    --progress=plain
    --platform "${platform}"
    --target package-validator
    --build-arg "PACKAGE_VALIDATOR_IMAGE=${validator_image}"
    --file docker/package-validation.Dockerfile
)
if [[ ! -v DOCKER_NO_CACHE || "${DOCKER_NO_CACHE-}" == --no-cache ]]; then
    validation_args+=(--no-cache)
fi
docker buildx build "${validation_args[@]}" .

mkdir -p dist
install -m 0644 "${staged_archive}" "dist/${dist_name}"
printf 'Built %s\n' "${ROOT_DIR}/dist/${dist_name}"
