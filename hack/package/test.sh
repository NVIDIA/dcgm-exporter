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

# Validate dcgm-exporter package tarballs by installing and running them.
#
# This compatibility guard consumes repo-produced
# dist/dcgm_exporter-*.tar.gz payload tarballs, creates temporary CI-only
# RPM/DEB metadata, and uses Docker Buildx to build throwaway packages. It then
# uses more Buildx Dockerfile RUN steps to install those packages in target
# distro images and run /usr/bin/dcgm-exporter --version.
#
# A failed Buildx build means package creation, image setup, native package
# install, expected file checks, or the installed-binary smoke test failed.
# The temporary packages are not byte-for-byte release artifacts.

set -Eeuo pipefail

ARG0="$(basename "$0")"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
export LOG_PREFIX="test-package"

# shellcheck disable=SC1091
# shellcheck source=hack/utils.sh
source "${ROOT_DIR}/hack/utils.sh"

# shellcheck disable=SC1091
# shellcheck source=hack/versions.env
source "${ROOT_DIR}/hack/versions.env"

PACKAGE_NAME="${PACKAGE_NAME:-datacenter-gpu-manager-exporter}"
PACKAGE_VERSION="${PACKAGE_VERSION:-${EXPORTER_VERSION}.${PACKAGE_REVISION}}"
PACKAGE_RELEASE="${PACKAGE_RELEASE:-1}"
PACKAGE_TARBALLS="${PACKAGE_TARBALLS:-dist/dcgm_exporter-*.tar.gz}"
PACKAGE_PLATFORM="${PACKAGE_PLATFORM:-linux/amd64}"
PACKAGE_COMPONENT_DIR="${PACKAGE_COMPONENT_DIR:-dcgm_exporter}"
PACKAGE_WORK_ROOT="${PACKAGE_WORK_ROOT:-${ROOT_DIR}}"
PACKAGE_BUILDX_BUILDER="${PACKAGE_BUILDX_BUILDER:-${BUILDX_BUILDER:-}}"

RPM_BUILDER_IMAGE="${RPM_BUILDER_IMAGE:-registry.access.redhat.com/ubi8/ubi:latest}"
RPM_TEST_IMAGES="${RPM_TEST_IMAGES:-registry.access.redhat.com/ubi8/ubi:latest registry.access.redhat.com/ubi9/ubi:latest}"
DEB_BUILDER_IMAGE="${DEB_BUILDER_IMAGE:-ubuntu:22.04}"
DEB_TEST_IMAGES="${DEB_TEST_IMAGES:-ubuntu:22.04 ubuntu:24.04 ubuntu:26.04}"

PACKAGE_CLEANUP_DIR=""
PACKAGE_TARBALL_ARGS=()

# usage_info prints the short command summary.
usage_info() {
    cat <<EOF
Usage: ${ARG0} [tarball ...]

Build temporary CI-only RPM/DEB packages from dcgm-exporter package tarballs,
install them in supported distro containers, and run dcgm-exporter --version.
EOF
}

# usage prints the short command summary to stderr and exits with a usage error.
usage() {
    exec 1>&2
    usage_info
    exit 1
}

# help prints the full command and option help.
help() {
    usage_info
    cat <<EOF

Options:
  --package-name name          temporary package name (env: PACKAGE_NAME, default: ${PACKAGE_NAME})
  --package-version version    temporary package version (env: PACKAGE_VERSION, default: ${PACKAGE_VERSION})
  --package-release release    temporary package release (env: PACKAGE_RELEASE, default: ${PACKAGE_RELEASE})
  --package-tarballs globs     tarball globs used when no tarball args are passed (env: PACKAGE_TARBALLS)
  --package-platform platform  Docker platform fallback when the tarball name does not identify an arch
  --package-component-dir dir  component directory inside each tarball (env: PACKAGE_COMPONENT_DIR)
  --rpm-builder-image image    RPM builder image (env: RPM_BUILDER_IMAGE, default: ${RPM_BUILDER_IMAGE})
  --rpm-test-images images     space-separated RPM install validation images (env: RPM_TEST_IMAGES)
  --deb-builder-image image    DEB builder image (env: DEB_BUILDER_IMAGE, default: ${DEB_BUILDER_IMAGE})
  --deb-test-images images     space-separated DEB install validation images (env: DEB_TEST_IMAGES)
  -h, --help                   show this help message

Environment:
  PACKAGE_WORK_ROOT       directory used for temporary BuildKit contexts
                          (default: ${PACKAGE_WORK_ROOT})
  PACKAGE_BUILDX_BUILDER  docker buildx builder used for package validation
                          (default: ${PACKAGE_BUILDX_BUILDER:-docker buildx default})

Examples:
  ${ARG0}
  ${ARG0} dist/dcgm_exporter-linux-x86-64-${PACKAGE_VERSION}.tar.gz
  ${ARG0} --package-platform linux/arm64 dist/dcgm_exporter-linux-sbsa-${PACKAGE_VERSION}.tar.gz
EOF
    exit 0
}

# need_value fails when an option that requires a value is missing one.
need_value() {
    local option="${1}"
    local value="${2:-}"
    [[ -n "${value}" ]] || die "No value supplied for ${option}"
}

# flags parses command-line options and records positional tarball paths.
flags() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --package-name)
                need_value "$1" "${2:-}"
                PACKAGE_NAME="$2"
                shift 2
                ;;
            --package-version)
                need_value "$1" "${2:-}"
                PACKAGE_VERSION="$2"
                shift 2
                ;;
            --package-release)
                need_value "$1" "${2:-}"
                PACKAGE_RELEASE="$2"
                shift 2
                ;;
            --package-tarballs)
                need_value "$1" "${2:-}"
                PACKAGE_TARBALLS="$2"
                shift 2
                ;;
            --package-platform)
                need_value "$1" "${2:-}"
                PACKAGE_PLATFORM="$2"
                shift 2
                ;;
            --package-component-dir)
                need_value "$1" "${2:-}"
                PACKAGE_COMPONENT_DIR="$2"
                shift 2
                ;;
            --rpm-builder-image)
                need_value "$1" "${2:-}"
                RPM_BUILDER_IMAGE="$2"
                shift 2
                ;;
            --rpm-test-images)
                need_value "$1" "${2:-}"
                RPM_TEST_IMAGES="$2"
                shift 2
                ;;
            --deb-builder-image)
                need_value "$1" "${2:-}"
                DEB_BUILDER_IMAGE="$2"
                shift 2
                ;;
            --deb-test-images)
                need_value "$1" "${2:-}"
                DEB_TEST_IMAGES="$2"
                shift 2
                ;;
            -h|--help|help)
                help
                ;;
            -*)
                die "Unsupported option ${1}"
                ;;
            *)
                PACKAGE_TARBALL_ARGS+=("$1")
                shift
                ;;
        esac
    done
}

# cleanup removes the temporary package build workspace.
cleanup() {
    if [[ -n "${PACKAGE_CLEANUP_DIR}" ]]; then
        rm -rf "${PACKAGE_CLEANUP_DIR}"
    fi
}

# docker_buildx_build runs a buildx build with the configured builder.
docker_buildx_build() {
    local -a args=(buildx build)

    if [[ -n "${PACKAGE_BUILDX_BUILDER}" ]]; then
        args+=(--builder "${PACKAGE_BUILDX_BUILDER}")
    fi

    docker "${args[@]}" "$@"
}

# require_buildx verifies that Docker Buildx is available.
require_buildx() {
    docker buildx version >/dev/null 2>&1 || die "docker buildx is required"
}

# abs_path prints an absolute path rooted at the repository for relative input.
abs_path() {
    local path="${1}"

    if [[ "${path}" = /* ]]; then
        printf '%s\n' "${path}"
    else
        printf '%s/%s\n' "${ROOT_DIR}" "${path}"
    fi
}

# resolve_tarballs prints explicit tarball paths or expands PACKAGE_TARBALLS globs.
resolve_tarballs() {
    local pattern
    local match
    local -a patterns=()
    local -a resolved=()

    if [[ $# -gt 0 ]]; then
        for match in "$@"; do
            [[ -f "${match}" ]] || die "Package tarball not found: ${match}"
            abs_path "${match}"
        done
        return
    fi

    # shellcheck disable=SC2206
    patterns=(${PACKAGE_TARBALLS})
    for pattern in "${patterns[@]}"; do
        while IFS= read -r match; do
            [[ -f "${match}" ]] && resolved+=("${match}")
        done < <(compgen -G "${pattern}" || true)
    done

    if [[ "${#resolved[@]}" -eq 0 ]]; then
        die "No package tarballs found for: ${PACKAGE_TARBALLS}"
    fi

    for match in "${resolved[@]}"; do
        abs_path "${match}"
    done
}

# image_digest prints the first resolved repo digest for a pulled image.
image_digest() {
    local image="${1}"
    local digest

    digest="$(
        docker image inspect \
            --format '{{range .RepoDigests}}{{println .}}{{end}}' \
            "${image}" 2>/dev/null | sed -n '1p'
    )"

    if [[ -z "${digest}" ]]; then
        digest="digest unavailable"
    fi

    printf '%s' "${digest}"
}

# infer_platform maps a package tarball name to its Docker platform.
infer_platform() {
    local tarball_name="${1}"

    case "${tarball_name}" in
        *linux-sbsa*|*linux-aarch64*|*linux-arm64*)
            printf '%s\n' "linux/arm64"
            ;;
        *linux-x86_64*|*linux-x86-64*|*linux-amd64*)
            printf '%s\n' "linux/amd64"
            ;;
        *)
            printf '%s\n' "${PACKAGE_PLATFORM}"
            ;;
    esac
}

# rpm_arch_for_platform maps a Docker platform to an RPM architecture.
rpm_arch_for_platform() {
    case "${1}" in
        linux/amd64) printf '%s\n' "x86_64" ;;
        linux/arm64) printf '%s\n' "aarch64" ;;
        *) die "Unsupported package platform for RPM: ${1}" ;;
    esac
}

# deb_arch_for_platform maps a Docker platform to a Debian architecture.
deb_arch_for_platform() {
    case "${1}" in
        linux/amd64) printf '%s\n' "amd64" ;;
        linux/arm64) printf '%s\n' "arm64" ;;
        *) die "Unsupported package platform for DEB: ${1}" ;;
    esac
}

# diagnose_payload_binary prints ELF diagnostics for a missing or invalid payload binary.
diagnose_payload_binary() {
    local binary="${1}"

    log_info "---- payload binary diagnostics: ${binary}"
    if command -v file >/dev/null 2>&1; then
        file "${binary}" || true
    fi
    if command -v ldd >/dev/null 2>&1; then
        ldd "${binary}" || true
    fi
    if command -v readelf >/dev/null 2>&1; then
        readelf -W -d "${binary}" || true
        readelf -W --version-info "${binary}" 2>/dev/null \
            | sed -n '/Name: /p' \
            | sort -u || true
    fi
    log_info "---- end diagnostics"
}

# write_rpm_spec writes the temporary CI RPM spec file.
write_rpm_spec() {
    local spec_path="${1}"

    cat > "${spec_path}" <<EOF
Name:           ${PACKAGE_NAME}
Version:        ${PACKAGE_VERSION}
Release:        ${PACKAGE_RELEASE}%{?dist}
Summary:        NVIDIA DCGM Exporter
License:        Apache-2.0
URL:            https://github.com/NVIDIA/dcgm-exporter
AutoReqProv:    yes

%description
NVIDIA DCGM Exporter exposes NVIDIA DCGM GPU metrics in Prometheus text format.

%install
rm -rf %{buildroot}
mkdir -p %{buildroot}
cp -a /payload/${PACKAGE_COMPONENT_DIR}/. %{buildroot}/

%files
%license /LICENSE
/usr/bin/dcgm-exporter
/etc/dcgm-exporter/1.x-compatibility-metrics.csv
/etc/dcgm-exporter/dcp-metrics-included.csv
/etc/dcgm-exporter/default-counters.csv
/lib/systemd/system/nvidia-dcgm-exporter.service
EOF
}

# build_rpm builds a temporary RPM package from the unpacked tarball payload.
build_rpm() {
    local platform="${1}"
    local rpm_arch="${2}"
    local payload_dir="${3}"
    local work_dir="${4}"
    local context_dir="${work_dir}/context"

    rm -rf "${context_dir}"
    mkdir -p "${context_dir}/payload"
    cp -a "${payload_dir}/${PACKAGE_COMPONENT_DIR}" "${context_dir}/payload/${PACKAGE_COMPONENT_DIR}"
    write_rpm_spec "${context_dir}/package.spec"

    # Buildx runs rpmbuild inside the target-platform container and exports
    # only the generated RPM file to the local output directory.
    cat > "${context_dir}/Dockerfile" <<'EOF'
ARG RPM_BUILDER_IMAGE=registry.access.redhat.com/ubi8/ubi:latest
FROM ${RPM_BUILDER_IMAGE} AS package-build

ARG PACKAGE_COMPONENT_DIR
ARG RPM_ARCH

WORKDIR /work
COPY package.spec /work/package.spec
COPY payload /payload

RUN set -eux; \
    dnf install -y --setopt=install_weak_deps=False cpio findutils gzip rpm-build tar; \
    dnf clean all; \
    rpmbuild -bb \
        --define "_topdir /work/rpmbuild" \
        --target "${RPM_ARCH}" \
        /work/package.spec; \
    mkdir -p /out; \
    cp /work/rpmbuild/RPMS/*/*.rpm /out/

FROM scratch
COPY --from=package-build /out/ /
EOF

    log_info "==> Building temporary RPM package (${platform}, ${rpm_arch})"
    docker pull --platform "${platform}" "${RPM_BUILDER_IMAGE}" >/dev/null
    log_info "RPM builder image: $(image_digest "${RPM_BUILDER_IMAGE}")"

    docker_buildx_build \
        --pull \
        --progress=plain \
        --platform "${platform}" \
        --build-arg "RPM_BUILDER_IMAGE=${RPM_BUILDER_IMAGE}" \
        --build-arg "PACKAGE_COMPONENT_DIR=${PACKAGE_COMPONENT_DIR}" \
        --build-arg "RPM_ARCH=${rpm_arch}" \
        --output "type=local,dest=${work_dir}/out" \
        "${context_dir}"
}

# write_deb_control writes the temporary CI Debian control file.
write_deb_control() {
    local control_path="${1}"
    local deb_arch="${2}"

    cat > "${control_path}" <<EOF
Package: ${PACKAGE_NAME}
Version: ${PACKAGE_VERSION}-${PACKAGE_RELEASE}
Section: utils
Priority: optional
Architecture: ${deb_arch}
Maintainer: NVIDIA <dcgm@nvidia.com>
Depends: libc6
Description: NVIDIA DCGM Exporter
 NVIDIA DCGM Exporter exposes NVIDIA DCGM GPU metrics in Prometheus text format.
EOF
}

# build_deb builds a temporary Debian package from the unpacked tarball payload.
build_deb() {
    local platform="${1}"
    local deb_arch="${2}"
    local payload_dir="${3}"
    local work_dir="${4}"
    local package_file="${PACKAGE_NAME}_${PACKAGE_VERSION}-${PACKAGE_RELEASE}_${deb_arch}.deb"
    local context_dir="${work_dir}/context"

    rm -rf "${context_dir}"
    mkdir -p "${context_dir}/payload"
    cp -a "${payload_dir}/${PACKAGE_COMPONENT_DIR}" "${context_dir}/payload/${PACKAGE_COMPONENT_DIR}"
    write_deb_control "${context_dir}/control" "${deb_arch}"

    # Buildx runs dpkg-deb inside the target-platform container and exports
    # only the generated DEB file to the local output directory.
    cat > "${context_dir}/Dockerfile" <<'EOF'
ARG DEB_BUILDER_IMAGE=ubuntu:22.04
FROM ${DEB_BUILDER_IMAGE} AS package-build

ARG PACKAGE_COMPONENT_DIR
ARG PACKAGE_FILE
ENV DEBIAN_FRONTEND=noninteractive

WORKDIR /work
COPY control /work/control
COPY payload /payload

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates dpkg-dev; \
    rm -rf /var/lib/apt/lists/*; \
    mkdir -p /work/pkg/DEBIAN /out; \
    cp -a "/payload/${PACKAGE_COMPONENT_DIR}/." /work/pkg/; \
    cp /work/control /work/pkg/DEBIAN/control; \
    chmod 0755 /work/pkg/DEBIAN; \
    dpkg-deb --build --root-owner-group /work/pkg "/out/${PACKAGE_FILE}"

FROM scratch
COPY --from=package-build /out/ /
EOF

    log_info "==> Building temporary DEB package (${platform}, ${deb_arch})"
    docker pull --platform "${platform}" "${DEB_BUILDER_IMAGE}" >/dev/null
    log_info "DEB builder image: $(image_digest "${DEB_BUILDER_IMAGE}")"

    docker_buildx_build \
        --pull \
        --progress=plain \
        --platform "${platform}" \
        --build-arg "DEB_BUILDER_IMAGE=${DEB_BUILDER_IMAGE}" \
        --build-arg "PACKAGE_COMPONENT_DIR=${PACKAGE_COMPONENT_DIR}" \
        --build-arg "PACKAGE_FILE=${package_file}" \
        --output "type=local,dest=${work_dir}/out" \
        "${context_dir}"
}

# validate_rpm installs and smoke-tests one temporary RPM package in one image.
validate_rpm() {
    local platform="${1}"
    local image="${2}"
    local rpm_file="${3}"
    local context_dir

    log_info "==> Installing $(basename "${rpm_file}") on ${image} (${platform})"
    docker pull --platform "${platform}" "${image}" >/dev/null
    log_info "Resolved image: $(image_digest "${image}")"

    context_dir="$(mktemp -d "${PACKAGE_CLEANUP_DIR}/rpm-validate.XXXXXX")"
    cp "${rpm_file}" "${context_dir}/package.rpm"

    # Buildx is the cross-arch execution environment. If this image build
    # succeeds, every RPM install and smoke-test RUN step passed.
    cat > "${context_dir}/Dockerfile" <<'EOF'
ARG TEST_IMAGE=registry.access.redhat.com/ubi8/ubi:latest
FROM ${TEST_IMAGE}

ARG PACKAGE_NAME
COPY package.rpm /pkg/package.rpm

RUN set -eux; \
    rpm -qpi /pkg/package.rpm; \
    rpm -qpl /pkg/package.rpm; \
    rpm -qp --requires /pkg/package.rpm || true; \
    dnf install -y --setopt=install_weak_deps=False file binutils findutils; \
    dnf install -y /pkg/package.rpm; \
    rpm -q "${PACKAGE_NAME}"; \
    rpm -ql "${PACKAGE_NAME}" | sort; \
    test -x /usr/bin/dcgm-exporter; \
    test -f /etc/dcgm-exporter/1.x-compatibility-metrics.csv; \
    test -f /etc/dcgm-exporter/dcp-metrics-included.csv; \
    test -f /etc/dcgm-exporter/default-counters.csv; \
    test -f /lib/systemd/system/nvidia-dcgm-exporter.service; \
    file /usr/bin/dcgm-exporter; \
    ldd /usr/bin/dcgm-exporter || true; \
    readelf -W -d /usr/bin/dcgm-exporter || true; \
    readelf -W --version-info /usr/bin/dcgm-exporter 2>/dev/null \
        | sed -n "/Name: /p" \
        | sort -u || true; \
    /usr/bin/dcgm-exporter --version
EOF

    docker_buildx_build \
        --pull \
        --progress=plain \
        --platform "${platform}" \
        --build-arg "TEST_IMAGE=${image}" \
        --build-arg "PACKAGE_NAME=${PACKAGE_NAME}" \
        "${context_dir}"
}

# validate_deb installs and smoke-tests one temporary Debian package in one image.
validate_deb() {
    local platform="${1}"
    local image="${2}"
    local deb_file="${3}"
    local context_dir

    log_info "==> Installing $(basename "${deb_file}") on ${image} (${platform})"
    docker pull --platform "${platform}" "${image}" >/dev/null
    log_info "Resolved image: $(image_digest "${image}")"

    context_dir="$(mktemp -d "${PACKAGE_CLEANUP_DIR}/deb-validate.XXXXXX")"
    cp "${deb_file}" "${context_dir}/package.deb"

    # Buildx is the cross-arch execution environment. If this image build
    # succeeds, every DEB install and smoke-test RUN step passed.
    cat > "${context_dir}/Dockerfile" <<'EOF'
ARG TEST_IMAGE=ubuntu:22.04
FROM ${TEST_IMAGE}

ARG PACKAGE_NAME
ENV DEBIAN_FRONTEND=noninteractive
COPY package.deb /pkg/package.deb

RUN set -eux; \
    dpkg-deb -I /pkg/package.deb; \
    dpkg-deb -c /pkg/package.deb; \
    mkdir -p /tmp/package-contents; \
    dpkg-deb -x /pkg/package.deb /tmp/package-contents; \
    test -f /tmp/package-contents/etc/dcgm-exporter/1.x-compatibility-metrics.csv; \
    test -f /tmp/package-contents/etc/dcgm-exporter/dcp-metrics-included.csv; \
    test -f /tmp/package-contents/etc/dcgm-exporter/default-counters.csv; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates file binutils; \
    apt-get install -y /pkg/package.deb; \
    dpkg -s "${PACKAGE_NAME}"; \
    dpkg -L "${PACKAGE_NAME}" | sort; \
    test -x /usr/bin/dcgm-exporter; \
    test -f /etc/dcgm-exporter/1.x-compatibility-metrics.csv; \
    test -f /etc/dcgm-exporter/dcp-metrics-included.csv; \
    test -f /etc/dcgm-exporter/default-counters.csv; \
    test -f /lib/systemd/system/nvidia-dcgm-exporter.service; \
    file /usr/bin/dcgm-exporter; \
    ldd /usr/bin/dcgm-exporter || true; \
    readelf -W -d /usr/bin/dcgm-exporter || true; \
    readelf -W --version-info /usr/bin/dcgm-exporter 2>/dev/null \
        | sed -n "/Name: /p" \
        | sort -u || true; \
    /usr/bin/dcgm-exporter --version
EOF

    docker_buildx_build \
        --pull \
        --progress=plain \
        --platform "${platform}" \
        --build-arg "TEST_IMAGE=${image}" \
        --build-arg "PACKAGE_NAME=${PACKAGE_NAME}" \
        "${context_dir}"
}

# validate_tarball builds and validates temporary packages for one package tarball.
validate_tarball() {
    local tarball="${1}"
    local tmpdir="${2}"
    local tarball_name
    local platform
    local rpm_arch
    local deb_arch
    local payload_dir
    local rpm_dir
    local deb_dir
    local rpm_file
    local deb_file
    local image
    local -a rpm_images=()
    local -a deb_images=()

    tarball_name="$(basename "${tarball}")"
    platform="$(infer_platform "${tarball_name}")"
    rpm_arch="$(rpm_arch_for_platform "${platform}")"
    deb_arch="$(deb_arch_for_platform "${platform}")"
    payload_dir="${tmpdir}/${tarball_name%.tar.gz}/payload"
    rpm_dir="${tmpdir}/${tarball_name%.tar.gz}/rpm"
    deb_dir="${tmpdir}/${tarball_name%.tar.gz}/deb"

    read -r -a rpm_images <<< "${RPM_TEST_IMAGES//$'\n'/ }"
    read -r -a deb_images <<< "${DEB_TEST_IMAGES//$'\n'/ }"
    [[ "${#rpm_images[@]}" -gt 0 ]] || die "RPM_TEST_IMAGES did not contain any images"
    [[ "${#deb_images[@]}" -gt 0 ]] || die "DEB_TEST_IMAGES did not contain any images"

    log_info "==> Unpacking ${tarball}"
    mkdir -p "${payload_dir}"
    tar -xzf "${tarball}" -C "${payload_dir}"

    if [[ ! -x "${payload_dir}/${PACKAGE_COMPONENT_DIR}/usr/bin/dcgm-exporter" ]]; then
        log_error "expected executable not found in package payload"
        diagnose_payload_binary "${payload_dir}/${PACKAGE_COMPONENT_DIR}/usr/bin/dcgm-exporter"
        return 1
    fi

    test -f "${payload_dir}/${PACKAGE_COMPONENT_DIR}/etc/dcgm-exporter/1.x-compatibility-metrics.csv"
    test -f "${payload_dir}/${PACKAGE_COMPONENT_DIR}/etc/dcgm-exporter/dcp-metrics-included.csv"
    test -f "${payload_dir}/${PACKAGE_COMPONENT_DIR}/etc/dcgm-exporter/default-counters.csv"
    local systemd_unit="${payload_dir}/${PACKAGE_COMPONENT_DIR}/lib/systemd/system/nvidia-dcgm-exporter.service"
    test -f "${systemd_unit}"
    if grep -Eq '^[[:space:]]*Standard(Output|Error)[[:space:]]*=[[:space:]]*(append:|file:|truncate:)?/var/log/dcgm-exporter\.log([[:space:]]|$)' "${systemd_unit}"; then
        die "Packaged systemd unit must log through journald"
    fi

    build_rpm "${platform}" "${rpm_arch}" "${payload_dir}" "${rpm_dir}"
    build_deb "${platform}" "${deb_arch}" "${payload_dir}" "${deb_dir}"

    rpm_file="$(find "${rpm_dir}/out" -type f -name '*.rpm' | sort | head -n1)"
    deb_file="$(find "${deb_dir}/out" -type f -name '*.deb' | sort | head -n1)"
    [[ -n "${rpm_file}" ]] || die "No RPM package was built for ${tarball_name}"
    [[ -n "${deb_file}" ]] || die "No DEB package was built for ${tarball_name}"

    for image in "${rpm_images[@]}"; do
        validate_rpm "${platform}" "${image}" "${rpm_file}"
    done

    for image in "${deb_images[@]}"; do
        validate_deb "${platform}" "${image}" "${deb_file}"
    done
}

# main parses options, resolves tarballs, and validates every package payload.
main() {
    local resolved_tarballs
    local tarball
    local tmpdir
    local -a tarballs=()

    flags "$@"

    require_command docker
    require_buildx
    [[ -n "${PACKAGE_COMPONENT_DIR}" ]] || die "PACKAGE_COMPONENT_DIR is required"
    [[ "${PACKAGE_COMPONENT_DIR}" != */* ]] || die "PACKAGE_COMPONENT_DIR must not contain slashes"
    [[ -d "${PACKAGE_WORK_ROOT}" ]] || die "PACKAGE_WORK_ROOT does not exist: ${PACKAGE_WORK_ROOT}"

    if ! resolved_tarballs="$(cd "${ROOT_DIR}" && resolve_tarballs "${PACKAGE_TARBALL_ARGS[@]}")"; then
        exit 1
    fi
    mapfile -t tarballs <<< "${resolved_tarballs}"
    [[ "${#tarballs[@]}" -gt 0 && -n "${tarballs[0]}" ]] \
        || die "No package tarballs found for: ${PACKAGE_TARBALLS}"

    tmpdir="$(mktemp -d "${PACKAGE_WORK_ROOT%/}/.test-package.XXXXXX")"
    PACKAGE_CLEANUP_DIR="${tmpdir}"
    trap cleanup EXIT

    log_info "Package name: ${PACKAGE_NAME}"
    log_info "Package version: ${PACKAGE_VERSION}-${PACKAGE_RELEASE}"

    for tarball in "${tarballs[@]}"; do
        validate_tarball "${tarball}" "${tmpdir}"
    done
}

main "$@"
