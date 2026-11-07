#!/usr/bin/env bash
set -euo pipefail

PROBE_DIR=""
HOSTENGINE_PID=""
HOSTENGINE_LOG=""
HOSTENGINE_STDOUT=""
HOSTENGINE_STDERR=""
PROBE_STDOUT=""
PROBE_STDERR=""

# cleanup stops the temporary hostengine and removes its probe workspace.
cleanup() {
    if [[ -n "${HOSTENGINE_PID}" ]]; then
        kill "${HOSTENGINE_PID}" >/dev/null 2>&1 || true
    fi
    if [[ -n "${PROBE_DIR}" ]]; then
        rm -rf "${PROBE_DIR}"
    fi
}

# initialize_probe_workspace creates files used to capture probe diagnostics.
initialize_probe_workspace() {
    PROBE_DIR="$(mktemp -d /tmp/dcgm-exporter-dcgmi-probe.XXXXXX)"
    HOSTENGINE_LOG="${PROBE_DIR}/nv-hostengine.log"
    HOSTENGINE_STDOUT="${PROBE_DIR}/nv-hostengine.stdout"
    HOSTENGINE_STDERR="${PROBE_DIR}/nv-hostengine.stderr"
    PROBE_STDOUT="${PROBE_DIR}/dcgmi.stdout"
    PROBE_STDERR="${PROBE_DIR}/dcgmi.stderr"
}

# start_hostengine runs a local hostengine for the duration of the probe.
start_hostengine() {
    /usr/bin/nv-hostengine -n -b ALL -p 5555 -f "${HOSTENGINE_LOG}" --log-level ERROR >"${HOSTENGINE_STDOUT}" 2>"${HOSTENGINE_STDERR}" &
    HOSTENGINE_PID=$!
}

# run_probe retries hostengine readiness until its retry deadline. Each attempt
# has its own timeout, so an attempt started near that deadline may finish later.
run_probe() {
    local deadline=$((SECONDS + 45))

    while (( SECONDS < deadline )); do
        if timeout --kill-after=2s 8s /usr/bin/dcgmi "$@" --host localhost:5555 >"${PROBE_STDOUT}" 2>"${PROBE_STDERR}"; then
            cat "${PROBE_STDOUT}"
            return 0
        fi
        sleep 1
    done

    printf 'dcgmi probe timed out or failed within 45s: %s\n' "$*" >&2
    cat "${PROBE_STDOUT}"
    cat "${PROBE_STDERR}" >&2
    cat "${HOSTENGINE_STDERR}" >&2
    return 1
}

# main starts a local hostengine and passes every argument through to dcgmi.
main() {
    initialize_probe_workspace
    trap cleanup EXIT
    start_hostengine
    run_probe "$@"
}

main "$@"
