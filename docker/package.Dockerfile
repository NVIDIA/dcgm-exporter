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

# This image builds the host-package payload, not the dcgm-exporter runtime
# container. The resulting files are copied from /package-payload/dcgm_exporter
# into dist/dcgm_exporter-*.tar.gz for downstream packaging.
#
# Use UBI8 as the default build root so the host binary is compiled against the
# oldest supported Linux/glibc baseline. Runtime images such as distroless are
# intentionally not used here because this step needs a shell, Go, make, gcc,
# glibc-devel, and install tools. A downstream packaging step produces the final
# RPM/DEB packages; this Dockerfile only creates the tarball payload.
ARG PACKAGE_BUILDER_IMAGE=registry.access.redhat.com/ubi8/ubi:latest
FROM ${PACKAGE_BUILDER_IMAGE} AS package-builder

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

ARG GOLANG_VERSION=1.26.4

WORKDIR /go/src/github.com/NVIDIA/dcgm-exporter

RUN dnf install -y --setopt=install_weak_deps=False \
        ca-certificates \
        file \
        findutils \
        gcc \
        git \
        glibc-devel \
        gzip \
        make \
        tar \
        wget \
    && dnf clean all \
    && rm -rf /var/cache/dnf

# Copy cached Go compiler and modules for offline hermetic builds.
# In regular mode these directories exist but are empty (created by Makefile/CI).
COPY .go/compiler/ .go/compiler/
COPY .go/pkg/mod/ /go/pkg/mod/

RUN set -eux; \
    case "$(uname -m)" in \
        x86_64) goarch="amd64" ;; \
        aarch64) goarch="arm64" ;; \
        *) echo >&2 "unsupported package builder architecture: $(uname -m)"; exit 1 ;; \
    esac; \
    if [ -f ".go/compiler/go${GOLANG_VERSION}.linux-${goarch}.tar.gz" ]; then \
        echo "Using pre-cached Go compiler (hermetic build)"; \
        tar -C /usr/local -xzf ".go/compiler/go${GOLANG_VERSION}.linux-${goarch}.tar.gz"; \
    else \
        echo "Downloading Go compiler from dl.google.com"; \
        filename="go${GOLANG_VERSION}.linux-${goarch}.tar.gz"; \
        url="https://dl.google.com/go/${filename}"; \
        wget -O go.tgz "$url" --progress=dot:giga; \
        echo "Verifying SHA256 checksum..."; \
        wget -q -O go.sha256 "https://dl.google.com/go/${filename}.sha256"; \
        expected_sha256="$(cat go.sha256)"; \
        actual_sha256="$(sha256sum go.tgz | awk '{print $1}')"; \
        if [ "$expected_sha256" != "$actual_sha256" ]; then \
            echo >&2 "error: SHA256 checksum verification failed"; \
            echo >&2 "expected: $expected_sha256"; \
            echo >&2 "actual:   $actual_sha256"; \
            exit 1; \
        fi; \
        echo "SHA256 checksum verified successfully"; \
        rm go.sha256; \
        tar -C /usr/local -xzf go.tgz; \
        rm go.tgz; \
    fi

ENV GOTOOLCHAIN=local GOPATH=/go
ENV PATH=$GOPATH/bin:/usr/local/go/bin:$PATH
RUN mkdir -p "$GOPATH/src" "$GOPATH/bin" && chmod -R 700 "$GOPATH"

# Go module settings for hermetic builds. Credentialed GOPROXY values are
# consumed from a BuildKit secret during module download and are never persisted.
ARG GOPROXY_ENABLED
ARG GONOSUMDB
ARG GOSUMDB
ENV GONOSUMDB=${GONOSUMDB}
ENV GOSUMDB=${GOSUMDB}

# Download dependencies - skipped when pre-cached modules exist (hermetic build).
COPY go.mod go.sum ./
RUN --mount=type=secret,id=goproxy \
    set -eu; \
    if [ -s /run/secrets/goproxy ]; then \
        GOPROXY="$(cat /run/secrets/goproxy)"; \
        export GOPROXY; \
    fi; \
    set -x; \
    if [ -d "/go/pkg/mod" ] && [ "$(ls -A /go/pkg/mod 2>/dev/null)" ]; then \
        echo "Using pre-cached Go modules (hermetic build)"; \
    else \
        echo "Downloading Go modules..."; \
        go mod download; \
    fi

COPY cmd/ cmd/
COPY pkg/ pkg/
COPY internal/ internal/
COPY Makefile ./
COPY hack/ hack/
COPY etc/ etc/
COPY LICENSE ./
COPY packaging/config-files/systemd/nvidia-dcgm-exporter.service packaging/config-files/systemd/nvidia-dcgm-exporter.service

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    if [ "${GOPROXY_ENABLED:-}" = "true" ] && [ -d "/go/pkg/mod" ] && [ "$(ls -A /go/pkg/mod 2>/dev/null)" ]; then \
        export GOPROXY=off GOSUMDB=off GONOSUMDB='*'; \
    fi; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=1 CC=gcc \
    make stage-package-payload PACKAGE_PAYLOAD_ROOT=/package-payload

RUN file /package-payload/dcgm_exporter/usr/bin/dcgm-exporter

FROM scratch AS package-artifact

COPY --from=package-builder /package-payload /package-payload

CMD ["/package-payload/dcgm_exporter/usr/bin/dcgm-exporter"]
