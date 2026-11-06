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

set -Eeuo pipefail

# shellcheck source=hack/versions.env
source "$(dirname "${BASH_SOURCE[0]}")/../versions.env"

# usage_info prints the command synopsis.
usage_info() {
    printf 'Usage: %s [--help]\n' "$(basename "$0")"
}

# help describes the release inputs configured by hack/versions.env.
help() {
    usage_info
    cat <<'EOF'

Mirrors the approved production NGC image aliases to Docker Hub.

Required environment:
  DOCKERHUB_USERNAME  Docker Hub user name
  DOCKERHUB_PAT_TOKEN Docker Hub personal-access token
EOF
}

# flags accepts the help option and rejects all other arguments.
flags() {
    case "$#" in
        0) ;;
        1)
            if [[ "$1" == --help || "$1" == -h ]]; then
                help
                exit 0
            fi
            usage_info >&2
            exit 2
            ;;
        *)
            usage_info >&2
            exit 2
            ;;
    esac
}

ngc_image="${EXPORTER_IMAGE_REPOSITORY}"
dockerhub_image="${EXPORTER_DOCKER_HUB_IMAGE_REPOSITORY}"
release_tags=(
    "${EXPORTER_VERSION}"
    "${DCGM_VERSION}-${EXPORTER_VERSION}-distroless"
    "${DCGM_VERSION}-${EXPORTER_VERSION}"
    latest
)
source_digest=""
source_pinned=""
pending_aliases=()
DOCKER_CONFIG_DIR=""

# inspect_field reads one image-manifest field through Docker Buildx.
inspect_field() {
    local format="$1"
    local image="$2"
    docker buildx imagetools inspect --format "${format}" "${image}"
}

# inspect_digest reads an image digest without line endings.
inspect_digest() {
    inspect_field '{{println .Manifest.Digest}}' "$1" | tr -d '\r\n'
}

# validate_source_aliases confirms every approved NGC alias resolves to one digest.
validate_source_aliases() {
    local alias
    local alias_digest
    local tag
    local canonical_alias="${ngc_image}:${release_tags[0]}"

    if ! source_digest="$(inspect_digest "${canonical_alias}")"; then
        echo "Unable to inspect production NGC alias ${canonical_alias}" >&2
        return 1
    fi
    [[ "${source_digest}" =~ ^sha256:[0-9a-f]{64}$ ]] || {
        echo "Production NGC alias ${canonical_alias} returned an invalid digest: ${source_digest}" >&2
        return 2
    }
    for tag in "${release_tags[@]:1}"; do
        alias="${ngc_image}:${tag}"
        if ! alias_digest="$(inspect_digest "${alias}")"; then
            echo "Unable to inspect production NGC alias ${alias}" >&2
            return 1
        fi
        [[ "${alias_digest}" == "${source_digest}" ]] || {
            echo "Production NGC alias ${alias} has ${alias_digest}; expected ${source_digest}" >&2
            return 1
        }
    done
    source_pinned="${ngc_image}@${source_digest}"
}

# validate_source_platforms confirms the approved source is exactly the expected image index.
validate_source_platforms() {
    local descriptor_type extra platform platform_descriptors runtime_platforms_sorted source_media_type
    local -a runtime_platforms=()

    source_media_type="$(inspect_field '{{println .Manifest.MediaType}}' "${source_pinned}" | tr -d '\r\n')"
    [[ "${source_media_type}" == application/vnd.docker.distribution.manifest.list.v2+json ]] || {
        echo "Approved NGC source must be a Docker v2 manifest list: ${source_media_type}" >&2
        return 1
    }
    platform_descriptors="$(inspect_field '{{range .Manifest.Manifests}}{{printf "%s/%s " .Platform.OS .Platform.Architecture}}{{with .Annotations}}{{index . "vnd.docker.reference.type"}}{{end}}{{println}}{{end}}' "${source_pinned}")"
    while read -r platform descriptor_type extra; do
        case "${platform}" in
            linux/amd64|linux/arm64)
                [[ -z "${descriptor_type:-}${extra:-}" ]] || { echo "Unexpected platform descriptor in approved NGC source: ${platform} ${descriptor_type} ${extra}" >&2; return 1; }
                runtime_platforms+=("${platform}")
                ;;
            *) echo "Unexpected platform descriptor in approved NGC source: ${platform}" >&2; return 1 ;;
        esac
    done <<<"${platform_descriptors}"
    runtime_platforms_sorted="$(printf '%s\n' "${runtime_platforms[@]}" | sort)"
    [[ "${runtime_platforms_sorted}" == $'linux/amd64\nlinux/arm64' ]] || { echo "Approved NGC source must contain exactly linux/amd64 and linux/arm64 images" >&2; return 1; }
}

# cleanup removes the temporary Docker configuration directory.
cleanup() {
    [[ -z "${DOCKER_CONFIG_DIR}" ]] || rm -rf -- "${DOCKER_CONFIG_DIR}"
}

# configure_dockerhub_auth creates an isolated Docker configuration and logs in.
configure_dockerhub_auth() {
    local source_config

    : "${DOCKERHUB_USERNAME:?DOCKERHUB_USERNAME is required}"
    : "${DOCKERHUB_PAT_TOKEN:?DOCKERHUB_PAT_TOKEN is required}"
    DOCKER_CONFIG_DIR="$(mktemp -d "${TMPDIR:-/tmp}/dcgm-exporter-dockerhub.XXXXXX")"
    trap cleanup EXIT
    source_config="${DOCKER_CONFIG:-${HOME:-}/.docker}/config.json"
    [[ ! -f "${source_config}" ]] || cp "${source_config}" "${DOCKER_CONFIG_DIR}/config.json"
    export DOCKER_CONFIG="${DOCKER_CONFIG_DIR}"
    printf '%s' "${DOCKERHUB_PAT_TOKEN}" | docker login docker.io --username "${DOCKERHUB_USERNAME}" --password-stdin >/dev/null
}

# is_missing_destination_error identifies a registry-confirmed missing manifest.
is_missing_destination_error() {
    local destination="$1"
    local output="$2"
    grep -Eiq '(^|[^[:alnum:]_])(manifest[ _]unknown|name[ _]unknown|no such manifest)([^[:alnum:]_]|$)' <<<"${output}" \
        || grep -Fqi "${destination}: not found" <<<"${output}" \
        || grep -Fqi "${destination}: 404 Not Found" <<<"${output}"
}

# Returns 1 only for a registry-confirmed missing manifest and 2 for every
# other inspection failure.
inspect_destination_digest() {
    local destination="$1"
    local output
    if output="$(inspect_digest "${destination}" 2>&1)"; then
        printf '%s' "${output}"
        return 0
    fi
    if is_missing_destination_error "${destination}" "${output}"; then
        return 1
    fi
    printf '%s\n' "${output}" >&2
    echo "Unable to inspect Docker Hub alias ${destination}" >&2
    return 2
}

# plan_mirror identifies missing aliases and rejects immutable conflicts before writing.
plan_mirror() {
    local destination destination_digest inspect_status tag

    pending_aliases=()
    for tag in "${release_tags[@]}"; do
        destination="${dockerhub_image}:${tag}"
        if destination_digest="$(inspect_destination_digest "${destination}")"; then
            if [[ "${destination_digest}" == "${source_digest}" ]]; then
                printf 'Already mirrored %s -> %s\n' "${destination}" "${destination_digest}"
            elif [[ "${tag}" == latest ]]; then
                pending_aliases+=("${destination}")
            else
                echo "Refusing to overwrite immutable Docker Hub alias ${destination}: found ${destination_digest}, expected ${source_digest}" >&2
                return 1
            fi
        else
            inspect_status=$?
            [[ "${inspect_status}" -eq 1 ]] && pending_aliases+=("${destination}") || return "${inspect_status}"
        fi
    done
}

# mirror_pending_aliases creates all queued aliases in one Docker Buildx operation.
mirror_pending_aliases() {
    local destination
    local -a create_args=()

    [[ "${#pending_aliases[@]}" -gt 0 ]] || return 0
    for destination in "${pending_aliases[@]}"; do create_args+=(--tag "${destination}"); done
    printf 'Mirroring %s to %s Docker Hub release alias(es)\n' "${source_pinned}" "${#pending_aliases[@]}"
    docker buildx imagetools create --prefer-index=false "${create_args[@]}" "${source_pinned}"
}

# verify_destination waits for a mirrored alias to resolve to the source digest.
verify_destination() {
    local destination="$1"
    local actual_digest=""
    local inspect_status
    local attempt
    for attempt in 1 2 3 4 5; do
        if actual_digest="$(inspect_destination_digest "${destination}")"; then
            if [[ "${actual_digest}" == "${source_digest}" ]]; then
                printf 'Verified %s -> %s\n' "${destination}" "${actual_digest}"
                return 0
            fi
        else
            inspect_status=$?
            if [[ "${inspect_status}" -ne 1 ]]; then
                return "${inspect_status}"
            fi
            actual_digest=""
        fi
        if [[ "${attempt}" -lt 5 ]]; then
            sleep 2
        fi
    done
    echo "Docker Hub verification failed for ${destination}: expected ${source_digest}, got ${actual_digest:-missing}" >&2
    return 1
}

# verify_destinations confirms all approved Docker Hub aliases resolve to the source digest.
verify_destinations() {
    local tag
    for tag in "${release_tags[@]}"; do verify_destination "${dockerhub_image}:${tag}"; done
}

# main validates the source, plans the aliases, mirrors them, and verifies the result.
main() {
    validate_source_aliases
    validate_source_platforms
    configure_dockerhub_auth
    plan_mirror
    mirror_pending_aliases
    verify_destinations
}

flags "$@"
main
