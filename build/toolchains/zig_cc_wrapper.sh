#!/usr/bin/env bash

# go-dcgm and go-nvml use CGO, so rules_go needs a C toolchain. Use the pinned
# Zig repository instead of a runner's compiler to produce the same amd64 or
# arm64, glibc-2.28-compatible output on either runner architecture. See
# build/toolchains/README.md for the constraints this wrapper preserves.

set -euo pipefail

target_or_mode="$1"
shift

execution_root="${GO_CC_ROOT:-$(pwd -P)}"
case "$(uname -m)" in
    x86_64) zig_repository="zig_linux_amd64" ;;
    aarch64 | arm64) zig_repository="zig_linux_arm64" ;;
    *) echo "unsupported Zig execution architecture: $(uname -m)" >&2; exit 1 ;;
esac

# Bazel changes the canonical external-repository directory name between
# releases. The declared repository name is stable; select its one executable
# Zig archive rather than coupling the toolchain wrapper to that internal name.
shopt -s nullglob
zig_candidates=("${execution_root}"/external/*"${zig_repository}"/zig)
if [[ "${#zig_candidates[@]}" -ne 1 ]]; then
    printf 'expected one %s Zig executable below %s/external, found %s\n' \
        "${zig_repository}" "${execution_root}" "${#zig_candidates[@]}" >&2
    exit 1
fi
zig_path="${zig_candidates[0]}"
zig_relative_root="${zig_path#"${execution_root}/"}"
zig_relative_root="${zig_relative_root%/zig}"

export ZIG_GLOBAL_CACHE_DIR="${ZIG_GLOBAL_CACHE_DIR:-${TMPDIR:-/tmp}/dcgm-exporter-zig-global}"
export ZIG_LOCAL_CACHE_DIR="${ZIG_LOCAL_CACHE_DIR:-${TMPDIR:-/tmp}/dcgm-exporter-zig-local}"

if [[ "${target_or_mode}" == --ar ]]; then
    exec "${zig_path}" ar "$@"
fi
target="${target_or_mode}"

depfile=""
previous=""
# Zig supplies the C runtime. Keep Clang's GCC discovery inside that archive
# so host GCC installations cannot affect compilation or driver diagnostics.
arguments=("--gcc-toolchain=${zig_path%/zig}")
allow_unresolved=false
for argument in "$@"; do
    if [[ "${previous}" == -MF ]]; then
        depfile="${argument}"
    fi
    case "${argument}" in
        -MF*) depfile="${argument#-MF}" ;;
    esac
    previous="${argument}"
    if [[ "${argument}" == '-Wl,--unresolved-symbols=ignore-in-object-files' ]]; then
        allow_unresolved=true
        continue
    fi
    arguments+=("${argument}")
done

if [[ "${allow_unresolved}" == true ]]; then
    # CGO's partial link intentionally leaves Go-provided symbols unresolved.
    # Ask Zig for its final linker command, remove eager binding, and invoke LLD
    # with the unresolved-symbol policy requested by rules_go.
    set +e
    link_trace="$("${zig_path}" cc -target "${target}" -fno-sanitize=all -no-canonical-prefixes "${arguments[@]}" -### 2>&1)"
    link_status=$?
    set -e
    # The sed program's dollar sign is an end-of-input address, not shell expansion.
    # shellcheck disable=SC2016
    link_command="$(sed -n '/^ld\.lld /h; ${x;p;}' <<<"${link_trace}")"
    if [[ -z "${link_command}" ]]; then
        printf '%s\n' "${link_trace}" >&2
        [[ "${link_status}" -ne 0 ]] || link_status=1
        exit "${link_status}"
    fi
    read -r -a linker_arguments <<<"${link_command}"
    filtered_linker_arguments=()
    for argument in "${linker_arguments[@]:1}"; do
        [[ "${argument}" == -znow ]] && continue
        filtered_linker_arguments+=("${argument}")
    done
    exec "${zig_path}" ld.lld \
        "${filtered_linker_arguments[@]}" \
        --unresolved-symbols=ignore-in-object-files
fi

"${zig_path}" cc -target "${target}" -fno-sanitize=all -no-canonical-prefixes "${arguments[@]}"

if [[ -n "${depfile}" && -f "${depfile}" ]]; then
    # Absolute external-repository paths differ between Bazel execution roots.
    # Rewriting them keeps dependency files safe for sandbox and cache reuse.
    zig_absolute_root="$(dirname "$(readlink -f "${zig_path}")")"
    sed -i "s#${zig_absolute_root}/#${zig_relative_root}/#g" "${depfile}"
fi
