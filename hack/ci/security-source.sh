#!/usr/bin/env bash
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Scan the exporter's reachable Go code with govulncheck and keep JSON and
# text reports. Exits nonzero on findings or scan errors.

set -Eeuo pipefail

ARG0="$(basename "$0")"
REPORT_DIR="${SECURITY_REPORT_DIR:-dist/security/source}"

# usage_info prints the short command summary.
usage_info() {
    printf 'usage: %s [--report-dir DIR]\n' "${ARG0}"
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

Scan reachable Go code in ./cmd/dcgm-exporter for known vulnerabilities.

Options:
  --report-dir DIR   report directory (env: SECURITY_REPORT_DIR, default: ${REPORT_DIR})
  -h, --help         show this help message
EOF
}

# flags parses command-line options.
flags() {
    local option
    local value

    while [[ $# -gt 0 ]]; do
        option="$1"
        value="${2:-}"
        case "${option}" in
            --report-dir)
                [[ -n "${value}" ]] || usage
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
    rm -f -- "${REPORT_DIR}"/{tool.txt,source-sha.txt,govulncheck.json,govulncheck.txt,error.txt}
}

# on_error records a short failure note beside the reports.
# shellcheck disable=SC2329 # Called by the ERR trap.
on_error() {
    local status=$?
    printf 'Source security scan failed (exit %s) at line %s; see job log.\n' \
        "${status}" "${BASH_LINENO[0]}" > "${REPORT_DIR}/error.txt"
}

# main records the scan inputs, then runs govulncheck once and renders its stream
# as text so findings appear in both reports. It exits directly so a finding is
# not also recorded as a script error.
main() {
    local status=0

    mkdir -p "${REPORT_DIR}"
    trap on_error ERR
    clear_reports

    govulncheck -version > "${REPORT_DIR}/tool.txt"
    git rev-parse HEAD > "${REPORT_DIR}/source-sha.txt"

    govulncheck -json ./cmd/dcgm-exporter > "${REPORT_DIR}/govulncheck.json" || status=1
    govulncheck -mode=convert < "${REPORT_DIR}/govulncheck.json" \
        | tee "${REPORT_DIR}/govulncheck.txt" || status=1

    exit "${status}"
}

flags "$@"
main
