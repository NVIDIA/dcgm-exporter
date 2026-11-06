#!/usr/bin/env bash
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.

# Run the host e2e scenarios that need no GPU, from the e2e bundle in
# E2E_BUNDLE_ROOT: the config commands and config-file startup errors, then
# exporter parity under each NVML injection fixture in NVML_INJECTION_DIR, the
# config-file scenarios under H200.yaml, the MIG XID counter check under
# H200-With-MIG.yaml, and parity under each NVSDM mock in NVSDM_MOCK_DIR. The
# fixtures must match DCGM_VERSION. The MIG check's output is kept in
# dist/mig-xid-counters-evidence.log. Run it through task test:e2e:mocks,
# which sets the three folders.
#
# It needs a DCGM development build with NVML injection (nv-hostengine and
# dcgmi on PATH) and must run as root.

set -euo pipefail

BUNDLE=""
INJECTION_DIR=""
NVSDM_DIR=""

# fail prints an error and stops the script.
fail() {
    echo "ERROR: $*" >&2
    exit 1
}

# run_host_scenarios runs host suite scenarios with the bundle's exporter and
# no bootstrap; the arguments select the scenarios and any fixture.
run_host_scenarios() {
    "${BUNDLE}/bin/e2e" tests \
        --prebuilt-binaries \
        --tools-dir "${BUNDLE}/bin" \
        --exporter-binary "${BUNDLE}/bin/dcgm-exporter" \
        --suite host \
        --dcgm-version "${DCGM_VERSION}" \
        --bootstrap=skip \
        "$@"
}

# list_fixtures prints the YAML fixtures under folder $1, NUL-separated and
# sorted.
list_fixtures() {
    find "$1" -type f -name '*.yaml' -print0 | sort -z
}

# find_one_fixture prints the path of the one NVML injection fixture named
# $1; it fails when the folder has none or more than one.
find_one_fixture() {
    local name="$1"
    local matches

    mapfile -d '' matches < <(find "${INJECTION_DIR}" -type f -name "${name}" -print0)
    if (( ${#matches[@]} != 1 )); then
        echo "ERROR: expected exactly one ${name} in ${INJECTION_DIR}, found ${#matches[@]}" >&2
        return 1
    fi
    printf '%s\n' "${matches[0]}"
}

# run_mig_check runs the MIG XID counter check under H200-With-MIG.yaml and
# prints the probe, fixture and DCGM it used.
run_mig_check() {
    local mig_fixture

    mig_fixture="$(find_one_fixture H200-With-MIG.yaml)"
    echo "MIG XID counter regression: DCGM ${DCGM_VERSION} fixtures, source:"
    git rev-parse HEAD
    sha256sum "${BUNDLE}/bin/e2e-dcgm-probe" "${mig_fixture}"
    go version -m "${BUNDLE}/bin/e2e-dcgm-probe"
    dcgmi --version
    run_host_scenarios \
        --scenario host/migXidCounters \
        --result-markers \
        --dcgm-nvml-injection-yaml "${mig_fixture}"
}

# main runs the checks that need no fixture, then each fixture set.
main() {
    : "${E2E_BUNDLE_ROOT:?E2E_BUNDLE_ROOT is required}"
    : "${NVML_INJECTION_DIR:?NVML_INJECTION_DIR is required}"
    : "${NVSDM_MOCK_DIR:?NVSDM_MOCK_DIR is required}"
    : "${DCGM_VERSION:?DCGM_VERSION is required}"

    BUNDLE="$(cd "${E2E_BUNDLE_ROOT}" && pwd -P)"
    INJECTION_DIR="$(cd "${NVML_INJECTION_DIR}" && pwd -P)"
    NVSDM_DIR="$(cd "${NVSDM_MOCK_DIR}" && pwd -P)"

    # These checks fail before DCGM starts or never start it, so they need
    # neither a GPU nor an NVML injection fixture.
    echo "Validating config commands and YAML config startup errors"
    run_host_scenarios \
        --scenario host/configInspection \
        --scenario host/configStartupErrors

    # Check both fixture sets before any run that uses them.
    local fixtures nvsdm_fixtures fixture config_fixture mig_status
    mapfile -d '' fixtures < <(list_fixtures "${INJECTION_DIR}")
    mapfile -d '' nvsdm_fixtures < <(list_fixtures "${NVSDM_DIR}")
    (( ${#fixtures[@]} > 0 )) || fail "no NVML injection fixtures were found in ${INJECTION_DIR}"
    (( ${#nvsdm_fixtures[@]} > 0 )) || fail "no NVSDM mock fixtures were found in ${NVSDM_DIR}"

    for fixture in "${fixtures[@]}"; do
        echo "Validating direct DCGM/exporter parity for $(basename "${fixture}")"
        run_host_scenarios \
            --scenario host/nvmlInjectionMetrics \
            --dcgm-nvml-injection-yaml "${fixture}"
    done

    # One invocation: scenarios that use the injection file cannot share a
    # run with scenarios that do not.
    config_fixture="$(find_one_fixture H200.yaml)"
    echo "Validating YAML config files under NVML injection"
    run_host_scenarios \
        --scenario host/configServesMetrics \
        --scenario host/configFile \
        --scenario host/configPrecedence \
        --scenario host/watchGroups \
        --scenario host/configEnrichment \
        --scenario host/hpcJobMapping \
        --scenario host/reload \
        --dcgm-nvml-injection-yaml "${config_fixture}"

    # Keep the MIG check's output in dist/.
    mkdir -p dist
    set +e
    (set -e; run_mig_check) 2>&1 | tee dist/mig-xid-counters-evidence.log
    mig_status=("${PIPESTATUS[@]}")
    set -e
    if (( mig_status[0] != 0 || mig_status[1] != 0 )); then
        fail "MIG XID regression or evidence capture failed: ${mig_status[*]}"
    fi

    for fixture in "${nvsdm_fixtures[@]}"; do
        echo "Validating direct DCGM/exporter NVSDM parity for $(basename "${fixture}")"
        run_host_scenarios \
            --scenario host/nvsdmMockMetrics \
            --dcgm-nvsdm-mock-yaml "${fixture}"
    done
}

main "$@"
