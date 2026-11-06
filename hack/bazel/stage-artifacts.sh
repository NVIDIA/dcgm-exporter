#!/usr/bin/env bash
# Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
# shellcheck disable=SC1091
# shellcheck source=hack/versions.env
source "${ROOT_DIR}/hack/versions.env"

architecture=""
artifact_set=""
BAZEL_CONFIG=""
STAGE_ROOT=""
BIN_DIR=""
BAZEL_RUN=("${ROOT_DIR}/hack/bazel/run.sh")

# Docker and package tools need normal files at stable paths, while Bazel writes
# configuration-dependent output paths. This helper asks Bazel for the exact
# outputs after a Bazel build, copies them to stable paths, and validates the
# staged results. It does not invoke a separate compiler outside the Bazel build graph.
# See hack/bazel/README.md for why the boundary exists and when it can go away.

# configure_inputs validates the requested architecture and artifact set.
configure_inputs() {
    architecture="${1:-}"
    artifact_set="${2:-product}"

    case "${architecture}" in
        amd64|arm64) ;;
        *) echo "Unsupported staging architecture: '${architecture}' (use amd64 or arm64)" >&2; exit 1 ;;
    esac
    case "${artifact_set}" in
        product|e2e-bundle|package|scan) ;;
        *) echo "Unsupported artifact set: ${artifact_set}" >&2; exit 1 ;;
    esac

    BAZEL_CONFIG="linux-${architecture}"
    STAGE_ROOT="$(realpath -m "${STAGED_ARTIFACT_ROOT:-${ROOT_DIR}/dist/staged/linux-${architecture}}")"
    if [[ "${STAGE_ROOT}" == / || "${STAGE_ROOT}" == "${ROOT_DIR}" || \
        (-n "${HOME:-}" && "${STAGE_ROOT}" == "$(realpath -m "${HOME}")") ]]; then
        echo "Refusing broad staging root: ${STAGE_ROOT}" >&2
        exit 1
    fi
    BIN_DIR="${STAGE_ROOT}/bin"
}

# prepare_stage_root removes only an empty or previously-created artifact root.
# A typo in STAGED_ARTIFACT_ROOT must not let a build erase unrelated files.
prepare_stage_root() {
    if [[ ! -e "${STAGE_ROOT}" ]]; then
        return 0
    fi
    if [[ ! -d "${STAGE_ROOT}" ]]; then
        echo "Refusing non-directory staging root: ${STAGE_ROOT}" >&2
        return 1
    fi
    if [[ -z "$(find "${STAGE_ROOT}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
        rmdir "${STAGE_ROOT}"
        return 0
    fi
    if [[ ! -f "${STAGE_ROOT}/.dcgm-exporter-stage" ]]; then
        echo "Refusing unrecognized staging root: ${STAGE_ROOT}" >&2
        return 1
    fi
    rm -rf "${STAGE_ROOT}"
}

# artifact_path locates one configured Bazel output file.
artifact_path() {
    local target="$1"
    shift
    local execution_root output
    output="$("${BAZEL_RUN[@]}" cquery --config="${BAZEL_CONFIG}" "$@" --output=files "${target}" 2>/dev/null)"
    if [[ -z "${output}" || "${output}" == *$'\n'* ]]; then
        echo "Unexpected Bazel output set for ${target}" >&2
        return 1
    fi
    execution_root="$("${BAZEL_RUN[@]}" info execution_root 2>/dev/null)"
    printf '%s/%s\n' "${execution_root}" "${output}"
}

# stage_built_artifact installs one output from the already-built target set.
stage_built_artifact() {
    local target="$1" destination="$2"
    shift 2
    local source
    source="$(artifact_path "${target}" "$@")"
    install -m 0755 "${source}" "${BIN_DIR}/${destination}"
}

# stage_source_artifact installs one declared source file from the active Bazel graph.
stage_source_artifact() {
    local target="$1" destination="$2" source
    source="$(artifact_path "${target}")"
    install -m 0644 "${source}" "${destination}"
}

# verify_architecture confirms an artifact matches the requested CPU architecture.
verify_architecture() {
    local path="$1" description
    description="$(file -b "${path}")"
    case "${architecture}" in
        amd64) [[ "${description}" == *"x86-64"* ]] ;;
        arm64) [[ "${description}" == *"ARM aarch64"* ]] ;;
        *) return 1 ;;
    esac || {
        echo "Unexpected ${architecture} artifact ${path}: ${description}" >&2
        return 1
    }
}

# verify_product_contract checks one exporter binary's release metadata and ABI.
verify_product_contract() {
    local binary="$1" newest_glibc expected_version
    expected_version="${EXPORTER_VERSION}"
    grep -aFq "${expected_version}" "${binary}" || {
        echo "Missing release version ${expected_version} in ${binary}" >&2
        return 1
    }
    newest_glibc="$(
        readelf --version-info "${binary}" \
            | grep -oE 'GLIBC_[0-9]+([.][0-9]+)+' \
            | sed 's/^GLIBC_//' \
            | sort -Vu \
            | tail -1
    )"
    [[ -n "${newest_glibc}" ]] || {
        echo "No glibc version requirements found in ${binary}" >&2
        return 1
    }
    [[ "$(printf '%s\n%s\n' "${newest_glibc}" "${PACKAGE_GLIBC_VERSION}" | sort -V | tail -1)" == \
        "${PACKAGE_GLIBC_VERSION}" ]] || {
        echo "${binary} requires glibc ${newest_glibc}; package floor is ${PACKAGE_GLIBC_VERSION}" >&2
        return 1
    }
}

# stage_artifacts builds the requested artifact set, stages its outputs, and validates them.
stage_artifacts() {
    local index package_archive package_check_root
    local -a build_args targets destinations

    prepare_stage_root
    build_args=(--config="${BAZEL_CONFIG}")
    case "${artifact_set}" in
        package)
            build_args+=(--config=release)
            targets=(//packaging:dcgm_exporter_tar)
            destinations=()
            ;;
        product)
            build_args+=(--config=release)
            targets=(
                //cmd/dcgm-exporter:dcgm-exporter
                //hack/licenses:notice_generator
            )
            destinations=(
                dcgm-exporter
                dcgm-exporter-notice-generator
            )
            ;;
        scan)
            # The image ships a stripped exporter. govulncheck needs the symbol
            # table to report only the code the binary actually links.
            build_args+=(--config=release --strip=never)
            targets=(//cmd/dcgm-exporter:dcgm-exporter)
            destinations=(dcgm-exporter)
            ;;
        e2e-bundle)
            build_args+=(
                --config=release
                "--@rules_go//go/config:tags=container,e2e,k8s,dcgm_uri_integration"
            )
            targets=(
                //cmd/dcgm-exporter:dcgm-exporter
                //cmd/e2e:e2e
                //tests/cmd/e2e-dcgm-probe:e2e-dcgm-probe
                //tests/static:static_test
                //tests/container:container_test
                //tests/k8s:k8s_test
                //tests/host:host_test
            )
            destinations=(
                dcgm-exporter
                e2e
                e2e-dcgm-probe
                dcgm-exporter-static.test
                dcgm-exporter-container.test
                dcgm-exporter-k8s.test
                dcgm-exporter-host.test
            )
            ;;
        *) echo "Unsupported artifact set: ${artifact_set}" >&2; return 1 ;;
    esac

    "${BAZEL_RUN[@]}" build "${build_args[@]}" "${targets[@]}"

    if [[ "${artifact_set}" == package ]]; then
        mkdir -p "${STAGE_ROOT}/package"
        package_archive="$(artifact_path "${targets[0]}" "${build_args[@]}")"
        install -m 0644 "${package_archive}" "${STAGE_ROOT}/package/dcgm_exporter.tar.gz"
        package_check_root="$(mktemp -d "${TMPDIR:-/tmp}/dcgm-exporter-package-check.XXXXXX")"
        (
            trap 'rm -rf "${package_check_root}"' EXIT
            tar -xzf "${STAGE_ROOT}/package/dcgm_exporter.tar.gz" \
                -C "${package_check_root}" \
                dcgm_exporter/usr/bin/dcgm-exporter
            verify_architecture "${package_check_root}/dcgm_exporter/usr/bin/dcgm-exporter"
            verify_product_contract "${package_check_root}/dcgm_exporter/usr/bin/dcgm-exporter"
        )
    else
        mkdir -p "${BIN_DIR}" "${STAGE_ROOT}/etc"
        for index in "${!targets[@]}"; do
            stage_built_artifact \
                "${targets[index]}" \
                "${destinations[index]}" \
                "${build_args[@]}"
        done
    fi

    if [[ "${artifact_set}" == e2e-bundle ]]; then
        stage_source_artifact \
            @com_github_nvidia_go_dcgm//pkg/dcgm:const_fields.go \
            "${STAGE_ROOT}/etc/go-dcgm-const-fields.go"
        # Keep the generated field map out of the checkout's Go package walk.
        # The staged bundle is consumed as artifacts, never as source code.
        printf '%s\n' \
            'module example.com/dcgm-exporter-staged-artifacts' \
            'go 1.27' \
            >"${STAGE_ROOT}/go.mod"
    fi

    if [[ "${artifact_set}" != package ]]; then
        for artifact in "${BIN_DIR}"/*; do
            verify_architecture "${artifact}"
        done
        verify_product_contract "${BIN_DIR}/dcgm-exporter"
    fi

    : >"${STAGE_ROOT}/.dcgm-exporter-stage"

    echo "Staged ${artifact_set} artifacts under ${STAGE_ROOT}"
}

# main configures the staging request and builds its artifacts.
main() {
    stage_artifacts
}

configure_inputs "${1:-}" "${2:-product}"
main
