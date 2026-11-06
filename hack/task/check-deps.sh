#!/usr/bin/env bash
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.

# Verify checked-in tool pins, the locked Mise selection, and installed tool availability without updating them.

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
versions_file="${root_dir}/hack/versions.env"
mise_file="${root_dir}/mise.toml"
bazel_version_file="${root_dir}/.bazelversion"

# shellcheck disable=SC1090
source "${versions_file}"

# Reads one pin from the repository version file for development-tool consistency checks.
version() {
    local key="$1"
    awk -F= -v key="${key}" '$1 == key {print substr($0, index($0, "=") + 1)}' "${versions_file}"
}

# Requires the Mise tool declaration to match its repository version pin.
require_tool() {
    local name="$1"
    local expected="$2"
    if ! grep -Fqx "${name} = \"${expected}\"" "${mise_file}"; then
        echo "error: ${mise_file} must pin ${name} to ${expected}" >&2
        exit 1
    fi
}

[[ -f "${root_dir}/mise.lock" ]] || {
    echo "error: mise.lock is missing; run task deps:lock" >&2
    exit 1
}

require_tool task "$(version TASK_VERSION)"
require_tool bazel "$(version BAZEL_VERSION)"
require_tool go "$(version GO_VERSION)"
require_tool uv "$(version UV_VERSION)"
require_tool golangci-lint "$(version GOLANGCI_LINT_VERSION)"
require_tool gofumpt "$(version GOFUMPT_VERSION)"
require_tool '"go:github.com/boumenot/gocover-cobertura"' "v$(version GOCOVER_COBERTURA_VERSION)"
require_tool helm "$(version HELM_VERSION)"
require_tool k3d "$(version K3D_VERSION)"
require_tool kubectl "${KUBECTL_VERSION#v}"
require_tool '"go:golang.org/x/tools/cmd/goimports"' "v$(version GOIMPORTS_VERSION)"
require_tool shellcheck "$(version SHELLCHECK_VERSION)"
require_tool hadolint "$(version HADOLINT_VERSION)"
require_tool '"go:golang.org/x/vuln/cmd/govulncheck"' "v$(version GOVULNCHECK_VERSION)"
require_tool grype "$(version GRYPE_VERSION)"
require_tool syft "$(version SYFT_VERSION)"

if [[ "$(<"${bazel_version_file}")" != "$(version BAZEL_VERSION)" ]]; then
    echo "error: ${bazel_version_file} must pin Bazel to $(version BAZEL_VERSION)" >&2
    exit 1
fi

mise --cd "${root_dir}" lock --locked --dry-run >/dev/null
if ! mise --cd "${root_dir}" install --locked --dry-run-code >/dev/null 2>&1; then
    echo "error: one or more development tools are missing; run task deps:install" >&2
    exit 1
fi

if [[ "$(mise --cd "${root_dir}" exec -- bazel --version)" != "bazel $(version BAZEL_VERSION)" ]]; then
    echo "error: Mise did not select Bazel $(version BAZEL_VERSION)" >&2
    exit 1
fi
