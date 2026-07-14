#!/usr/bin/env bash
set -euo pipefail

# Wrap CI commands that can fail transiently, such as registry, network, or
# package fetches, with bounded retries and predictable backoff.
attempts="${CI_RETRY_ATTEMPTS:-3}"
delay="${CI_RETRY_DELAY_SECONDS:-10}"

usage() {
    echo "usage: $0 [--attempts N] [--delay SECONDS] -- command [args...]" >&2
}

format_command() {
    local formatted
    printf -v formatted '%q ' "$@"
    printf '%s' "${formatted% }"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --attempts)
            if [[ $# -lt 2 ]]; then
                usage
                exit 2
            fi
            attempts="$2"
            shift 2
            ;;
        --delay)
            if [[ $# -lt 2 ]]; then
                usage
                exit 2
            fi
            delay="$2"
            shift 2
            ;;
        --)
            shift
            break
            ;;
        -*)
            usage
            exit 2
            ;;
        *)
            break
            ;;
    esac
done

if [[ $# -eq 0 ]]; then
    usage
    exit 2
fi

if ! [[ "$attempts" =~ ^[1-9][0-9]*$ ]]; then
    echo "attempts must be a positive integer" >&2
    exit 2
fi

if ! [[ "$delay" =~ ^[0-9]+$ ]]; then
    echo "delay must be a non-negative integer" >&2
    exit 2
fi
delay=$((10#$delay))

command_display=$(format_command "$@")

attempt=1
while true; do
    if "$@"; then
        exit 0
    else
        status=$?
    fi

    if (( attempt >= attempts )); then
        echo "Command failed after ${attempts} attempts: ${command_display}" >&2
        exit "$status"
    fi

    sleep_for=$(( delay * attempt ))
    echo "Command failed with exit ${status}; retrying in ${sleep_for}s (${attempt}/${attempts}): ${command_display}" >&2
    sleep "$sleep_for"
    attempt=$(( attempt + 1 ))
done
