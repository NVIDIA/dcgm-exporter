#!/usr/bin/env bash
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.

# Run the selected container test mode with pinned image defaults and caller image overrides.

set -euo pipefail

mode="${1:-all}"
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck disable=SC1091
source "${root_dir}/hack/versions.env"

registry="${REGISTRY:-nvidia}"
version="${VERSION:-${EXPORTER_VERSION}}"
extra_env=("REGISTRY=${registry}" "VERSION=${version}")
ginkgo_args=(--ginkgo.v --ginkgo.no-color)

case "${mode}" in
    # distroless is kept as an alias of all so existing commands keep working.
    all | distroless) ;;
    verbose)
        ginkgo_args=(--ginkgo.v --ginkgo.vv --ginkgo.trace)
        ;;
    focus)
        : "${FOCUS:?error: FOCUS is required; for example: FOCUS=smoke task test:container:focus}"
        ginkgo_args=(--ginkgo.v "--ginkgo.focus=${FOCUS}")
        ;;
    *)
        echo "error: unsupported container test mode: ${mode}" >&2
        exit 2
        ;;
esac

# The remote DCGM specs skip when DCGM_IMAGE is empty or the image is not local, because the suite
# pulls a missing image only when E2E_REQUIRE_CONTAINER_IMAGES=1. So default DCGM_IMAGE to the
# pinned image and pull it. A failed pull only warns, so specs that do not need it still run.
if [[ ! ${DCGM_IMAGE+x} ]]; then
    dcgm_image="${DCGM_E2E_IMAGE_REPOSITORY}:${DCGM_VERSION}-${DCGM_IMAGE_TAG_SUFFIX}@${DCGM_E2E_IMAGE_DIGEST}"
    if ! docker image inspect "${dcgm_image}" >/dev/null 2>&1 && ! docker pull "${dcgm_image}"; then
        echo "warning: could not pull ${dcgm_image}; the remote DCGM specs will skip" >&2
    fi
    extra_env+=("DCGM_IMAGE=${dcgm_image}")
fi

cd "${root_dir}/tests/container"
env "${extra_env[@]}" \
go test --tags=container -count=1 -v . -args "${ginkgo_args[@]}"
