#!/usr/bin/env bash
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.

set -euo pipefail

file_list=""
files=()

# cleanup removes the temporary Git file list, including after failures.
cleanup() {
    rm -f -- "${file_list}"
}

# collect_files selects maintained Go source and includes new files in format mode.
collect_files() {
    local mode="$1" path generated
    local -a list_args=(--cached)

    if [[ "${mode}" == "format" ]]; then
        list_args+=(--others --exclude-standard)
    fi

    # Keep Git's failure status and NUL-delimited paths without a pipeline or subshell.
    file_list="$(mktemp "${TMPDIR:-/tmp}/dcgm-exporter-format.XXXXXX")"
    trap cleanup EXIT
    git ls-files "${list_args[@]}" -z -- '*.go' >"${file_list}"

    while IFS= read -r -d '' path; do
        case "${path}" in
            vendor/* | */vendor/* | third_party/*) continue ;;
            *) ;;
        esac
        [[ -f "${path}" ]] || continue

        # Only the standard marker before the package clause identifies generated Go.
        generated="$(awk '
            { sub(/\r$/, "") }
            /^\/\/ Code generated .* DO NOT EDIT\.$/ { generated = 1; exit }
            /^[[:space:]]*package[[:space:]]/ { exit }
            END { print generated + 0 }
        ' "./${path}")"
        [[ "${generated}" == 1 ]] && continue

        files+=("./${path}")
    done <"${file_list}"
}

# run_formatters runs both tools and retains the largest failure status.
run_formatters() {
    local mode="$1" tool output tool_status status=0
    local -a args formatter_flags=(-l)

    if [[ "${mode}" == "format" ]]; then
        formatter_flags=(-w)
    fi
    for tool in gofumpt goimports; do
        if ! command -v "${tool}" >/dev/null 2>&1; then
            echo "error: required formatter not found: ${tool}" >&2
            return 127
        fi
    done

    for tool in gofumpt goimports; do
        args=("${formatter_flags[@]}")
        if [[ "${tool}" == "goimports" ]]; then
            args+=(-local github.com/NVIDIA/dcgm-exporter)
        fi
        if output="$("${tool}" "${args[@]}" "${files[@]}")"; then
            tool_status=0
            if [[ "${mode}" == "check" && -n "${output}" ]]; then
                echo "error: ${tool} found files needing formatting:" >&2
                tool_status=1
            fi
        else
            tool_status="$?"
            echo "error: ${tool} failed with status ${tool_status}" >&2
        fi
        if [[ -n "${output}" ]]; then
            printf '%s\n' "${output}" >&2
        fi
        if (( tool_status > status )); then
            status="${tool_status}"
        fi
    done

    if [[ "${status}" -eq 0 ]]; then
        if [[ "${mode}" == format ]]; then
            printf 'Formatted %s Go source files.\n' "${#files[@]}"
        else
            printf 'Go formatting check passed (%s Go source files).\n' "${#files[@]}"
        fi
    fi
    return "${status}"
}

# main validates the mode, selects source files, and runs the formatters.
main() {
    local mode root_dir

    if [[ "$#" -ne 1 || ("${1:-}" != "format" && "${1:-}" != "check") ]]; then
        echo "Usage: $0 format|check" >&2
        return 2
    fi
    mode="$1"
    root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    cd "${root_dir}"

    collect_files "${mode}"
    if [[ "${#files[@]}" -eq 0 ]]; then
        echo "No Go source files to ${mode}."
        return 0
    fi
    run_formatters "${mode}"
}

main "$@"
