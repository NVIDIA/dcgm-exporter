#!/usr/bin/env bash
set -euo pipefail

# Wrap CI commands that can fail transiently, such as registry, network, or
# package fetches, with bounded retries and predictable backoff.
ARG0="$(basename "$0")"
ATTEMPTS="${CI_RETRY_ATTEMPTS:-3}"
DELAY="${CI_RETRY_DELAY_SECONDS:-10}"
COMMAND=()

# usage_info prints the short command summary.
usage_info() {
    printf 'usage: %s [--attempts N] [--delay SECONDS] -- command [args...]\n' "${ARG0}"
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

Run a command with bounded linear-backoff retries.

Options:
  --attempts N      maximum number of attempts (env: CI_RETRY_ATTEMPTS, default: ${ATTEMPTS})
  --delay SECONDS   initial retry delay (env: CI_RETRY_DELAY_SECONDS, default: ${DELAY})
  -h, --help        show this help message
EOF
}

# need_value fails when an option that requires a value is missing one.
need_value() {
    [[ -n "${2:-}" ]] || usage
}

# flags parses retry options and stores the command to execute.
flags() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --attempts)
                need_value "$1" "${2:-}"
                ATTEMPTS="$2"
                shift 2
                ;;
            --delay)
                need_value "$1" "${2:-}"
                DELAY="$2"
                shift 2
                ;;
            -h|--help)
                help
                exit 0
                ;;
            --)
                shift
                break
                ;;
            -*)
                usage
                ;;
            *)
                break
                ;;
        esac
    done

    [[ $# -gt 0 ]] || usage
    COMMAND=("$@")
}

# validate_options verifies retry settings before executing the command.
validate_options() {
    if ! [[ "${ATTEMPTS}" =~ ^[1-9][0-9]*$ ]]; then
        echo "attempts must be a positive integer" >&2
        exit 2
    fi

    if ! [[ "${DELAY}" =~ ^[0-9]+$ ]]; then
        echo "delay must be a non-negative integer" >&2
        exit 2
    fi
    DELAY=$((10#${DELAY}))
}

# format_command prints a shell-escaped command for retry diagnostics.
format_command() {
    local formatted
    printf -v formatted '%q ' "$@"
    printf '%s' "${formatted% }"
}

# run_with_retry executes COMMAND until it succeeds or exhausts ATTEMPTS.
run_with_retry() {
    local attempt=1
    local command_display
    local sleep_for
    local status

    command_display="$(format_command "${COMMAND[@]}")"
    while true; do
        if "${COMMAND[@]}"; then
            return 0
        else
            status=$?
        fi

        if (( attempt >= ATTEMPTS )); then
            echo "Command failed after ${ATTEMPTS} attempts: ${command_display}" >&2
            return "${status}"
        fi

        sleep_for=$((DELAY * attempt))
        echo "Command failed with exit ${status}; retrying in ${sleep_for}s (${attempt}/${ATTEMPTS}): ${command_display}" >&2
        sleep "${sleep_for}"
        attempt=$((attempt + 1))
    done
}
# main runs the requested command with the validated retry configuration.
main() {
    run_with_retry
}

flags "$@"
validate_options
main
