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

"""Render canonical pins and their derived repository files."""

from __future__ import annotations

import importlib.util
import re
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path

from . import pins
from .writes import AnchoredUpdate, FencedUpdate


# These are destinations for derived version updates.
BAZEL_MODULE_PATH = "MODULE.bazel"
GO_WORKFLOW_PATH = ".github/workflows/go.yml"
GITLEAKS_WORKFLOW_PATH = ".github/workflows/gitleaks.yml"
RUNTIME_DOCKERFILE_PATH = "docker/Dockerfile"
DEVCONTAINER_DOCKERFILE_PATH = ".devcontainer/Dockerfile"
LOCAL_NODE_DOCKERFILE_PATH = "hack/e2e-local-node.Dockerfile"
CONTAINER_TEST_README_PATH = "tests/container/README.md"

# A file with this name in a top-level "*/ci/" directory may add update
# targets for files that exist only in some checkouts. See
# discover_target_modules.
TARGET_FILE_NAME = "version-sync-targets.py"


def render_versions_env_updates(
    original: str,
    updates: Mapping[str, str],
    *,
    source: str = "versions.env",
) -> str:
    """Render selected canonical values without altering layout or comments."""

    unknown = sorted(set(updates) - set(pins.REGISTRY))
    if unknown:
        raise pins.VersioningError(f"cannot update unknown keys: {', '.join(unknown)}")

    lines = original.splitlines(keepends=True)
    seen: set[str] = set()
    rendered: list[str] = []

    for lineno, raw in enumerate(lines, start=1):
        stripped = raw.strip()
        if not stripped or stripped.startswith("#") or "=" not in stripped:
            rendered.append(raw)
            continue

        key = stripped.split("=", 1)[0].strip()
        if key not in updates:
            rendered.append(raw)
            continue

        if key in seen:
            raise pins.VersioningError(f"{source}:{lineno}: duplicate key {key}")

        newline = "\n" if raw.endswith("\n") else ""
        rendered.append(f"{key}={updates[key]}{newline}")
        seen.add(key)

    missing = sorted(set(updates) - seen)
    if missing:
        raise pins.VersioningError(
            f"{source}: update keys not present: {', '.join(missing)}"
        )

    candidate = "".join(rendered)
    pins.parse_versions_env_text(candidate, source=source)
    return candidate


@dataclass(frozen=True)
class RenderContext:
    """Derived strings shared by file-rendering update groups."""

    full_version: str
    go_directive: str
    go_major_minor: str
    cuda_ubuntu_suffix: str
    k3d_node_ubuntu_suffix: str

    @classmethod
    def from_versions(cls, values: Mapping[str, str]) -> RenderContext:
        """Derive repeated render values once from the canonical registry."""

        return cls(
            full_version=f"{values['DCGM_VERSION']}-{values['EXPORTER_VERSION']}",
            go_directive=values["GO_VERSION"],
            go_major_minor=".".join(values["GO_VERSION"].split(".")[:2]),
            cuda_ubuntu_suffix=f"ubuntu{values['CUDA_UBUNTU_TAG']}",
            k3d_node_ubuntu_suffix=(f"ubuntu{values['K3D_NODE_BASE_UBUNTU_TAG']}"),
        )


def _go_toolchain_updates(
    values: Mapping[str, str],
    context: RenderContext,
) -> list[AnchoredUpdate]:
    """Render Go toolchain versions and pinned artifact checksums."""

    return [
        AnchoredUpdate(
            rel_path="go.mod",
            pattern=r"^go \d+\.\d+(?:\.\d+)?$",
            replacement=f"go {context.go_directive}",
            expect=1,
            desc="go.mod go directive",
        ),
    ]


def _mise_tool_updates(values: Mapping[str, str]) -> list[AnchoredUpdate]:
    """Render every Mise-managed tool pin from the canonical catalog."""

    tool_pins = (
        ("task", "TASK_VERSION", ""),
        ("bazel", "BAZEL_VERSION", ""),
        ("go", "GO_VERSION", ""),
        ("uv", "UV_VERSION", ""),
        ("golangci-lint", "GOLANGCI_LINT_VERSION", ""),
        ("gofumpt", "GOFUMPT_VERSION", ""),
        ('"go:golang.org/x/tools/cmd/goimports"', "GOIMPORTS_VERSION", "v"),
        (
            '"go:github.com/boumenot/gocover-cobertura"',
            "GOCOVER_COBERTURA_VERSION",
            "v",
        ),
        ("helm", "HELM_VERSION", ""),
        ("k3d", "K3D_VERSION", ""),
        ("kubectl", "KUBECTL_VERSION", ""),
        ("shellcheck", "SHELLCHECK_VERSION", ""),
        ("hadolint", "HADOLINT_VERSION", ""),
        ('"go:golang.org/x/vuln/cmd/govulncheck"', "GOVULNCHECK_VERSION", "v"),
        ("grype", "GRYPE_VERSION", ""),
        ("syft", "SYFT_VERSION", ""),
    )
    return [
        AnchoredUpdate(
            rel_path="mise.toml",
            pattern=rf"^({re.escape(tool)} = )\"[^\"]+\"$",
            replacement=rf'\g<1>"{prefix}{values[key].removeprefix("v")}"',
            expect=1,
            desc=f"mise.toml {key}",
        )
        for tool, key, prefix in tool_pins
    ]


def _bazel_updates(values: Mapping[str, str]) -> list[AnchoredUpdate]:
    """Render Bazel, module-rule, Go SDK, and Zig toolchain pins."""

    return [
        AnchoredUpdate(
            rel_path=".bazelversion",
            pattern=r"^\S+$",
            replacement=values["BAZEL_VERSION"],
            expect=1,
            desc=".bazelversion Bazel pin",
        ),
        AnchoredUpdate(
            rel_path=BAZEL_MODULE_PATH,
            pattern=r'^(bazel_dep\(name = "aspect_bazel_lib", version = ")[^"]+("\))$',
            replacement=rf'\g<1>{values["ASPECT_BAZEL_LIB_VERSION"]}\g<2>',
            expect=1,
            desc="MODULE.bazel aspect_bazel_lib version",
        ),
        AnchoredUpdate(
            rel_path="go.mod",
            pattern=r"^(\s*github[.]com/NVIDIA/go-dcgm v)\S+$",
            replacement=rf"\g<1>{values['GO_DCGM_BAZEL_VERSION']}",
            expect=1,
            desc="go.mod go-dcgm release-compatible version",
        ),
        AnchoredUpdate(
            rel_path=BAZEL_MODULE_PATH,
            pattern=r'^(bazel_dep\(name = "rules_go", version = ")[^"]+("\))$',
            replacement=rf'\g<1>{values["RULES_GO_VERSION"]}\g<2>',
            expect=1,
            desc="MODULE.bazel rules_go version",
        ),
        AnchoredUpdate(
            rel_path=BAZEL_MODULE_PATH,
            pattern=r'^(bazel_dep\(name = "gazelle", version = ")[^"]+("\))$',
            replacement=rf'\g<1>{values["GAZELLE_VERSION"]}\g<2>',
            expect=1,
            desc="MODULE.bazel Gazelle version",
        ),
        AnchoredUpdate(
            rel_path=BAZEL_MODULE_PATH,
            pattern=r'^(bazel_dep\(name = "rules_cc", version = ")[^"]+("\))$',
            replacement=rf'\g<1>{values["RULES_CC_VERSION"]}\g<2>',
            expect=1,
            desc="MODULE.bazel rules_cc version",
        ),
        AnchoredUpdate(
            rel_path=BAZEL_MODULE_PATH,
            pattern=r'^(bazel_dep\(name = "rules_pkg", version = ")[^"]+("\))$',
            replacement=rf'\g<1>{values["RULES_PKG_VERSION"]}\g<2>',
            expect=1,
            desc="MODULE.bazel rules_pkg version",
        ),
        AnchoredUpdate(
            rel_path=BAZEL_MODULE_PATH,
            pattern=r'^(bazel_dep\(name = "rules_python", version = ")[^"]+("\))$',
            replacement=rf'\g<1>{values["RULES_PYTHON_VERSION"]}\g<2>',
            expect=1,
            desc="MODULE.bazel rules_python version",
        ),
        AnchoredUpdate(
            rel_path=BAZEL_MODULE_PATH,
            pattern=r'^(bazel_dep\(name = "rules_shell", version = ")[^"]+("\))$',
            replacement=rf'\g<1>{values["RULES_SHELL_VERSION"]}\g<2>',
            expect=1,
            desc="MODULE.bazel rules_shell version",
        ),
        AnchoredUpdate(
            rel_path=BAZEL_MODULE_PATH,
            pattern=r'^(bazel_dep\(name = "platforms", version = ")[^"]+("\))$',
            replacement=rf'\g<1>{values["PLATFORMS_VERSION"]}\g<2>',
            expect=1,
            desc="MODULE.bazel platforms version",
        ),
    ]


def _github_action_updates(
    values: Mapping[str, str],
    context: RenderContext,
) -> list[AnchoredUpdate]:
    """Render immutable Action SHAs and readable tags."""

    return [
        AnchoredUpdate(
            rel_path=GO_WORKFLOW_PATH,
            pattern=r"^(\s*- uses: actions/checkout@)[0-9a-f]{40}( # v\S+)$",
            replacement=(
                rf"\g<1>{values['ACTIONS_CHECKOUT_SHA']} "
                f"# {values['ACTIONS_CHECKOUT_VERSION']}"
            ),
            expect=1,
            desc="GitHub Actions checkout SHA and version comment",
        ),
        AnchoredUpdate(
            rel_path=GITLEAKS_WORKFLOW_PATH,
            pattern=r"^(\s*- uses: actions/checkout@)[0-9a-f]{40}( # v\S+)$",
            replacement=(
                rf"\g<1>{values['ACTIONS_CHECKOUT_SHA']} "
                f"# {values['ACTIONS_CHECKOUT_VERSION']}"
            ),
            expect=1,
            desc="Gitleaks workflow checkout SHA and version comment",
        ),
        AnchoredUpdate(
            rel_path=GO_WORKFLOW_PATH,
            pattern=r"^(\s*uses: devcontainers/ci@)[0-9a-f]{40}( # v\S+)$",
            replacement=(
                rf"\g<1>{values['DEVCONTAINERS_CI_SHA']} "
                f"# {values['DEVCONTAINERS_CI_VERSION']}"
            ),
            expect=1,
            desc="Dev Container Action SHA and version comment",
        ),
        AnchoredUpdate(
            rel_path=GO_WORKFLOW_PATH,
            pattern=r"^(\s*uses: codecov/codecov-action@)[0-9a-f]{40}( # v\S+)$",
            replacement=(
                rf"\g<1>{values['CODECOV_ACTION_SHA']} "
                f"# {values['CODECOV_ACTION_VERSION']}"
            ),
            expect=1,
            desc="Codecov Action SHA and version comment",
        ),
    ]


def _gitleaks_workflow_updates(values: Mapping[str, str]) -> list[AnchoredUpdate]:
    """Render the gitleaks release and archive checksum the workflow installs."""

    return [
        AnchoredUpdate(
            rel_path=GITLEAKS_WORKFLOW_PATH,
            pattern=r'^(  GITLEAKS_VERSION: )"[^"]+"$',
            replacement=rf'\g<1>"{values["GITLEAKS_VERSION"]}"',
            expect=1,
            desc=f"{GITLEAKS_WORKFLOW_PATH} GITLEAKS_VERSION",
        ),
        AnchoredUpdate(
            rel_path=GITLEAKS_WORKFLOW_PATH,
            pattern=r'^(  GITLEAKS_SHA256: )"[^"]+"$',
            replacement=rf'\g<1>"{values["GITLEAKS_LINUX_AMD64_SHA256"]}"',
            expect=1,
            desc=f"{GITLEAKS_WORKFLOW_PATH} GITLEAKS_SHA256",
        ),
    ]


def _runtime_container_updates(
    values: Mapping[str, str],
) -> list[AnchoredUpdate]:
    """Render runtime image and native package pins."""

    return [
        AnchoredUpdate(
            rel_path=RUNTIME_DOCKERFILE_PATH,
            pattern=r'^([ \t]*DCGM_PACKAGE_VERSION="1:\$\{DCGM_VERSION\}-)\d+(".*)$',
            replacement=rf'\g<1>{values["DCGM_PACKAGE_REVISION"]}\g<2>',
            expect=1,
            desc="docker/Dockerfile DCGM native package revision",
        ),
        AnchoredUpdate(
            rel_path=RUNTIME_DOCKERFILE_PATH,
            pattern=r"^FROM --platform=\$TARGETARCH \S+ AS runtime-distroless-helper$",
            replacement=(
                f"FROM --platform=$TARGETARCH {values['CUDA_IMAGE_REPOSITORY']}"
                f"@{values['DISTROLESS_HELPER_CUDA_IMAGE_DIGEST']} "
                "AS runtime-distroless-helper"
            ),
            expect=1,
            desc="docker/Dockerfile runtime-distroless-helper FROM cuda digest",
        ),
        AnchoredUpdate(
            rel_path=RUNTIME_DOCKERFILE_PATH,
            pattern=r"^FROM --platform=\$TARGETARCH \S+ AS runtime-distroless-base$",
            replacement=(
                f"FROM --platform=$TARGETARCH {values['DISTROLESS_IMAGE_REPOSITORY']}"
                f"@{values['DISTROLESS_IMAGE_DIGEST']} "
                "AS runtime-distroless-base"
            ),
            expect=1,
            desc="docker/Dockerfile runtime-distroless-base FROM distroless/cc",
        ),
        AnchoredUpdate(
            rel_path=RUNTIME_DOCKERFILE_PATH,
            pattern=(
                r"^LABEL org[.]opencontainers[.]image[.]base[.]name="
                r'"nvcr[.]io/nvidia/distroless/cc:[^"]+"\n'
                r"^LABEL org[.]opencontainers[.]image[.]base[.]digest="
                r'"sha256:[0-9a-f]{64}"$'
            ),
            replacement=(
                'LABEL org.opencontainers.image.base.name="'
                f"{values['DISTROLESS_IMAGE_REPOSITORY']}:"
                f"{values['DISTROLESS_TAG']}"
                '"\nLABEL org.opencontainers.image.base.digest="'
                f"{values['DISTROLESS_IMAGE_DIGEST']}"
                '"'
            ),
            expect=1,
            desc="docker/Dockerfile runtime-distroless OCI base metadata",
        ),
    ]


def _local_container_updates(
    values: Mapping[str, str],
    context: RenderContext,
) -> list[AnchoredUpdate]:
    """Render images used by the devcontainer, e2e node, and package build."""

    return [
        AnchoredUpdate(
            rel_path=DEVCONTAINER_DOCKERFILE_PATH,
            pattern=r'^([ \t]*DCGM_PACKAGE_VERSION="1:\$\{DCGM_VERSION\}-)\d+(".*)$',
            replacement=rf'\g<1>{values["DCGM_PACKAGE_REVISION"]}\g<2>',
            expect=1,
            desc=".devcontainer/Dockerfile DCGM native package revision",
        ),
        AnchoredUpdate(
            rel_path=DEVCONTAINER_DOCKERFILE_PATH,
            pattern=r"^FROM \S+$",
            replacement=(
                f"FROM {values['CUDA_IMAGE_REPOSITORY']}:"
                f"{values['CUDA_BASE_TAG']}-{context.cuda_ubuntu_suffix}"
                f"@{values['CUDA_UBUNTU_IMAGE_DIGEST']}"
            ),
            expect=1,
            desc=".devcontainer/Dockerfile FROM cuda",
        ),
        AnchoredUpdate(
            rel_path=DEVCONTAINER_DOCKERFILE_PATH,
            pattern=r"^ARG DCGM_VERSION=\S+$",
            replacement=f"ARG DCGM_VERSION={values['DCGM_VERSION']}",
            expect=1,
            desc=".devcontainer/Dockerfile ARG DCGM_VERSION default",
        ),
        AnchoredUpdate(
            rel_path=LOCAL_NODE_DOCKERFILE_PATH,
            pattern=r"^ARG K3S_IMAGE=\S+$",
            replacement=(
                f"ARG K3S_IMAGE={values['K3S_IMAGE_REPOSITORY']}:"
                f"{values['K3S_VERSION']}@{values['K3S_IMAGE_DIGEST']}"
            ),
            expect=1,
            desc="hack/e2e-local-node.Dockerfile ARG K3S_IMAGE default",
        ),
        AnchoredUpdate(
            rel_path=LOCAL_NODE_DOCKERFILE_PATH,
            pattern=r"^ARG CUDA_IMAGE=\S+$",
            replacement=(
                f"ARG CUDA_IMAGE={values['CUDA_IMAGE_REPOSITORY']}:"
                f"{values['CUDA_BASE_TAG']}-{context.k3d_node_ubuntu_suffix}"
                f"@{values['K3D_NODE_CUDA_IMAGE_DIGEST']}"
            ),
            expect=1,
            desc="hack/e2e-local-node.Dockerfile ARG CUDA_IMAGE default",
        ),
        AnchoredUpdate(
            rel_path=LOCAL_NODE_DOCKERFILE_PATH,
            pattern=r"^ARG NVIDIA_CONTAINER_TOOLKIT_VERSION=\S+$",
            replacement=(
                "ARG NVIDIA_CONTAINER_TOOLKIT_VERSION="
                f"{values['NVIDIA_CONTAINER_TOOLKIT_VERSION']}"
            ),
            expect=1,
            desc=(
                "hack/e2e-local-node.Dockerfile ARG "
                "NVIDIA_CONTAINER_TOOLKIT_VERSION default"
            ),
        ),
        AnchoredUpdate(
            rel_path="docker/package-validation.Dockerfile",
            pattern=r"^ARG PACKAGE_VALIDATOR_IMAGE=\S+$",
            replacement=(
                f"ARG PACKAGE_VALIDATOR_IMAGE={values['UBI8_IMAGE_REPOSITORY']}"
                f"@{values['UBI8_IMAGE_DIGEST']}"
            ),
            expect=1,
            desc="docker/package-validation.Dockerfile UBI8 validator image",
        ),
    ]


def _deployment_updates(
    values: Mapping[str, str],
    context: RenderContext,
) -> list[AnchoredUpdate]:
    """Render Helm and raw Kubernetes release references."""

    return [
        AnchoredUpdate(
            rel_path="deployment/Chart.yaml",
            pattern=r'^version: "[^"]+"$',
            replacement=f'version: "{values["EXPORTER_VERSION"]}"',
            expect=1,
            desc="deployment/Chart.yaml version",
        ),
        AnchoredUpdate(
            rel_path="deployment/Chart.yaml",
            pattern=r'^appVersion: "[^"]+"$',
            replacement=f'appVersion: "{values["EXPORTER_VERSION"]}"',
            expect=1,
            desc="deployment/Chart.yaml appVersion",
        ),
        AnchoredUpdate(
            rel_path="deployment/values.yaml",
            pattern=r"^(  repository: )\S+$",
            replacement=rf"\g<1>{values['EXPORTER_IMAGE_REPOSITORY']}",
            expect=1,
            desc="deployment/values.yaml image repository",
        ),
        AnchoredUpdate(
            rel_path="deployment/values.yaml",
            pattern=r"^(  tag: )\S+$",
            replacement=rf"\g<1>{values['EXPORTER_VERSION']}",
            expect=1,
            desc="deployment/values.yaml image tag",
        ),
        AnchoredUpdate(
            rel_path="deployment/examples/dcgm-exporter.yaml",
            pattern=r'^(\s*app\.kubernetes\.io/version: )"[^"]+"$',
            replacement=rf'\g<1>"{values["EXPORTER_VERSION"]}"',
            expect=3,
            desc="dcgm-exporter.yaml app.kubernetes.io/version labels",
        ),
        AnchoredUpdate(
            rel_path="deployment/examples/dcgm-exporter.yaml",
            pattern=r'^(\s*- image: ")[^"]+("$)',
            replacement=(
                rf"\g<1>{values['EXPORTER_IMAGE_REPOSITORY']}:"
                f"{values['EXPORTER_VERSION']}"
                r"\g<2>"
            ),
            expect=1,
            desc="dcgm-exporter.yaml image tag",
        ),
        AnchoredUpdate(
            rel_path="deployment/examples/service-monitor.yaml",
            pattern=r'^(\s*app\.kubernetes\.io/version: )"[^"]+"$',
            replacement=rf'\g<1>"{values["EXPORTER_VERSION"]}"',
            expect=1,
            desc="service-monitor.yaml app.kubernetes.io/version labels",
        ),
    ]


def _documentation_updates(
    values: Mapping[str, str],
    context: RenderContext,
) -> list[FencedUpdate]:
    """Render intentionally exact version examples inside documentation fences."""

    tests_docker_run_example = (
        "```bash\n"
        f"docker run --rm --gpus all --cap-add SYS_ADMIN "
        f"{values['EXPORTER_IMAGE_REPOSITORY']}:{values['EXPORTER_VERSION']}\n"
        "```"
    )
    distroless_tag_examples = (
        "- `${EXPORTER_VERSION}`, the tag to use "
        f"(for example, `{values['EXPORTER_VERSION']}`)\n"
        "- `latest` as the moving alias\n"
        "- `${DCGM_VERSION}-${EXPORTER_VERSION}-distroless` and\n"
        "  `${DCGM_VERSION}-${EXPORTER_VERSION}`, legacy aliases kept for existing\n"
        f"  references (for example, `{context.full_version}-distroless`)"
    )

    return [
        FencedUpdate(
            rel_path="RELEASE.md",
            name="release-artifact-example",
            body=(
                f"For `EXPORTER_VERSION={values['EXPORTER_VERSION']}` and "
                "`CI_PIPELINE_IID=1234`, the package artifacts are:\n\n"
                "```text\n"
                f"dcgm_exporter-linux-x86_64-{values['EXPORTER_VERSION']}.1234.tar.gz\n"
                f"dcgm_exporter-linux-sbsa-{values['EXPORTER_VERSION']}.1234.tar.gz\n"
                f"RPM/DEB package version: {values['EXPORTER_VERSION']}.1234-1\n"
                "```"
            ),
            desc="RELEASE.md package artifact example",
        ),
        FencedUpdate(
            rel_path="RELEASE.md",
            name="distroless-tag-examples",
            body=distroless_tag_examples,
            desc="RELEASE.md distroless tag examples",
        ),
        FencedUpdate(
            rel_path=CONTAINER_TEST_README_PATH,
            name="image-tags",
            body=(
                f"- `{values['EXPORTER_DOCKER_HUB_IMAGE_REPOSITORY']}:"
                f"{values['EXPORTER_VERSION']}`"
            ),
            desc="tests/container/README.md image tag list",
        ),
        FencedUpdate(
            rel_path=CONTAINER_TEST_README_PATH,
            name="version-example",
            body=(
                f"| `VERSION` | `{values['EXPORTER_VERSION']}` | Exporter version, "
                "used as the image tag (read from hack/versions.env) |"
            ),
            desc="tests/container/README.md VERSION table row",
        ),
        FencedUpdate(
            rel_path=CONTAINER_TEST_README_PATH,
            name="docker-run-example",
            body=tests_docker_run_example,
            desc="tests/container/README.md docker run example",
        ),
        FencedUpdate(
            rel_path=CONTAINER_TEST_README_PATH,
            name="dcgm-gpu-check-example",
            body=(
                f"DCGM_IMAGE={values['DCGM_E2E_IMAGE_REPOSITORY']}:"
                f"{values['DCGM_VERSION']}-{values['DCGM_IMAGE_TAG_SUFFIX']}"
                f"@{values['DCGM_E2E_IMAGE_DIGEST']} \\\n"
                "EXPORTER_DISTROLESS_IMAGE="
                "my-registry.io/dcgm-exporter:custom-distroless \\\n"
                "go test --tags=container ./tests/container -v \\\n"
                "  -args --ginkgo.label-filter=remoteDcgmUri"
            ),
            desc="tests/container/README.md DCGM GPU-check example",
        ),
        FencedUpdate(
            rel_path=CONTAINER_TEST_README_PATH,
            name="cuda-gpu-check-example",
            body=(
                f"docker run --rm --gpus all {values['CUDA_IMAGE_REPOSITORY']}:"
                f"{values['CUDA_BASE_TAG']}-ubuntu{values['CUDA_UBUNTU_TAG']}"
                f"@{values['CUDA_UBUNTU_IMAGE_DIGEST']} nvidia-smi"
            ),
            desc="tests/container/README.md CUDA GPU-check example",
        ),
    ]


def _bazel_fenced_updates(values: Mapping[str, str]) -> list[FencedUpdate]:
    """Render multi-line Bazel toolchain regions from canonical pins."""

    zig_version = values["ZIG_VERSION"]
    return [
        FencedUpdate(
            rel_path=BAZEL_MODULE_PATH,
            name="go-dcgm-bazel-module",
            body=(
                "bazel_dep(\n"
                '    name = "go_dcgm",\n'
                f'    version = "{values["GO_DCGM_BAZEL_VERSION"]}",\n'
                '    repo_name = "com_github_nvidia_go_dcgm",\n'
                ")\n\n"
                "git_override(\n"
                '    module_name = "go_dcgm",\n'
                f'    commit = "{values["GO_DCGM_BAZEL_COMMIT"]}",\n'
                f'    remote = "{values["GO_DCGM_BAZEL_REPOSITORY"]}",\n'
                ")"
            ),
            desc="MODULE.bazel go-dcgm native module source",
        ),
        FencedUpdate(
            rel_path=BAZEL_MODULE_PATH,
            name="zig-toolchain",
            body=(
                "http_archive(\n"
                '    name = "zig_linux_amd64",\n'
                "    build_file_content = ZIG_BUILD_FILE,\n"
                f'    sha256 = "{values["ZIG_LINUX_AMD64_SHA256"]}",\n'
                f'    strip_prefix = "zig-x86_64-linux-{zig_version}",\n'
                f'    urls = ["https://ziglang.org/download/{zig_version}/zig-x86_64-linux-{zig_version}.tar.xz"],\n'
                ")\n\n"
                "http_archive(\n"
                '    name = "zig_linux_arm64",\n'
                "    build_file_content = ZIG_BUILD_FILE,\n"
                f'    sha256 = "{values["ZIG_LINUX_ARM64_SHA256"]}",\n'
                f'    strip_prefix = "zig-aarch64-linux-{zig_version}",\n'
                f'    urls = ["https://ziglang.org/download/{zig_version}/zig-aarch64-linux-{zig_version}.tar.xz"],\n'
                ")"
            ),
            desc="MODULE.bazel Zig repositories",
        ),
        FencedUpdate(
            rel_path=BAZEL_MODULE_PATH,
            name="go-sdk-download",
            body=(
                "    sdks = {\n"
                '        "linux_amd64": [\n'
                f'            "go{values["GO_VERSION"]}.linux-amd64.tar.gz",\n'
                f'            "{values["GO_LINUX_AMD64_SHA256"]}",\n'
                "        ],\n"
                '        "linux_arm64": [\n'
                f'            "go{values["GO_VERSION"]}.linux-arm64.tar.gz",\n'
                f'            "{values["GO_LINUX_ARM64_SHA256"]}",\n'
                "        ],\n"
                "    },\n"
                f'    version = "{values["GO_VERSION"]}",'
            ),
            desc="MODULE.bazel Go SDK downloads",
        ),
    ]


def discover_target_modules(repo_root: Path) -> list[Path]:
    """Find the version-sync-targets.py files in top-level "*/ci/" directories.

    Each module defines updates(values) and returns AnchoredUpdate items for
    files that the module sits next to. A checkout without such modules simply
    has no extra targets. Only this fixed depth is searched, so a nested
    checkout inside the repository is never picked up.
    """

    return sorted(repo_root.glob(f"*/ci/{TARGET_FILE_NAME}"))


def _load_module_updates(
    repo_root: Path,
    path: Path,
    index: int,
    values: Mapping[str, str],
) -> list[AnchoredUpdate]:
    """Load one target module, run its updates(values), and check the result."""

    name = path.relative_to(repo_root)

    try:
        spec = importlib.util.spec_from_file_location(
            f"version_sync_targets_{index}", path
        )
        if spec is None or spec.loader is None:
            raise pins.VersioningError(f"{name}: cannot load")

        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)

        if not callable(getattr(module, "updates", None)):
            raise pins.VersioningError(f"{name}: missing updates(values)")

        offered = list(module.updates(values))
    except pins.VersioningError:
        raise
    except Exception as error:
        raise pins.VersioningError(f"{name}: {error!r}") from error

    root = repo_root.resolve()
    for update in offered:
        if not isinstance(update, AnchoredUpdate):
            raise pins.VersioningError(
                f"{name}: updates(values) returned an item that is not an "
                f"AnchoredUpdate: {update!r}"
            )
        target = (root / update.rel_path).resolve()
        if Path(update.rel_path).is_absolute() or not target.is_relative_to(root):
            raise pins.VersioningError(
                f"{name}: update path {update.rel_path!r} is outside the repository"
            )

    return offered


def _module_updates(
    repo_root: Path,
    values: Mapping[str, str],
) -> list[AnchoredUpdate]:
    """Collect the updates offered by every discovered target module."""

    collected: list[AnchoredUpdate] = []
    for index, path in enumerate(discover_target_modules(repo_root)):
        collected.extend(_load_module_updates(repo_root, path, index, values))
    return collected


def build_updates(
    values: dict[str, str],
    repo_root: Path,
) -> tuple[list[AnchoredUpdate], list[FencedUpdate]]:
    """Build every derived-file replacement from canonical version values."""

    context = RenderContext.from_versions(values)

    anchored = [
        *_go_toolchain_updates(values, context),
        *_mise_tool_updates(values),
        *_bazel_updates(values),
        *_github_action_updates(values, context),
        *_gitleaks_workflow_updates(values),
        *_runtime_container_updates(values),
        *_local_container_updates(values, context),
        *_deployment_updates(values, context),
        *_module_updates(repo_root, values),
    ]

    return anchored, [
        *_documentation_updates(values, context),
        *_bazel_fenced_updates(values),
    ]
