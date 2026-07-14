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

# Build the files that go into the dcgm-exporter package tarball.
#
# The package image calls this script to create PAYLOAD_ROOT/dcgm_exporter.
# It runs `make install DESTDIR=...`, adds the license and systemd unit, and
# verifies the install tree before the Makefile turns it into dist/*.tar.gz.

set -Eeuo pipefail

ARG0="$(basename "$0")"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
export LOG_PREFIX="stage-package-payload"

# shellcheck disable=SC1091
# shellcheck source=hack/utils.sh
source "${ROOT_DIR}/hack/utils.sh"

PACKAGE_COMPONENT_DIR="${PACKAGE_COMPONENT_DIR:-dcgm_exporter}"

# usage_info prints the short command summary.
usage_info() {
    cat <<EOF
Usage: ${ARG0} PAYLOAD_ROOT

Build the package file tree under:
  PAYLOAD_ROOT/${PACKAGE_COMPONENT_DIR}
EOF
}

# usage prints the short command summary to stderr and exits with a usage error.
usage() {
    exec 1>&2
    usage_info
    exit 1
}

# help prints the full command and option help.
help() {
    usage_info
    cat <<EOF

Environment:
  PACKAGE_PAYLOAD_ROOT  Payload root used when PAYLOAD_ROOT is omitted
  PACKAGE_COMPONENT_DIR  Component directory name inside the tarball
                         (default: ${PACKAGE_COMPONENT_DIR})
EOF
    exit 0
}

# main builds the package file tree and verifies the expected install layout.
main() {
    local payload_root="${PACKAGE_PAYLOAD_ROOT:-}"
    local component_root

    case "$#" in
        0)
            ;;
        1)
            case "$1" in
                -h|--help|help)
                    help
                    ;;
                -*)
                    die "Unsupported option ${1}"
                    ;;
                *)
                    payload_root="$1"
                    ;;
            esac
            ;;
        *)
            die "Unexpected argument: ${2}"
            ;;
    esac

    [[ -n "${payload_root}" ]] || die "PAYLOAD_ROOT is required"
    [[ -n "${PACKAGE_COMPONENT_DIR}" ]] || die "PACKAGE_COMPONENT_DIR is required"
    [[ "${PACKAGE_COMPONENT_DIR}" != */* ]] || die "PACKAGE_COMPONENT_DIR must not contain slashes"

    payload_root="${payload_root%/}"
    component_root="${payload_root}/${PACKAGE_COMPONENT_DIR}"

    rm -rf "${component_root}"
    mkdir -p "${component_root}"

    log_info "Staging package payload under ${component_root}"
    "${MAKE:-make}" -C "${ROOT_DIR}" install DESTDIR="${component_root}"
    install -m 644 -D \
        "${ROOT_DIR}/etc/dcp-metrics-included.csv" \
        "${component_root}/etc/dcgm-exporter/dcp-metrics-included.csv"
    install -m 644 -D \
        "${ROOT_DIR}/etc/1.x-compatibility-metrics.csv" \
        "${component_root}/etc/dcgm-exporter/1.x-compatibility-metrics.csv"
    install -m 644 -D \
        "${ROOT_DIR}/LICENSE" \
        "${component_root}/LICENSE"
    install -m 644 -D \
        "${ROOT_DIR}/packaging/config-files/systemd/nvidia-dcgm-exporter.service" \
        "${component_root}/lib/systemd/system/nvidia-dcgm-exporter.service"

    test -x "${component_root}/usr/bin/dcgm-exporter"
    test -f "${component_root}/etc/dcgm-exporter/default-counters.csv"
    test -f "${component_root}/etc/dcgm-exporter/dcp-metrics-included.csv"
    test -f "${component_root}/etc/dcgm-exporter/1.x-compatibility-metrics.csv"
    test -f "${component_root}/lib/systemd/system/nvidia-dcgm-exporter.service"
    log_info "Package payload staged"
}

main "$@"
