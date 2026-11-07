#!/usr/bin/env bash
# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Stage every helper-derived file copied into the distroless image and record
# its provenance. The final count check requires a record for every staged
# file or link; the notice generator uses those records for package attribution.
# This script owns the helper-to-runtime filesystem delta.

set -Eeuo pipefail
export LC_ALL=C

runtime_root=""
manifest=""

# flags validates and records the required output paths.
flags() {
    if [[ "$#" -ne 2 ]]; then
        echo "Usage: $0 RUNTIME_ROOT MANIFEST" >&2
        exit 2
    fi

    runtime_root="${1%/}"
    manifest="$2"

    [[ -n "$runtime_root" && "$runtime_root" = /* ]] || {
        echo "RUNTIME_ROOT must be an absolute path" >&2
        exit 2
    }
    [[ -n "$manifest" && "$manifest" = /* ]] || {
        echo "MANIFEST must be an absolute path" >&2
        exit 2
    }
    [[ "$manifest" != "$runtime_root" && "$manifest" != "$runtime_root"/* ]] || {
        echo "MANIFEST must be outside RUNTIME_ROOT" >&2
        exit 2
    }
    [[ ! -e "$runtime_root" ]] || {
        echo "RUNTIME_ROOT already exists: $runtime_root" >&2
        exit 1
    }
}

# stage_file copies one file and records its provenance.
stage_file() {
    local category="$1"
    local source="$2"
    local destination="$3"
    local staged_path="${runtime_root}${destination}"

    [[ "$source" = /* && "$destination" = /* ]] || {
        echo "Staged source and destination must be absolute: $source -> $destination" >&2
        exit 1
    }
    [[ "$source" != *$'\t'* && "$destination" != *$'\t'* ]] || {
        echo "Staged paths must not contain tabs" >&2
        exit 1
    }
    [[ -e "$source" || -L "$source" ]] || {
        echo "Staged source does not exist: $source" >&2
        exit 1
    }
    [[ ! -e "$staged_path" && ! -L "$staged_path" ]] || {
        echo "Duplicate staged destination: $destination" >&2
        exit 1
    }

    mkdir -p "$(dirname "$staged_path")"
    cp --archive --no-preserve=ownership,xattr "$source" "$staged_path"
    chown --no-dereference root:root "$staged_path"
    # Public data is read-only; executable files retain only ordinary execute bits.
    # Do not chmod links: their targets may be outside the staging tree.
    if [[ ! -L "$staged_path" ]]; then
        if [[ -x "$source" ]]; then
            chmod 0755 "$staged_path"
        else
            chmod 0644 "$staged_path"
        fi
    fi
    printf '%s\t%s\t%s\n' "$category" "$source" "$destination" >> "$manifest"
}

# stage_package_libraries selects required package libraries and preserves their paths.
stage_package_libraries() {
    local package_names package_files pattern source
    local matched
    local -a installed_packages=() selected_files=()

    [[ "$#" -gt 0 ]] || {
        echo "No runtime library patterns supplied" >&2
        exit 1
    }

    package_names="$(dpkg-query --show --showformat='${db:Status-Status} ${binary:Package}\n' |
        awk '$1 == "installed" { print $2 }')"
    mapfile -t installed_packages <<< "$package_names"
    package_files="$(dpkg-query --listfiles "${installed_packages[@]}" | sort -u)"

    for pattern in "$@"; do
        matched=false
        while IFS= read -r source; do
            # Selectors are shell patterns, not literal paths.
            # shellcheck disable=SC2254
            case "$source" in
                $pattern)
                    selected_files+=("$source")
                    matched=true
                    ;;
            esac
        done <<< "$package_files"
        [[ "$matched" = true ]] || {
            echo "Runtime library pattern $pattern matched no package files" >&2
            exit 1
        }
    done

    # Select each file once even when required patterns overlap.
    package_files="$(printf '%s\n' "${selected_files[@]}" | sort -u)"
    while IFS= read -r source; do
        stage_file package "$source" "$source"
    done <<< "$package_files"
}

# stage_link creates and records a verified runtime symlink.
stage_link() {
    local category="$1"
    local package_source="$2"
    local destination="$3"
    local target="$4"
    local staged_path="${runtime_root}${destination}"

    [[ -e "$package_source" || -L "$package_source" ]] || {
        echo "Synthetic link package source does not exist: $package_source" >&2
        exit 1
    }
    [[ ! -e "$staged_path" && ! -L "$staged_path" ]] || {
        echo "Duplicate staged destination: $destination" >&2
        exit 1
    }
    mkdir -p "$(dirname "$staged_path")"
    ln -s "$target" "$staged_path"
    chown --no-dereference root:root "$staged_path"
    [[ -e "$staged_path" ]] || {
        echo "Synthetic link target is not staged: $destination -> $target" >&2
        exit 1
    }
    printf '%s\t%s\t%s\n' "$category" "$package_source" "$destination" >> "$manifest"
}

# architecture_library_directory identifies the one package library directory.
# Its optional root argument exists so the fail-closed selection can be tested
# without inspecting or modifying the test host's /usr/lib.
architecture_library_directory() {
    local library_root="${1:-/usr/lib}"
    local -a architecture_directories=()

    mapfile -t architecture_directories < <(
        find "$library_root" -mindepth 1 -maxdepth 1 -type d -name '*-linux-gnu' -printf '%f\n' |
            sort
    )
    if [[ "${#architecture_directories[@]}" -ne 1 ]]; then
        echo "Unable to identify a unique architecture library directory under $library_root" >&2
        return 1
    fi

    printf '%s\n' "${architecture_directories[0]}"
}

# stage_runtime_libraries stages DCGM and supporting package libraries.
stage_runtime_libraries() {
    local architecture_directory="$1"
    local library_destination="/usr/lib/${architecture_directory}"

    # Require Sysmon even when other modules are present.
    stage_package_libraries \
        "$library_destination/libdcgm*" \
        '/*/libdcgmmodule*.so*' \
        '/*/libdcgmmodulesysmon.so*' \
        "$library_destination/libnvperf_dcgm*" \
        "$library_destination/libcap.so*" \
        "$library_destination/libtinfo.so*"
    stage_link \
        package \
        "/usr/lib/${architecture_directory}/libnvperf_dcgm_host.so" \
        "${library_destination}/libnvperf_host.so" \
        libnvperf_dcgm_host.so
}

# stage_runtime_utilities stages shell and utility binaries under /usr.
stage_runtime_utilities() {
    local shell_source

    # Stage utilities only under /usr. The pinned distroless base supplies the
    # merged-/usr compatibility links /bin -> usr/bin and /sbin -> usr/sbin.
    # Staging a /bin or /sbin directory would conflict with those links when the
    # controlled root is overlaid onto the final image.
    #
    # Stage the canonical shell target as a regular file so /usr/bin/sh (and,
    # through the base link, /bin/sh) does not rely on Ubuntu's dash symlink.
    shell_source="$(readlink -f /usr/bin/sh)"
    stage_file package "$shell_source" /usr/bin/sh
    stage_file package /usr/bin/lshw /usr/bin/lshw
    stage_file package /usr/sbin/setcap /usr/bin/setcap
    stage_file package /usr/bin/env /usr/bin/env
    stage_file package /usr/bin/bash /usr/bin/bash

    if [[ -e /sbin/ldconfig.real ]]; then
        stage_file package /sbin/ldconfig.real /usr/sbin/ldconfig
    else
        stage_file package /sbin/ldconfig /usr/sbin/ldconfig
    fi
}

# stage_runtime_metadata stages project and generated configuration files.
stage_runtime_metadata() {
    stage_file project /usr/local/dcgm/dcgm-exporter-entrypoint.sh /usr/bin/dcgm-exporter-entrypoint.sh
    stage_file generated /etc/passwd /etc/passwd
    stage_file generated /etc/group /etc/group
    # Startup resolves libdcgm through ldconfig, so the generated cache is part
    # of the runtime contract.
    stage_file generated /etc/ld.so.cache /etc/ld.so.cache
}

# stage_runtime_files copies the package, project, and generated runtime files.
stage_runtime_files() {
    local architecture_directory

    architecture_directory="$(architecture_library_directory)"

    stage_runtime_libraries "$architecture_directory"
    stage_runtime_utilities
    stage_runtime_metadata

    sort -t $'\t' -k3,3 -o "$manifest" "$manifest"
}

# main stages the runtime filesystem and writes its provenance manifest.
main() {
    mkdir -p "$runtime_root" "$(dirname "$manifest")"
    : > "$manifest"

    stage_runtime_files

    manifest_count="$(wc -l < "$manifest")"
    staged_count="$(find "$runtime_root" \( -type f -o -type l \) -printf '.\n' | wc -l)"
    [[ "$manifest_count" -eq "$staged_count" ]] || {
        echo "Runtime manifest count $manifest_count does not match staged file count $staged_count" >&2
        return 1
    }
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    flags "$@"
    main
fi
