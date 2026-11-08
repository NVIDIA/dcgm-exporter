#!/usr/bin/env bash
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.

# Install pinned Mise and Task. Task installs the remaining tools.
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${root_dir}/hack/mise-bootstrap.env"

# Describes the bootstrap inputs without installing or changing developer tools.
usage() {
    cat <<'USAGE'
Usage: ./bootstrap.sh [--bin-dir DIRECTORY]

Installs the pinned Mise release and Task command. Run `task deps:install` to
install the remaining repository tools.

Options:
  --bin-dir DIRECTORY  Directory that receives the task command.
  -h, --help           Show this help.
USAGE
}

# Verifies downloaded bytes from a checksum line using the available platform checksum command.
sha256_check() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum -c -
    else
        shasum -a 256 -c -
    fi
}

# Selects the pinned Mise archive and checksum for the host OS, architecture, and libc.
select_platform() {
    case "$(uname -s):$(uname -m)" in
        Linux:x86_64)
            mise_arch="linux-x64"
            mise_sha256="${MISE_TARBALL_SHA256_LINUX_X64}"
            if compgen -G '/lib/ld-musl-*.so.1' >/dev/null; then
                mise_arch+="-musl"
                mise_sha256="${MISE_TARBALL_SHA256_LINUX_X64_MUSL}"
            fi
            ;;
        Linux:aarch64 | Linux:arm64)
            mise_arch="linux-arm64"
            mise_sha256="${MISE_TARBALL_SHA256_LINUX_ARM64}"
            if compgen -G '/lib/ld-musl-*.so.1' >/dev/null; then
                mise_arch+="-musl"
                mise_sha256="${MISE_TARBALL_SHA256_LINUX_ARM64_MUSL}"
            fi
            ;;
        Darwin:x86_64)
            mise_arch="macos-x64"
            mise_sha256="${MISE_TARBALL_SHA256_MACOS_X64}"
            ;;
        Darwin:arm64)
            mise_arch="macos-arm64"
            mise_sha256="${MISE_TARBALL_SHA256_MACOS_ARM64}"
            ;;
        *)
            echo "error: unsupported platform: $(uname -s)/$(uname -m)" >&2
            exit 1
            ;;
    esac
}

# Installs the requested Mise version only after checksum verification; its subshell cleans download files.
install_mise() (
    local mise_bin="$1" mise_data_dir="$2" install_arch="$3" install_sha256="$4"
    local expected_version="${MISE_VERSION#v}"
    if [[ -x "${mise_bin}" ]] && [[ "$("${mise_bin}" --version | awk '{print $1}')" == "${expected_version}" ]]; then
        return
    fi

    local temp_dir archive release_base_url
    temp_dir="$(mktemp -d)"
    trap 'rm -rf "${temp_dir}"' EXIT
    archive="${temp_dir}/mise.tar.gz"
    release_base_url="${MISE_RELEASE_BASE_URL:-https://github.com/jdx/mise/releases/download}"

    curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
        "${release_base_url}/${MISE_VERSION}/mise-${MISE_VERSION}-${install_arch}.tar.gz" \
        --output "${archive}"
    printf '%s  %s\n' "${install_sha256}" "${archive}" | sha256_check
    tar -xzf "${archive}" -C "${temp_dir}"
    mkdir -p "${mise_data_dir}/bin"
    install -m 0755 "${temp_dir}/mise/bin/mise" "${mise_bin}"
)

# Atomically installs the repository Task wrapper and refuses to overwrite an unrelated command.
install_task_wrapper() {
    local bin_dir="$1" mise_data_dir="$2" mise_bin="$3"
    local task_bin="${bin_dir}/task" task_temp
    if [[ -e "${task_bin}" || -L "${task_bin}" ]]; then
        if [[ ! -f "${task_bin}" ]] || ! grep -Fqx '# dcgm-exporter Task wrapper' "${task_bin}"; then
            echo "error: refusing to replace existing ${task_bin}" >&2
            echo "Choose another directory with --bin-dir, or remove that command yourself." >&2
            exit 1
        fi
    fi

    mkdir -p "${bin_dir}"
    task_temp="$(mktemp "${bin_dir}/.task.XXXXXX")"
    {
        printf '%s\n' '#!/usr/bin/env bash' '# dcgm-exporter Task wrapper' 'set -euo pipefail'
        printf 'export MISE_DATA_DIR=%q\n' "${mise_data_dir}"
        # shellcheck disable=SC2016 # The wrapper expands PATH when it runs.
        printf '%s\n' 'export PATH="${MISE_DATA_DIR}/bin:${MISE_DATA_DIR}/shims:${PATH}"'
        printf 'exec %q --cd %q exec task -- task "$@"\n' "${mise_bin}" "${root_dir}"
    } >"${task_temp}"
    chmod 0755 "${task_temp}"
    mv -f "${task_temp}" "${task_bin}"
}

# Parses the installation destination, checks prerequisites, and installs the pinned Mise and Task commands.
main() {
    local bin_dir="${XDG_BIN_HOME:-${HOME}/.local/bin}"
    while (($# > 0)); do
        case "$1" in
            --bin-dir)
                (($# >= 2)) || {
                    echo "error: --bin-dir needs a directory" >&2
                    exit 2
                }
                bin_dir="$2"
                shift 2
                ;;
            -h | --help)
                usage
                return
                ;;
            *)
                echo "error: unknown option: $1" >&2
                usage >&2
                exit 2
                ;;
        esac
    done

    local required_command
    for required_command in curl tar mktemp; do
        command -v "${required_command}" >/dev/null 2>&1 || {
            echo "error: bootstrap needs ${required_command} on PATH" >&2
            exit 1
        }
    done
    if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
        echo "error: bootstrap needs sha256sum or shasum on PATH" >&2
        exit 1
    fi

    local mise_arch mise_sha256 mise_data_dir mise_bin task_bin
    select_platform
    mise_data_dir="${MISE_DATA_DIR:-${XDG_DATA_HOME:-${HOME}/.local/share}/mise}"
    mise_bin="${mise_data_dir}/bin/mise"
    install_mise "${mise_bin}" "${mise_data_dir}" "${mise_arch}" "${mise_sha256}"
    "${mise_bin}" --cd "${root_dir}" trust "${root_dir}/mise.toml"
    "${mise_bin}" --cd "${root_dir}" install task --locked
    "${mise_bin}" --cd "${root_dir}" reshim

    install_task_wrapper "${bin_dir}" "${mise_data_dir}" "${mise_bin}"
    task_bin="${bin_dir}/task"
    "${task_bin}" --version
    if [[ ":${PATH}:" != *":${bin_dir}:"* ]]; then
        echo "Task is installed at ${task_bin}. Add ${bin_dir} to PATH, then open a new shell."
    else
        echo "Task is ready. Run: task --list"
    fi
}

main "$@"
