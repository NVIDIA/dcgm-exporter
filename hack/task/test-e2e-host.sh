#!/usr/bin/env bash
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.

# Run the host e2e scenarios that a GPU host with DCGM supports, against an
# exporter built with coverage, from the e2e bundle in E2E_BUNDLE_ROOT.
# It writes the coverage data under .coverdata/host/, the e2e output with
# result markers to .coverdata/host-e2e-result-markers.log, and the coverage
# profile to integration_binary.cov, also when a scenario fails. Run it
# through task test:e2e:host, which sets E2E_BUNDLE_ROOT.
#
# Scenarios that need another kind of host are not selected, among them
# host/dcgmUri, host/dcgmUriUnix and host/dcgmUriVSOCK (a separate
# hostengine) and host/profilingReload (profiling). The NVML injection and
# NVSDM mock scenarios run in task test:e2e:mocks.

set -euo pipefail

BUNDLE=""
COVERAGE_DIR="${PWD}/.coverdata/host"
RESULT_MARKER_LOG="${PWD}/.coverdata/host-e2e-result-markers.log"

# fail prints an error and stops the script with exit status $1.
fail() {
    local status="$1"
    shift
    echo "ERROR: $*" >&2
    exit "${status}"
}

# remove_last_results deletes the last run's coverage data, profile and the
# reports made from it, so they cannot mix with this run's.
remove_last_results() {
    rm -rf "${COVERAGE_DIR}" "${RESULT_MARKER_LOG}"
    rm -f integration_binary.cov integration_binary.cov.func.txt \
        integration_metadata.json integration_results.json integration_junit.xml
    mkdir -p "${COVERAGE_DIR}"
}

# show_host prints the host, GPU and DCGM the run uses, and fails when DCGM is
# missing. The config cadence checks start a private nv-hostengine.
show_host() {
    hostname
    head -n 3 /etc/os-release
    nvidia-smi
    ldconfig -p | grep libdcgm.so.4
    command -v nv-hostengine
    go version
}

# build_exporter builds the exporter with coverage, so each exporter exit
# writes GOCOVERDIR data.
build_exporter() {
    echo "Building the exporter with coverage"
    CGO_ENABLED=1 go build \
        -buildvcs=false \
        -cover -covermode=atomic -coverpkg=./cmd/dcgm-exporter,./internal/...,./pkg/... \
        -trimpath \
        -ldflags "-X main.BuildVersion=${EXPORTER_VERSION}" \
        -o "${COVERAGE_DIR}/dcgm-exporter" \
        ./cmd/dcgm-exporter
}

# run_scenarios runs the host scenarios this kind of host supports.
run_scenarios() {
    GOCOVERDIR="${COVERAGE_DIR}" "${BUNDLE}/bin/e2e" tests \
        --prebuilt-binaries \
        --tools-dir "${BUNDLE}/bin" \
        --exporter-binary "${COVERAGE_DIR}/dcgm-exporter" \
        --suite host \
        --scenario host/startupMetrics \
        --scenario host/dcgmLibraryPath \
        --scenario host/configInspection \
        --scenario host/configFile \
        --scenario host/configPrecedence \
        --scenario host/configStartupErrors \
        --scenario host/configEnrichment \
        --scenario host/watchGroups \
        --scenario host/latestValueBatching \
        --scenario host/oversizedFieldGroup \
        --scenario host/runtimeContainerLabels \
        --scenario host/hpcJobMapping \
        --scenario host/startupTLS \
        --scenario host/reload \
        --scenario host/ipv6Listen \
        --scenario host/gpuBindUnbindWatch \
        --scenario host/systemdSocket \
        --result-markers \
        --dcgm-version "${DCGM_VERSION}"
}

# main runs the scenarios and writes the coverage profile, also after a
# failed run. It fails with the e2e status first, then the log or profile
# status.
main() {
    : "${E2E_BUNDLE_ROOT:?E2E_BUNDLE_ROOT is required}"
    : "${DCGM_VERSION:?DCGM_VERSION is required}"
    : "${EXPORTER_VERSION:?EXPORTER_VERSION is required}"

    BUNDLE="$(cd "${E2E_BUNDLE_ROOT}" && pwd -P)"

    # Clean up before the host checks can stop the run.
    remove_last_results
    show_host
    build_exporter

    echo "Running the host e2e scenarios"
    local pipe_status e2e_status tee_status coverage_status=0
    set +e
    run_scenarios 2>&1 | tee "${RESULT_MARKER_LOG}"
    pipe_status=("${PIPESTATUS[@]}")
    set -e
    e2e_status=${pipe_status[0]}
    tee_status=${pipe_status[1]}

    go tool covdata textfmt -i="${COVERAGE_DIR}" -o=integration_binary.cov || coverage_status=$?

    (( e2e_status == 0 )) || fail "${e2e_status}" "host e2e failed with exit code ${e2e_status}"
    (( tee_status == 0 )) || fail "${tee_status}" "writing ${RESULT_MARKER_LOG} failed with exit code ${tee_status}"
    (( coverage_status == 0 )) || fail "${coverage_status}" "writing integration_binary.cov failed with exit code ${coverage_status}"
}

main "$@"
