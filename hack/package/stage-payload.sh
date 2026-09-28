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
    local metric_preset

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
    [[ "${EUID}" -eq 0 ]] || die "Package payload staging requires root-owned output"

    payload_root="${payload_root%/}"
    component_root="${payload_root}/${PACKAGE_COMPONENT_DIR}"

    rm -rf "${component_root}"
    mkdir -p "${component_root}"

    log_info "Staging package payload under ${component_root}"
    "${MAKE:-make}" -C "${ROOT_DIR}" install DESTDIR="${component_root}"
    for metric_preset in \
        default-counters.csv \
        dcp-metrics-included.csv \
        1.x-compatibility-metrics.csv; do
        install -o root -g root -m 0644 -D \
            "${ROOT_DIR}/etc/${metric_preset}" \
            "${component_root}/etc/dcgm-exporter/${metric_preset}"
    done
    install -m 644 -D \
        "${ROOT_DIR}/LICENSE" \
        "${component_root}/LICENSE"
    install -m 644 -D \
        "${ROOT_DIR}/THIRD_PARTY_NOTICES" \
        "${component_root}/THIRD_PARTY_NOTICES"
    install -m 644 -D \
        "${ROOT_DIR}/packaging/config-files/systemd/nvidia-dcgm-exporter.service" \
        "${component_root}/lib/systemd/system/nvidia-dcgm-exporter.service"

    test -x "${component_root}/usr/bin/dcgm-exporter"
    for metric_preset in \
        default-counters.csv \
        dcp-metrics-included.csv \
        1.x-compatibility-metrics.csv; do
        test -f "${component_root}/etc/dcgm-exporter/${metric_preset}"
        test "$(stat -c '%a:%u:%g' "${component_root}/etc/dcgm-exporter/${metric_preset}")" = \
            "644:0:0"
    done
    test -f "${component_root}/lib/systemd/system/nvidia-dcgm-exporter.service"
    test -s "${component_root}/LICENSE"
    test -s "${component_root}/THIRD_PARTY_NOTICES"
    log_info "Package payload staged"
}

main "$@"
