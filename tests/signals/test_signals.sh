#!/usr/bin/env bash
#
# Black-box behavioural test for dcgm-exporter signal handling.
#
# Proves end-to-end (no Go test harness — just the real binary, real
# signals, real HTTP scrapes) that:
#
#   1. SIGTERM            → daemon shuts down cleanly within budget.
#   2. SIGINT             → daemon shuts down cleanly within budget.
#   3. SIGHUP             → daemon performs hot reload, stays running,
#                           /metrics keeps responding.
#   4. Multiple SIGHUPs   → all reloads complete, daemon survives.
#
# Requirements:
#   - libdcgm.so.4 and libnvidia-ml.so.1 discoverable by the dynamic
#     linker (NVIDIA DCGM installed on the host, or run inside an
#     nvidia-container-runtime container).
#   - curl on PATH.
#   - The dcgm-exporter binary present at $BIN (defaults to
#     cmd/dcgm-exporter/dcgm-exporter; `make binary` produces it).
#
# Usage:
#   make test-signals
#     (builds the binary, then runs this script)
#
#   ./tests/signals/test_signals.sh
#     (assumes the binary is already built)
#
# Environment overrides:
#   BIN                       path to the dcgm-exporter binary
#   PORT                      HTTP port (default 9499 — off the canonical 9400)
#   COUNTERS                  path to counters CSV
#   SHUTDOWN_BUDGET_SECONDS   max time allowed between signal and exit (default 5)
#   STARTUP_TIMEOUT_SECONDS   max time waiting for /metrics to respond (default 30)

set -uo pipefail

#─────────────────────────────────────────────────────────────────────────
# Config
#─────────────────────────────────────────────────────────────────────────

BIN=${BIN:-cmd/dcgm-exporter/dcgm-exporter}
PORT=${PORT:-9499}
COUNTERS=${COUNTERS:-etc/default-counters.csv}
SHUTDOWN_BUDGET_SECONDS=${SHUTDOWN_BUDGET_SECONDS:-5}
STARTUP_TIMEOUT_SECONDS=${STARTUP_TIMEOUT_SECONDS:-30}

# METRICS_URL is assigned after the port-availability check below; using
# it before that would risk pointing at a stale port.
METRICS_URL=""

#─────────────────────────────────────────────────────────────────────────
# Output helpers
#─────────────────────────────────────────────────────────────────────────

if [ -t 1 ]; then
    RED=$'\033[0;31m'
    GREEN=$'\033[0;32m'
    YELLOW=$'\033[0;33m'
    NC=$'\033[0m'
else
    RED='' GREEN='' YELLOW='' NC=''
fi

pass() { printf '%sPASS%s: %s\n' "$GREEN" "$NC" "$*"; }
fail() { printf '%sFAIL%s: %s\n' "$RED" "$NC" "$*" >&2; exit 1; }
info() { printf '%s---%s %s\n' "$YELLOW" "$NC" "$*"; }

#─────────────────────────────────────────────────────────────────────────
# Daemon lifecycle
#─────────────────────────────────────────────────────────────────────────

DAEMON_PID=""
DAEMON_LOG=""

# wait_for_metrics PID
#   Polls /metrics until it responds or the daemon dies.
wait_for_metrics() {
    local pid=$1
    local start=$SECONDS
    while true; do
        if ! kill -0 "$pid" 2>/dev/null; then
            local exit_code
            wait "$pid" 2>/dev/null
            exit_code=$?
            echo
            echo "===== daemon log ====="
            cat "$DAEMON_LOG"
            echo "===== end log ====="
            fail "daemon (pid=$pid) died before /metrics responded (exit=$exit_code)"
        fi
        if curl -fsSL --max-time 2 "$METRICS_URL" >/dev/null 2>&1; then
            return 0
        fi
        if (( SECONDS - start > STARTUP_TIMEOUT_SECONDS )); then
            fail "/metrics did not respond within ${STARTUP_TIMEOUT_SECONDS}s"
        fi
        sleep 0.5
    done
}

start_daemon() {
    DAEMON_LOG=$(mktemp -t dcgm-exporter-signal-test.XXXXXX.log)
    info "Starting dcgm-exporter on :$PORT (log: $DAEMON_LOG)"
    "$BIN" --collectors "$COUNTERS" --address ":$PORT" \
        >"$DAEMON_LOG" 2>&1 &
    DAEMON_PID=$!
    info "Daemon pid=$DAEMON_PID"
    wait_for_metrics "$DAEMON_PID"
    info "Daemon is serving /metrics"
}

# stop_daemon_with_signal SIG
#   Sends SIG to the daemon and asserts it exits within budget.
stop_daemon_with_signal() {
    local sig=$1
    local start=$SECONDS
    info "Sending SIG${sig} to pid=${DAEMON_PID}"
    kill -s "$sig" "$DAEMON_PID"
    while kill -0 "$DAEMON_PID" 2>/dev/null; do
        local elapsed=$(( SECONDS - start ))
        if (( elapsed > SHUTDOWN_BUDGET_SECONDS )); then
            kill -KILL "$DAEMON_PID" 2>/dev/null || true
            echo
            echo "===== daemon log ====="
            cat "$DAEMON_LOG"
            echo "===== end log ====="
            fail "daemon did not exit within ${SHUTDOWN_BUDGET_SECONDS}s after SIG${sig}"
        fi
        sleep 0.1
    done
    wait "$DAEMON_PID" 2>/dev/null || true
    local elapsed=$(( SECONDS - start ))
    pass "daemon exited within ${elapsed}s after SIG${sig}"
    DAEMON_PID=""
    rm -f "$DAEMON_LOG"
}

cleanup() {
    if [ -n "${DAEMON_PID:-}" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
        info "Cleaning up daemon pid=$DAEMON_PID"
        kill -KILL "$DAEMON_PID" 2>/dev/null || true
    fi
    if [ -n "${DAEMON_LOG:-}" ] && [ -f "$DAEMON_LOG" ]; then
        rm -f "$DAEMON_LOG"
    fi
}
trap cleanup EXIT INT TERM

#─────────────────────────────────────────────────────────────────────────
# Pre-flight checks
#─────────────────────────────────────────────────────────────────────────

if [ ! -x "$BIN" ]; then
    fail "binary not found or not executable: $BIN (run 'make binary' first)"
fi
if ! command -v curl >/dev/null 2>&1; then
    fail "curl not on PATH (required for /metrics scrape)"
fi
if [ ! -f "$COUNTERS" ]; then
    fail "counters file not found: $COUNTERS"
fi

# port_in_use PORT
#   Returns 0 if something is listening on PORT, non-zero otherwise.
#   Uses bash's /dev/tcp pseudo-device (no external deps).
port_in_use() {
    local p=$1
    (echo > "/dev/tcp/127.0.0.1/$p") 2>/dev/null
}

# Bump PORT until we find a free one. Caps at +20 to avoid wandering
# off if the user's machine has a dense local-port population (e.g.
# kubernetes node-ports). The default 9499 is one off from the
# canonical 9400 so a running dcgm-exporter service doesn't clash.
original_port=$PORT
max_attempts=20
attempt=0
while port_in_use "$PORT"; do
    info "port $PORT already in use; trying $((PORT + 1))"
    PORT=$((PORT + 1))
    attempt=$((attempt + 1))
    if [ "$attempt" -ge "$max_attempts" ]; then
        fail "no free port found in range ${original_port}-${PORT}; set PORT=NNNN to pick one explicitly"
    fi
done
if [ "$PORT" -ne "$original_port" ]; then
    info "Using port $PORT (default $original_port was in use)"
fi
METRICS_URL="http://localhost:${PORT}/metrics"
readonly METRICS_URL

# Runtime-library detection. The daemon dlopens libdcgm.so.4 and
# libnvidia-ml.so.1 at startup. Behaviour per environment:
#
#   * Standard Linux (Ubuntu/RHEL/etc.): rely on ldconfig + standard
#     loader paths. Warn (not fail) if missing.
#   * NixOS: ldconfig doesn't see /nix/store libs. Auto-populate
#     LD_LIBRARY_PATH from /run/opengl-driver/lib (NVIDIA driver) and
#     the first libdcgm.so.4 found in /nix/store. Caller-set
#     LD_LIBRARY_PATH wins.
nixos=0
if [ -e /etc/NIXOS ] || [ -d /nix/store ]; then
    nixos=1
fi

if [ "$nixos" -eq 1 ]; then
    # Check whether the required libs are already reachable via the
    # caller's LD_LIBRARY_PATH (e.g. inside a nix-shell that already
    # has DCGM). If not, prepend the standard NixOS locations.
    needs_dcgm=1
    needs_nvml=1
    if [ -n "${LD_LIBRARY_PATH:-}" ]; then
        IFS=':' read -r -a ld_dirs <<< "$LD_LIBRARY_PATH"
        for d in "${ld_dirs[@]}"; do
            [ -e "$d/libdcgm.so.4" ] && needs_dcgm=0
            [ -e "$d/libnvidia-ml.so.1" ] && needs_nvml=0
        done
    fi

    if [ "$needs_dcgm" -eq 1 ] || [ "$needs_nvml" -eq 1 ]; then
        info "NixOS detected; auto-configuring LD_LIBRARY_PATH for DCGM/NVML"
        new_paths=()
        if [ "$needs_nvml" -eq 1 ] && [ -d /run/opengl-driver/lib ]; then
            new_paths+=(/run/opengl-driver/lib)
        fi
        if [ "$needs_dcgm" -eq 1 ]; then
            dcgm_lib=$(find /nix/store -maxdepth 4 -name 'libdcgm.so.4' -printf '%h\n' 2>/dev/null | head -1)
            if [ -n "$dcgm_lib" ]; then
                new_paths+=("$dcgm_lib")
            fi
        fi
        if [ "${#new_paths[@]}" -gt 0 ]; then
            prefix=$(IFS=:; echo "${new_paths[*]}")
            if [ -n "${LD_LIBRARY_PATH:-}" ]; then
                LD_LIBRARY_PATH="$prefix:$LD_LIBRARY_PATH"
            else
                LD_LIBRARY_PATH="$prefix"
            fi
            export LD_LIBRARY_PATH
            info "LD_LIBRARY_PATH=$LD_LIBRARY_PATH"
        fi
    fi
fi

if command -v ldconfig >/dev/null 2>&1 && [ "$nixos" -eq 0 ]; then
    missing_libs=()
    for lib in libdcgm.so.4 libnvidia-ml.so.1; do
        if ! ldconfig -p 2>/dev/null | grep -q -- "$lib"; then
            missing_libs+=("$lib")
        fi
    done
    if [ "${#missing_libs[@]}" -gt 0 ]; then
        printf '%sWARN%s: ldconfig does not list: %s\n' \
            "$YELLOW" "$NC" "${missing_libs[*]}" >&2
        echo "       If the daemon fails to start below, install NVIDIA DCGM and the driver." >&2
        echo "       Proceeding anyway — daemon startup will surface the real error if libs are unreachable." >&2
        echo >&2
    fi
fi

info "Binary:   $BIN"
info "Port:     $PORT"
info "Budget:   ${SHUTDOWN_BUDGET_SECONDS}s shutdown, ${STARTUP_TIMEOUT_SECONDS}s startup"

#─────────────────────────────────────────────────────────────────────────
# Test 1 — SIGTERM
#─────────────────────────────────────────────────────────────────────────

info "=== Test 1: SIGTERM → clean shutdown ==="
start_daemon
stop_daemon_with_signal TERM

#─────────────────────────────────────────────────────────────────────────
# Test 2 — SIGINT
#─────────────────────────────────────────────────────────────────────────

info "=== Test 2: SIGINT → clean shutdown ==="
start_daemon
stop_daemon_with_signal INT

#─────────────────────────────────────────────────────────────────────────
# Test 3 — SIGHUP triggers reload, daemon stays up
#─────────────────────────────────────────────────────────────────────────

info "=== Test 3: SIGHUP → hot reload, daemon stays up ==="
start_daemon
info "Sending SIGHUP to pid=${DAEMON_PID}"
kill -s HUP "$DAEMON_PID"
# Give the reload a moment to complete.
sleep 2
if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
    fail "daemon died after SIGHUP (should hot-reload, not exit)"
fi
if ! curl -fsSL --max-time 5 "$METRICS_URL" >/dev/null 2>&1; then
    fail "/metrics not responding after SIGHUP"
fi
pass "daemon survived SIGHUP and /metrics still responds"
stop_daemon_with_signal TERM

#─────────────────────────────────────────────────────────────────────────
# Test 4 — Multiple SIGHUPs back-to-back
#─────────────────────────────────────────────────────────────────────────

info "=== Test 4: 3 × SIGHUP → all reloads complete ==="
start_daemon
for i in 1 2 3; do
    info "SIGHUP #$i"
    kill -s HUP "$DAEMON_PID"
    sleep 1
    if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
        fail "daemon died after SIGHUP #$i"
    fi
done
if ! curl -fsSL --max-time 5 "$METRICS_URL" >/dev/null 2>&1; then
    fail "/metrics not responding after 3 SIGHUPs"
fi
pass "daemon survived 3 SIGHUPs and /metrics still responds"
stop_daemon_with_signal TERM

#─────────────────────────────────────────────────────────────────────────
# Summary
#─────────────────────────────────────────────────────────────────────────

echo
info "All signal tests passed."
