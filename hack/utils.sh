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

# This script provides shared logging and failure helpers for repository shell scripts.

log_prefix() {
    printf '%s' "${LOG_PREFIX:-$(basename "$0")}"
}

log_info() {
    printf '[%s] %s\n' "$(log_prefix)" "$*"
}

log_warn() {
    printf '[%s] WARNING: %s\n' "$(log_prefix)" "$*" >&2
}

log_error() {
    printf '[%s] ERROR: %s\n' "$(log_prefix)" "$*" >&2
}

die() {
    log_error "$@"
    exit 1
}

has_command() {
    command -v "$1" >/dev/null 2>&1
}

require_command() {
    has_command "$1" || die "required command not found: $1"
}

# verify_nvidia_driver_prerequisite verifies the host display driver can report GPU inventory.
verify_nvidia_driver_prerequisite() {
    local output

    if ! has_command nvidia-smi; then
        die "NVIDIA driver prerequisite is missing: nvidia-smi was not found"
    fi

    if ! output="$(nvidia-smi --query-gpu=name,uuid,driver_version --format=csv,noheader 2>&1)"; then
        die "$(printf 'nvidia-smi GPU inventory check failed:\n%s' "${output}")"
    fi
    if printf '%s\n' "${output}" | grep -Eqi 'No devices were found|No devices found'; then
        die "$(printf 'nvidia-smi GPU inventory check failed:\n%s' "${output}")"
    fi
    if [[ -z "$(printf '%s\n' "${output}" | sed -n '/[^[:space:]]/{p;q;}')" ]]; then
        die "nvidia-smi GPU inventory check returned no GPUs"
    fi
}

duration_to_seconds() {
    local value="${1:-300s}"
    local rest total number unit

    if [[ "${value}" =~ ^[0-9]+$ ]]; then
        printf '%s\n' "${value}"
        return
    fi

    rest="${value}"
    total=0
    while [[ -n "${rest}" ]]; do
        if [[ ! "${rest}" =~ ^([0-9]+)(h|m|s)(.*)$ ]]; then
            log_warn "could not parse timeout ${value}; using 300s"
            printf '300\n'
            return
        fi
        number="${BASH_REMATCH[1]}"
        unit="${BASH_REMATCH[2]}"
        rest="${BASH_REMATCH[3]}"
        case "${unit}" in
            h) total=$((total + number * 3600)) ;;
            m) total=$((total + number * 60)) ;;
            s) total=$((total + number)) ;;
        esac
    done

    printf '%s\n' "${total}"
}
