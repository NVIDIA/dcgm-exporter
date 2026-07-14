#!/usr/bin/env bash
set -euo pipefail

tmpdir=$(mktemp -d /tmp/dcgm-exporter-dcgmi-probe.XXXXXX)
hostengine_log="${tmpdir}/nv-hostengine.log"
hostengine_stdout="${tmpdir}/nv-hostengine.stdout"
hostengine_stderr="${tmpdir}/nv-hostengine.stderr"
probe_stdout="${tmpdir}/dcgmi.stdout"
probe_stderr="${tmpdir}/dcgmi.stderr"

/usr/bin/nv-hostengine -n -b ALL -p 5555 -f "${hostengine_log}" --log-level ERROR >"${hostengine_stdout}" 2>"${hostengine_stderr}" &
hostengine_pid=$!
trap 'kill "${hostengine_pid}" >/dev/null 2>&1 || true; rm -rf "${tmpdir}"' EXIT

deadline=$((SECONDS + 45))
while (( SECONDS < deadline )); do
    if timeout --kill-after=2s 8s /usr/bin/dcgmi "$@" --host localhost:5555 >"${probe_stdout}" 2>"${probe_stderr}"; then
        cat "${probe_stdout}"
        exit 0
    fi
    sleep 1
done

printf "dcgmi probe timed out or failed within 45s: %s\n" "$*" >&2
cat "${probe_stdout}"
cat "${probe_stderr}" >&2
cat "${hostengine_stderr}" >&2
exit 1
