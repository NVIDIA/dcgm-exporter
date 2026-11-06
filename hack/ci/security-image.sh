#!/usr/bin/env bash
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scan one exact exporter image: its Go binary with govulncheck, its packages
# with Syft and Grype, and its file metadata. Keeps every report and exits
# nonzero on findings or scan errors.

set -Eeuo pipefail

ARG0="$(basename "$0")"
REPORT_DIR="${SECURITY_REPORT_DIR:-dist/security/image}"
IMAGE_REF="${IMAGE_REF:-}"
ARCHITECTURE="${ARCHITECTURE:-}"
BUILD_METADATA="${SECURITY_IMAGE_METADATA:-}"
IMAGE_ARCHIVE="${SECURITY_IMAGE_ARCHIVE:-}"
SCAN_BINARY="${SECURITY_SCAN_BINARY:-}"
IMAGE_ID=""
CONTAINER_ID=""

# usage_info prints the short command summary.
usage_info() {
    printf 'usage: %s [--image REF] [--arch ARCH] [--metadata FILE | --archive FILE] [--scan-binary FILE] [--report-dir DIR]\n' "${ARG0}"
}

# usage prints the short command summary to stderr and exits with a usage error.
usage() {
    usage_info >&2
    exit 2
}

# help prints full command and option help.
help() {
    usage_info
    cat <<EOF

Scan an exact image for known vulnerabilities and unsafe file metadata.

Options:
  --image REF        image to scan (env: IMAGE_REF, required)
  --arch ARCH        image architecture, such as amd64 (env: ARCHITECTURE, required)
  --metadata FILE    Buildx metadata file; scan its pushed digest (env: SECURITY_IMAGE_METADATA)
  --archive FILE     load the image from a docker save archive (env: SECURITY_IMAGE_ARCHIVE)
  --scan-binary FILE unstripped build of the image's exporter for govulncheck
                     (env: SECURITY_SCAN_BINARY); without it, govulncheck scans
                     the stripped image binary and matches whole modules
  --report-dir DIR   report directory (env: SECURITY_REPORT_DIR, default: ${REPORT_DIR})
  -h, --help         show this help message
EOF
}

# need_value fails when an option that requires a value is missing one.
need_value() {
    local value="${1:-}"

    [[ -n "${value}" ]] || usage
}

# flags parses command-line options over the environment defaults.
flags() {
    local option
    local value

    while [[ $# -gt 0 ]]; do
        option="$1"
        value="${2:-}"
        case "${option}" in
            --image)
                need_value "${value}"
                IMAGE_REF="${value}"
                shift 2
                ;;
            --arch)
                need_value "${value}"
                ARCHITECTURE="${value}"
                shift 2
                ;;
            --metadata)
                need_value "${value}"
                BUILD_METADATA="${value}"
                shift 2
                ;;
            --archive)
                need_value "${value}"
                IMAGE_ARCHIVE="${value}"
                shift 2
                ;;
            --scan-binary)
                need_value "${value}"
                SCAN_BINARY="${value}"
                shift 2
                ;;
            --report-dir)
                need_value "${value}"
                REPORT_DIR="${value}"
                shift 2
                ;;
            -h|--help)
                help
                exit 0
                ;;
            *)
                usage
                ;;
        esac
    done
}

# clear_reports removes this scan's previous outputs and keeps other files.
clear_reports() {
    rm -f -- \
        "${REPORT_DIR}"/{image.json,image.tar,image-archive-sha256.txt,build-metadata.json} \
        "${REPORT_DIR}"/{govulncheck-version.txt,grype-version.txt,syft-version.txt} \
        "${REPORT_DIR}"/{dcgm-exporter,binary-sha256.txt,scan-binary-sha256.txt,govulncheck.json,govulncheck.txt} \
        "${REPORT_DIR}"/{filesystem.json,sbom.json,grype.json,error.txt}
}

# on_error records a short failure note beside the reports.
# shellcheck disable=SC2329 # Called by the ERR trap.
on_error() {
    local status=$?
    printf 'Image security scan failed (exit %s) at line %s; see job log.\n' \
        "${status}" "${BASH_LINENO[0]}" > "${REPORT_DIR}/error.txt"
}

# cleanup removes the temporary image archive, binary copy, and container.
# shellcheck disable=SC2329 # Called by the EXIT trap.
cleanup() {
    rm -f -- "${REPORT_DIR}/image.tar" "${REPORT_DIR}/dcgm-exporter"
    if [[ -n "${CONTAINER_ID}" ]]; then
        docker rm -f "${CONTAINER_ID}" >/dev/null || true
    fi
}

# fail records a message as the scan error and exits.
fail() {
    local message="$1"

    printf 'ERROR: %s\n' "${message}" | tee "${REPORT_DIR}/error.txt" >&2
    exit 1
}

# pin_pushed_digest replaces the image tag with the digest Buildx pushed.
pin_pushed_digest() {
    local digest

    cp "${BUILD_METADATA}" "${REPORT_DIR}/build-metadata.json"
    digest="$(jq -er '.["containerimage.digest"] | select(test("^sha256:[0-9a-f]{64}$"))' \
        "${REPORT_DIR}/build-metadata.json")"
    IMAGE_REF="${IMAGE_REF%%@*}@${digest}"
}

# load_image loads the saved archive, or pulls the image for this architecture.
load_image() {
    if [[ -n "${IMAGE_ARCHIVE}" ]]; then
        docker load --input "${IMAGE_ARCHIVE}"
    else
        docker pull --platform "linux/${ARCHITECTURE}" "${IMAGE_REF}"
    fi
}

# inspect_image records the image and checks its architecture and, in CI, its
# source revision label.
inspect_image() {
    local revision_label='{{index .Config.Labels "org.opencontainers.image.revision"}}'

    IMAGE_ID="$(docker image inspect --format '{{.Id}}' "${IMAGE_REF}")"
    docker image inspect "${IMAGE_ID}" > "${REPORT_DIR}/image.json"

    [[ "$(docker image inspect --format '{{.Architecture}}' "${IMAGE_ID}")" == "${ARCHITECTURE}" ]]

    if [[ -n "${CI_COMMIT_SHA:-}" ]]; then
        [[ "$(docker image inspect --format "${revision_label}" "${IMAGE_ID}")" == "${CI_COMMIT_SHA}" ]]
    fi
}

# save_image saves the exact local image. Layer tar headers keep capabilities
# and modes that a container export may drop; every scanner reads this archive.
save_image() {
    docker save --output "${REPORT_DIR}/image.tar" "${IMAGE_ID}"
    sha256sum "${REPORT_DIR}/image.tar" > "${REPORT_DIR}/image-archive-sha256.txt"
}

# record_tool_versions keeps the scanner versions beside their reports.
record_tool_versions() {
    govulncheck -version > "${REPORT_DIR}/govulncheck-version.txt"
    grype version > "${REPORT_DIR}/grype-version.txt"
    syft version > "${REPORT_DIR}/syft-version.txt"
}

# copy_binary copies the exporter binary out of the image and records its hash.
copy_binary() {
    CONTAINER_ID="$(docker create --platform "linux/${ARCHITECTURE}" "${IMAGE_ID}")"
    docker cp "${CONTAINER_ID}:/usr/bin/dcgm-exporter" "${REPORT_DIR}/dcgm-exporter"
    sha256sum "${REPORT_DIR}/dcgm-exporter" > "${REPORT_DIR}/binary-sha256.txt"
}

# module_list prints a Go binary's main path and module versions, without the
# file name or build settings, so two builds of one source compare equal.
module_list() {
    go version -m "$1" | awk '$1 == "path" || $1 == "mod" || $1 == "dep" || $1 == "=>"'
}

# section_digests prints a SHA-256 for each code and data section of a Go
# binary. Stripping removes only the symbol and debug sections, so a stripped
# and an unstripped link of the same build print the same digests. It fails
# when objcopy cannot read the binary, so an unread binary never compares equal.
section_digests() {
    local section
    local digest

    for section in .text .rodata .data .noptrdata .gopclntab; do
        digest="$(objcopy --output-target=binary --only-section="${section}" "$1" /dev/stdout | sha256sum | cut -d' ' -f1)" \
            || return 1
        printf '%s %s\n' "${section}" "${digest}"
    done
}

# check_scan_binary confirms the scan binary has symbols and holds the same
# modules, code and data as the image binary, so its findings apply to the image.
check_scan_binary() {
    local image_sections
    local scan_sections

    sha256sum "${SCAN_BINARY}" > "${REPORT_DIR}/scan-binary-sha256.txt"

    if ! go tool nm "${SCAN_BINARY}" > /dev/null 2>&1; then
        fail "Scan binary has no symbol table: ${SCAN_BINARY}"
    fi

    if ! diff <(module_list "${REPORT_DIR}/dcgm-exporter") <(module_list "${SCAN_BINARY}"); then
        fail "Scan binary modules differ from the image binary: ${SCAN_BINARY}"
    fi

    if ! image_sections="$(section_digests "${REPORT_DIR}/dcgm-exporter")"; then
        fail "Cannot read the image binary's sections with objcopy"
    fi
    if ! scan_sections="$(section_digests "${SCAN_BINARY}")"; then
        fail "Cannot read the scan binary's sections with objcopy: ${SCAN_BINARY}"
    fi
    if [[ "${image_sections}" != "${scan_sections}" ]]; then
        fail "Scan binary code or data differs from the image binary: ${SCAN_BINARY}"
    fi
}

# scan_binary runs govulncheck on the unstripped scan binary when one is given,
# otherwise on the image binary, and renders its stream as text.
scan_binary() {
    local binary="${REPORT_DIR}/dcgm-exporter"
    local status=0

    if [[ -n "${SCAN_BINARY}" ]]; then
        binary="${SCAN_BINARY}"
    fi

    govulncheck -mode=binary -json "${binary}" > "${REPORT_DIR}/govulncheck.json" || status=1
    govulncheck -mode=convert < "${REPORT_DIR}/govulncheck.json" \
        | tee "${REPORT_DIR}/govulncheck.txt" || status=1
    return "${status}"
}

# check_file_metadata checks ownership, modes, and capabilities in the image.
check_file_metadata() {
    uv run --script hack/ci/security-image-metadata.py \
        "${REPORT_DIR}/image.tar" "${REPORT_DIR}/image.json" > "${REPORT_DIR}/filesystem.json"
}

# scan_packages catalogs the image once with Syft, then scans that SBOM with
# Grype. The Grype JSON includes its database and effective configuration.
scan_packages() {
    SYFT_CHECK_FOR_APP_UPDATE=false SYFT_GOLANG_CAPTURE_SYMBOLS=all \
        syft "docker-archive:${REPORT_DIR}/image.tar" -o syft-json > "${REPORT_DIR}/sbom.json" || return 1
    grype --config hack/ci/security-grype.yaml "sbom:${REPORT_DIR}/sbom.json" > "${REPORT_DIR}/grype.json"
}

# main resolves the exact image, then runs every scan and fails if any failed.
# It exits directly so a finding is not also recorded as a script error.
main() {
    local status=0

    mkdir -p "${REPORT_DIR}"
    trap on_error ERR
    clear_reports

    if [[ -z "${IMAGE_REF}" || -z "${ARCHITECTURE}" ]]; then
        fail "IMAGE_REF and ARCHITECTURE are required."
    fi

    if [[ -n "${BUILD_METADATA}" ]]; then
        pin_pushed_digest
    fi
    load_image
    inspect_image

    # Job cancellation sends SIGTERM; exit so the EXIT trap still cleans up.
    trap cleanup EXIT
    trap 'exit 143' TERM
    trap 'exit 130' INT
    save_image
    record_tool_versions
    copy_binary
    if [[ -n "${SCAN_BINARY}" ]]; then
        check_scan_binary
    fi

    scan_binary || status=1
    check_file_metadata || status=1
    scan_packages || status=1

    exit "${status}"
}

flags "$@"
main
