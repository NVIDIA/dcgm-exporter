#!/usr/bin/env bash
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.

# Check that a built exporter image has the expected architecture and labels
# and that its --version runs. Run it through task build:image:verify, which
# sets:
#
#   ARCH             architecture the image must be built for
#   IMAGE_REF        image to check (default: the image that task
#                    build:image:local tags)
#   SOURCE_REVISION  commit the revision label must name (default: HEAD)
#
# The expected versions and base image come from hack/versions.env, which the
# task loads.

set -euo pipefail

# fail prints an error and stops the script.
fail() {
    echo "ERROR: $*" >&2
    exit 1
}

# image_field prints one field of the image, selected by the docker inspect
# template $1.
image_field() {
    docker image inspect --format "$1" "${IMAGE_REF}"
}

# assert_label fails when image label $1 is not $2.
assert_label() {
    local label="$1" expected="$2"
    local actual

    actual="$(image_field "{{ index .Config.Labels \"${label}\" }}")"
    [[ "${actual}" == "${expected}" ]] || fail "${label}: expected '${expected}', got '${actual}'"
}

# main checks the architecture and labels, then runs --version.
main() {
    : "${ARCH:?ARCH is required}"
    : "${IMAGE_REF:?IMAGE_REF is required}"
    : "${DCGM_VERSION:?DCGM_VERSION is required}"
    : "${EXPORTER_VERSION:?EXPORTER_VERSION is required}"
    : "${DISTROLESS_IMAGE_REPOSITORY:?DISTROLESS_IMAGE_REPOSITORY is required}"
    : "${DISTROLESS_TAG:?DISTROLESS_TAG is required}"
    : "${DISTROLESS_IMAGE_DIGEST:?DISTROLESS_IMAGE_DIGEST is required}"

    local source_revision architecture

    source_revision="${SOURCE_REVISION:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"

    architecture="$(image_field '{{.Architecture}}')"
    [[ "${architecture}" == "${ARCH}" ]] || fail "${IMAGE_REF} is built for ${architecture}, expected ${ARCH}"

    assert_label org.opencontainers.image.title "NVIDIA DCGM Exporter"
    assert_label org.opencontainers.image.description "Exports NVIDIA GPU metrics in Prometheus format"
    assert_label org.opencontainers.image.vendor "NVIDIA"
    assert_label org.opencontainers.image.version "${EXPORTER_VERSION}"
    assert_label org.opencontainers.image.revision "${source_revision}"
    assert_label org.opencontainers.image.source "https://github.com/NVIDIA/dcgm-exporter"
    assert_label org.opencontainers.image.documentation "https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html"
    assert_label org.opencontainers.image.url "https://catalog.ngc.nvidia.com/orgs/nvidia/teams/k8s/containers/dcgm-exporter"
    assert_label org.opencontainers.image.base.name "${DISTROLESS_IMAGE_REPOSITORY}:${DISTROLESS_TAG}"
    assert_label org.opencontainers.image.base.digest "${DISTROLESS_IMAGE_DIGEST}"
    assert_label com.nvidia.dcgm.version "${DCGM_VERSION}"

    docker run --rm "${IMAGE_REF}" --version
}

main "$@"
