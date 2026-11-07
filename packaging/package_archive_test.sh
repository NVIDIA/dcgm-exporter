#!/usr/bin/env bash
# Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# Check the release archive path list, permissions, ownership, and fixed timestamps without extracting it.

set -Eeuo pipefail
export TZ=UTC

archive="${1:?package archive path is required}"
expected_paths="$(cat <<'PATHS'
dcgm_exporter/
dcgm_exporter/LICENSE
dcgm_exporter/THIRD_PARTY_NOTICES
dcgm_exporter/etc/
dcgm_exporter/etc/dcgm-exporter/
dcgm_exporter/etc/dcgm-exporter/1.x-compatibility-metrics.csv
dcgm_exporter/etc/dcgm-exporter/config.yaml
dcgm_exporter/etc/dcgm-exporter/dcp-metrics-included.csv
dcgm_exporter/etc/dcgm-exporter/default-counters.csv
dcgm_exporter/lib/
dcgm_exporter/lib/systemd/
dcgm_exporter/lib/systemd/system/
dcgm_exporter/lib/systemd/system/nvidia-dcgm-exporter.service
dcgm_exporter/usr/
dcgm_exporter/usr/bin/
dcgm_exporter/usr/bin/dcgm-exporter
PATHS
)"

actual_paths="$(tar -tzf "${archive}")"
[[ "${actual_paths}" == "${expected_paths}" ]] || {
    printf 'Unexpected package archive paths:\n%s\n' "${actual_paths}" >&2
    exit 1
}

tar -tvzf "${archive}" | awk '
    $2 != "0/0" { print "non-root ownership: " $0 > "/dev/stderr"; failed = 1 }
    $1 ~ /^d/ && $1 != "drwxr-xr-x" { print "unexpected directory mode: " $0 > "/dev/stderr"; failed = 1 }
    $NF == "dcgm_exporter/usr/bin/dcgm-exporter" && $1 != "-rwxr-xr-x" { print "unexpected exporter mode: " $0 > "/dev/stderr"; failed = 1 }
    $1 ~ /^-/ && $NF != "dcgm_exporter/usr/bin/dcgm-exporter" && $1 != "-rw-r--r--" { print "unexpected data mode: " $0 > "/dev/stderr"; failed = 1 }
    $4 != "2000-01-01" || $5 != "00:00" { print "non-deterministic timestamp: " $0 > "/dev/stderr"; failed = 1 }
    END { exit failed }
'
