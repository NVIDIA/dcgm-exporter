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

"""Canonical dependency pins, schemas, and cross-pin validation."""

from __future__ import annotations

import re
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from enum import Enum
from pathlib import Path
from types import MappingProxyType


class VersioningError(ValueError):
    """A schema, anchor, or transactional-write error."""


class Source(str, Enum):
    """Upstream mechanism used to discover a registry entry's latest value."""

    GO_DEV = "go.dev"
    BCR = "bazel-central-registry"
    GITHUB_RELEASE = "github-release"
    GITHUB_TAG = "github-tag"
    GITHUB_REF = "github-ref"
    NVIDIA_HELM = "nvidia-helm-index"
    OCI_TAG = "oci-tag"
    OCI_DIGEST = "oci-digest"
    ARTIFACT = "official-artifact"
    DERIVED = "derived"
    MANUAL = "manual"


class UpdatePolicy(str, Enum):
    """How check/apply may treat a version or its companion value."""

    AUTOMATIC = "automatic"
    COMPANION = "companion"
    DERIVED = "derived"
    MANUAL = "manual"


SEMVER = r"v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?"
PLAIN_SEMVER = r"\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?"
# Fixed releases with a leading v use this three-part shape.
V_PREFIXED_SEMVER_PATTERN = r"v\d+\.\d+\.\d+"
# Ubuntu release tags use the YY.MM format.
UBUNTU_RELEASE_PATTERN = r"\d{2}\.\d{2}"
# Ubuntu package pins share this supported-release policy.
UBUNTU_PACKAGE_TARGET_RATIONALE = (
    "Supported Ubuntu package targets are an explicit compatibility matrix."
)
SHA256 = r"[0-9a-f]{64}"
DIGEST = rf"sha256:{SHA256}"
GIT_SHA = r"[0-9a-f]{40}"
HTTPS_URL = r"https://[^\s]+"
OCI_REPOSITORY = (
    r"[a-z0-9]+(?:[.-][a-z0-9]+)*(?::\d+)?"
    r"(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+"
)


@dataclass(frozen=True)
class VersionSpec:
    """Describe one versions.env key and how the CLI maintains it.

    ``source`` and ``source_id`` select the freshness resolver.
    ``update_policy`` controls whether apply may change the value, while
    ``companions`` names checksums, digests, or SHAs that must move with it.
    """

    key: str
    display_name: str
    category: str
    pattern: str
    source: Source
    source_id: str = ""
    update_policy: UpdatePolicy = UpdatePolicy.AUTOMATIC
    rationale: str = ""
    companions: tuple[str, ...] = ()

    def validate(self, value: str) -> None:
        """Reject a value that does not match this entry's schema."""

        if not re.fullmatch(self.pattern, value):
            raise VersioningError(
                f"{self.key} has malformed value {value!r}; expected /{self.pattern}/"
            )


# The short constructor alias keeps the large declarative registry scannable.
_spec = VersionSpec


def _repository_spec(key: str, display_name: str) -> VersionSpec:
    """Build a manual image-repository entry with the shared rationale."""

    return VersionSpec(
        key,
        display_name,
        "image-repositories",
        OCI_REPOSITORY,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "Image repository locations change only with an explicit registry "
            "or mirror policy decision."
        ),
    )


# Registry groups make the schema reviewable without changing versions.env order.
GO_SPECS: tuple[VersionSpec, ...] = (
    _spec(
        "GO_VERSION",
        "Go toolchain",
        "go",
        PLAIN_SEMVER,
        Source.GO_DEV,
        companions=("GO_LINUX_AMD64_SHA256", "GO_LINUX_ARM64_SHA256"),
    ),
    _spec(
        "GO_LINUX_AMD64_SHA256",
        "Go Linux amd64 checksum",
        "checksums",
        SHA256,
        Source.ARTIFACT,
        "https://dl.google.com/go/go{GO_VERSION}.linux-amd64.tar.gz.sha256",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "GO_LINUX_ARM64_SHA256",
        "Go Linux arm64 checksum",
        "checksums",
        SHA256,
        Source.ARTIFACT,
        "https://dl.google.com/go/go{GO_VERSION}.linux-arm64.tar.gz.sha256",
        UpdatePolicy.COMPANION,
    ),
)

UV_SPECS: tuple[VersionSpec, ...] = (
    _spec(
        "UV_VERSION", "uv", "tools", PLAIN_SEMVER, Source.GITHUB_RELEASE, "astral-sh/uv"
    ),
)

BAZEL_SPECS: tuple[VersionSpec, ...] = (
    _spec(
        "BAZEL_VERSION",
        "Bazel",
        "build-tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "bazelbuild/bazel",
    ),
    _spec(
        "RULES_GO_VERSION",
        "rules_go",
        "bazel-modules",
        PLAIN_SEMVER,
        Source.BCR,
        "rules_go",
    ),
    _spec(
        "ASPECT_BAZEL_LIB_VERSION",
        "aspect_bazel_lib",
        "bazel-modules",
        PLAIN_SEMVER,
        Source.BCR,
        "aspect_bazel_lib",
    ),
    _spec(
        "GAZELLE_VERSION",
        "Gazelle",
        "bazel-modules",
        PLAIN_SEMVER,
        Source.BCR,
        "gazelle",
    ),
    _spec(
        "RULES_CC_VERSION",
        "rules_cc",
        "bazel-modules",
        PLAIN_SEMVER,
        Source.BCR,
        "rules_cc",
    ),
    _spec(
        "RULES_PKG_VERSION",
        "rules_pkg",
        "bazel-modules",
        PLAIN_SEMVER,
        Source.BCR,
        "rules_pkg",
    ),
    _spec(
        "RULES_PYTHON_VERSION",
        "rules_python",
        "bazel-modules",
        PLAIN_SEMVER,
        Source.BCR,
        "rules_python",
    ),
    _spec(
        "RULES_SHELL_VERSION",
        "rules_shell",
        "bazel-modules",
        PLAIN_SEMVER,
        Source.BCR,
        "rules_shell",
    ),
    _spec(
        "PLATFORMS_VERSION",
        "Bazel platforms",
        "bazel-modules",
        PLAIN_SEMVER,
        Source.BCR,
        "platforms",
    ),
    _spec(
        "GO_DCGM_BAZEL_VERSION",
        "go-dcgm Bazel module",
        "bazel-modules",
        PLAIN_SEMVER,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "Update the release-compatible Go version and immutable Bazel "
            "source commit together after the go-dcgm Bazel module is "
            "published upstream."
        ),
        companions=("GO_DCGM_BAZEL_COMMIT", "GO_DCGM_BAZEL_REPOSITORY"),
    ),
    _spec(
        "GO_DCGM_BAZEL_COMMIT",
        "go-dcgm Bazel module commit",
        "bazel-modules",
        GIT_SHA,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "The Bazel module must remain pinned to an immutable source "
            "commit that contains its MODULE.bazel and BUILD targets."
        ),
    ),
    _spec(
        "GO_DCGM_BAZEL_REPOSITORY",
        "go-dcgm Bazel module repository",
        "bazel-modules",
        HTTPS_URL,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "Public and release builds use the upstream repository; CI may "
            "use a local module override for unreleased commits."
        ),
    ),
    _spec(
        "ZIG_VERSION",
        "Zig cross compiler",
        "build-tools",
        PLAIN_SEMVER,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale="Zig upgrades require explicit CGO ABI and cross-build validation.",
    ),
    _spec(
        "ZIG_LINUX_AMD64_SHA256",
        "Zig Linux amd64 checksum",
        "checksums",
        SHA256,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale="The checksum is pinned from Zig's signed release index.",
    ),
    _spec(
        "ZIG_LINUX_ARM64_SHA256",
        "Zig Linux arm64 checksum",
        "checksums",
        SHA256,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale="The checksum is pinned from Zig's signed release index.",
    ),
    _spec(
        "PACKAGE_GLIBC_VERSION",
        "Package glibc compatibility floor",
        "build-policy",
        r"\d+\.\d+",
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale="The package ABI floor follows the oldest supported package runtime.",
    ),
)

KUBERNETES_SPECS: tuple[VersionSpec, ...] = (
    _spec(
        "K3D_VERSION",
        "k3d",
        "kubernetes",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "k3d-io/k3d",
        companions=("K3D_LINUX_AMD64_SHA256", "K3D_LINUX_ARM64_SHA256"),
    ),
    _spec(
        "K3S_VERSION",
        "K3s",
        "kubernetes",
        r"v\d+\.\d+\.\d+-k3s\d+",
        Source.GITHUB_RELEASE,
        "k3s-io/k3s",
        companions=("K3S_IMAGE_DIGEST",),
    ),
    _spec(
        "KUBECTL_VERSION",
        "kubectl",
        "kubernetes",
        V_PREFIXED_SEMVER_PATTERN,
        Source.DERIVED,
        update_policy=UpdatePolicy.DERIVED,
        rationale="kubectl is kept at the Kubernetes version embedded in K3S_VERSION.",
        companions=("KUBECTL_LINUX_AMD64_SHA256", "KUBECTL_LINUX_ARM64_SHA256"),
    ),
    _spec(
        "HELM_VERSION",
        "Helm",
        "kubernetes",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "helm/helm",
        companions=("HELM_LINUX_AMD64_SHA256", "HELM_LINUX_ARM64_SHA256"),
    ),
)

KUBERNETES_CHECKSUM_SPECS: tuple[VersionSpec, ...] = (
    _spec(
        "K3D_LINUX_AMD64_SHA256",
        "k3d Linux amd64 checksum",
        "checksums",
        SHA256,
        Source.ARTIFACT,
        "https://github.com/k3d-io/k3d/releases/download/v{K3D_VERSION}/checksums.txt",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "K3D_LINUX_ARM64_SHA256",
        "k3d Linux arm64 checksum",
        "checksums",
        SHA256,
        Source.ARTIFACT,
        "https://github.com/k3d-io/k3d/releases/download/v{K3D_VERSION}/checksums.txt",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "KUBECTL_LINUX_AMD64_SHA256",
        "kubectl Linux amd64 checksum",
        "checksums",
        SHA256,
        Source.ARTIFACT,
        "https://dl.k8s.io/release/{KUBECTL_VERSION}/bin/linux/amd64/kubectl.sha256",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "KUBECTL_LINUX_ARM64_SHA256",
        "kubectl Linux arm64 checksum",
        "checksums",
        SHA256,
        Source.ARTIFACT,
        "https://dl.k8s.io/release/{KUBECTL_VERSION}/bin/linux/arm64/kubectl.sha256",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "HELM_LINUX_AMD64_SHA256",
        "Helm Linux amd64 checksum",
        "checksums",
        SHA256,
        Source.ARTIFACT,
        "https://get.helm.sh/helm-v{HELM_VERSION}-linux-amd64.tar.gz.sha256sum",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "HELM_LINUX_ARM64_SHA256",
        "Helm Linux arm64 checksum",
        "checksums",
        SHA256,
        Source.ARTIFACT,
        "https://get.helm.sh/helm-v{HELM_VERSION}-linux-arm64.tar.gz.sha256sum",
        UpdatePolicy.COMPANION,
    ),
)

NVIDIA_KUBERNETES_SPECS: tuple[VersionSpec, ...] = (
    _spec(
        "NVIDIA_DEVICE_PLUGIN_VERSION",
        "NVIDIA device plugin",
        "kubernetes",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "NVIDIA/k8s-device-plugin",
    ),
    _spec(
        "NVIDIA_CONTAINER_TOOLKIT_VERSION",
        "NVIDIA Container Toolkit package",
        "kubernetes",
        r"\d+\.\d+\.\d+-\d+",
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "The nested k3d runtime is a tested compatibility component; update "
            "this package pin only after the local GPU e2e matrix passes."
        ),
    ),
    _spec(
        "GPU_OPERATOR_VERSION",
        "GPU Operator",
        "kubernetes",
        V_PREFIXED_SEMVER_PATTERN,
        Source.NVIDIA_HELM,
        "gpu-operator",
    ),
    _spec(
        "NVIDIA_DRA_DRIVER_VERSION",
        "NVIDIA DRA driver",
        "kubernetes",
        PLAIN_SEMVER,
        Source.NVIDIA_HELM,
        "nvidia-dra-driver-gpu",
    ),
)

TOOL_SPECS: tuple[VersionSpec, ...] = (
    _repository_spec("BUSYBOX_IMAGE_REPOSITORY", "BusyBox upstream repository"),
    _spec(
        "BUSYBOX_IMAGE_TAG",
        "BusyBox image",
        "images",
        PLAIN_SEMVER,
        Source.OCI_TAG,
        "{BUSYBOX_IMAGE_REPOSITORY}",
        companions=("BUSYBOX_IMAGE_DIGEST",),
    ),
    _spec(
        "BUSYBOX_IMAGE_DIGEST",
        "BusyBox manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{BUSYBOX_IMAGE_REPOSITORY}:{BUSYBOX_IMAGE_TAG}",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "TASK_VERSION",
        "Task",
        "tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "go-task/task",
    ),
    _spec(
        "GOLANGCI_LINT_VERSION",
        "golangci-lint",
        "go-tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "golangci/golangci-lint",
    ),
    _spec(
        "GOIMPORTS_VERSION",
        "goimports",
        "go-tools",
        PLAIN_SEMVER,
        Source.GITHUB_TAG,
        "golang/tools:v0.",
    ),
    _spec(
        "GOFUMPT_VERSION",
        "gofumpt",
        "go-tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "mvdan/gofumpt",
    ),
    _spec(
        "GOCOVER_COBERTURA_VERSION",
        "gocover-cobertura",
        "go-tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "boumenot/gocover-cobertura",
    ),
    _spec(
        "SHELLCHECK_VERSION",
        "ShellCheck",
        "tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "koalaman/shellcheck",
    ),
    _spec(
        "GOVULNCHECK_VERSION",
        "govulncheck",
        "go-tools",
        PLAIN_SEMVER,
        Source.GITHUB_TAG,
        "golang/vuln:v1.",
    ),
    _spec(
        "GRYPE_VERSION",
        "Grype",
        "tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "anchore/grype",
    ),
    _spec(
        "SYFT_VERSION",
        "Syft",
        "tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "anchore/syft",
    ),
    _spec(
        "HADOLINT_VERSION",
        "Hadolint",
        "tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "hadolint/hadolint",
    ),
    _spec(
        "GITLEAKS_VERSION",
        "Gitleaks",
        "tools",
        PLAIN_SEMVER,
        Source.GITHUB_RELEASE,
        "gitleaks/gitleaks",
        companions=("GITLEAKS_LINUX_AMD64_SHA256", "GITLEAKS_IMAGE_DIGEST"),
    ),
    _spec(
        "GITLEAKS_LINUX_AMD64_SHA256",
        "Gitleaks Linux amd64 checksum",
        "checksums",
        SHA256,
        Source.ARTIFACT,
        "https://github.com/gitleaks/gitleaks/releases/download/v{GITLEAKS_VERSION}/"
        "gitleaks_{GITLEAKS_VERSION}_checksums.txt",
        UpdatePolicy.COMPANION,
    ),
)

GITHUB_ACTION_SPECS: tuple[VersionSpec, ...] = (
    _spec(
        "ACTIONS_CHECKOUT_VERSION",
        "actions/checkout",
        "github-actions",
        V_PREFIXED_SEMVER_PATTERN,
        Source.GITHUB_REF,
        "actions/checkout",
        companions=("ACTIONS_CHECKOUT_SHA",),
    ),
    _spec(
        "ACTIONS_CHECKOUT_SHA",
        "actions/checkout commit",
        "github-actions",
        GIT_SHA,
        Source.GITHUB_REF,
        "actions/checkout:{ACTIONS_CHECKOUT_VERSION}",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "DEVCONTAINERS_CI_VERSION",
        "devcontainers/ci",
        "github-actions",
        V_PREFIXED_SEMVER_PATTERN,
        Source.GITHUB_REF,
        "devcontainers/ci",
        companions=("DEVCONTAINERS_CI_SHA",),
    ),
    _spec(
        "DEVCONTAINERS_CI_SHA",
        "devcontainers/ci commit",
        "github-actions",
        GIT_SHA,
        Source.GITHUB_REF,
        "devcontainers/ci:{DEVCONTAINERS_CI_VERSION}",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "CODECOV_ACTION_VERSION",
        "codecov/codecov-action",
        "github-actions",
        V_PREFIXED_SEMVER_PATTERN,
        Source.GITHUB_REF,
        "codecov/codecov-action",
        companions=("CODECOV_ACTION_SHA",),
    ),
    _spec(
        "CODECOV_ACTION_SHA",
        "codecov/codecov-action commit",
        "github-actions",
        GIT_SHA,
        Source.GITHUB_REF,
        "codecov/codecov-action:{CODECOV_ACTION_VERSION}",
        UpdatePolicy.COMPANION,
    ),
)

DOCKER_IMAGE_SPECS: tuple[VersionSpec, ...] = (
    _repository_spec("DOCKER_IMAGE_REPOSITORY", "Docker upstream repository"),
    _spec(
        "DOCKER_DIND_ROOTLESS_IMAGE_TAG",
        "Docker rootless DinD service alias",
        "images",
        r"dind-rootless",
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "CI runners can allow privileges only for the exact dind-rootless "
            "service alias; digest-qualified references start without the "
            "privileges rootlesskit requires."
        ),
    ),
)

CUDA_IMAGE_SPECS: tuple[VersionSpec, ...] = (
    _repository_spec("CUDA_IMAGE_REPOSITORY", "CUDA repository"),
    _spec(
        "CUDA_BASE_TAG",
        "CUDA base version",
        "images",
        r"\d+\.\d+\.\d+-base",
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "CUDA is selected together with DCGM support and the tested Ubuntu "
            "compatibility matrix."
        ),
    ),
    _spec(
        "CUDA_UBUNTU_TAG",
        "CUDA image Ubuntu release",
        "images",
        UBUNTU_RELEASE_PATTERN,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale="The devcontainer, the CUDA workload image and the container toolkit check image share this CUDA Ubuntu base.",
        companions=("CUDA_UBUNTU_IMAGE_DIGEST",),
    ),
    _spec(
        "CUDA_UBUNTU_IMAGE_DIGEST",
        "CUDA Ubuntu image manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{CUDA_IMAGE_REPOSITORY}:{CUDA_BASE_TAG}-ubuntu{CUDA_UBUNTU_TAG}",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "K3D_NODE_BASE_UBUNTU_TAG",
        "K3d node CUDA Ubuntu",
        "images",
        UBUNTU_RELEASE_PATTERN,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "The k3d node base must remain compatible with the K3s and NVIDIA "
            "container-toolkit matrix."
        ),
        companions=("K3D_NODE_CUDA_IMAGE_DIGEST",),
    ),
    _spec(
        "K3D_NODE_CUDA_IMAGE_DIGEST",
        "K3d node CUDA manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{CUDA_IMAGE_REPOSITORY}:{CUDA_BASE_TAG}-ubuntu{K3D_NODE_BASE_UBUNTU_TAG}",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "DISTROLESS_HELPER_UBUNTU_TAG",
        "Distroless helper CUDA Ubuntu",
        "images",
        UBUNTU_RELEASE_PATTERN,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "The helper stage matches the DCGM package distribution used by the "
            "distroless runtime."
        ),
        companions=("DISTROLESS_HELPER_CUDA_IMAGE_DIGEST",),
    ),
    _spec(
        "DISTROLESS_HELPER_CUDA_IMAGE_DIGEST",
        "Distroless helper CUDA digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{CUDA_IMAGE_REPOSITORY}:{CUDA_BASE_TAG}-ubuntu{DISTROLESS_HELPER_UBUNTU_TAG}",
        UpdatePolicy.COMPANION,
    ),
)

DISTROLESS_IMAGE_SPECS: tuple[VersionSpec, ...] = (
    _repository_spec("DISTROLESS_IMAGE_REPOSITORY", "NVIDIA distroless repository"),
    _spec(
        "DISTROLESS_TAG",
        "NVIDIA distroless",
        "images",
        V_PREFIXED_SEMVER_PATTERN,
        Source.OCI_TAG,
        "{DISTROLESS_IMAGE_REPOSITORY}",
        companions=("DISTROLESS_IMAGE_DIGEST",),
    ),
    _spec(
        "DISTROLESS_IMAGE_DIGEST",
        "NVIDIA distroless manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{DISTROLESS_IMAGE_REPOSITORY}:{DISTROLESS_TAG}",
        UpdatePolicy.COMPANION,
    ),
)

UBUNTU_IMAGE_SPECS: tuple[VersionSpec, ...] = (
    _repository_spec("UBUNTU_IMAGE_REPOSITORY", "Ubuntu upstream repository"),
    _spec(
        "BUILDER_UBUNTU_TAG",
        "Go builder Ubuntu",
        "images",
        UBUNTU_RELEASE_PATTERN,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "Builder Ubuntu changes are coordinated with cross-compilers, package "
            "tests, and release compatibility."
        ),
        companions=("UBUNTU_2604_IMAGE_TAG", "UBUNTU_2604_IMAGE_DIGEST"),
    ),
    _spec(
        "UBUNTU_2204_IMAGE_TAG",
        "Ubuntu 22.04 package target",
        "images",
        UBUNTU_RELEASE_PATTERN,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=UBUNTU_PACKAGE_TARGET_RATIONALE,
        companions=("UBUNTU_2204_IMAGE_DIGEST",),
    ),
    _spec(
        "UBUNTU_2204_IMAGE_DIGEST",
        "Ubuntu 22.04 manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{UBUNTU_IMAGE_REPOSITORY}:{UBUNTU_2204_IMAGE_TAG}",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "UBUNTU_2404_IMAGE_TAG",
        "Ubuntu 24.04 package target",
        "images",
        UBUNTU_RELEASE_PATTERN,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=UBUNTU_PACKAGE_TARGET_RATIONALE,
        companions=("UBUNTU_2404_IMAGE_DIGEST",),
    ),
    _spec(
        "UBUNTU_2404_IMAGE_DIGEST",
        "Ubuntu 24.04 manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{UBUNTU_IMAGE_REPOSITORY}:{UBUNTU_2404_IMAGE_TAG}",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "UBUNTU_2604_IMAGE_TAG",
        "Ubuntu 26.04 package target",
        "images",
        UBUNTU_RELEASE_PATTERN,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=UBUNTU_PACKAGE_TARGET_RATIONALE,
        companions=("UBUNTU_2604_IMAGE_DIGEST",),
    ),
    _spec(
        "UBUNTU_2604_IMAGE_DIGEST",
        "Ubuntu 26.04 manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{UBUNTU_IMAGE_REPOSITORY}:{UBUNTU_2604_IMAGE_TAG}",
        UpdatePolicy.COMPANION,
    ),
)

UBI_IMAGE_SPECS: tuple[VersionSpec, ...] = (
    _repository_spec("UBI8_IMAGE_REPOSITORY", "UBI 8 repository"),
    _spec(
        "UBI8_IMAGE_TAG",
        "UBI 8 package image",
        "images",
        r"latest|\d+(?:\.\d+)*",
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "UBI majors are package compatibility targets; moving tags are locked "
            "by their manifest digests."
        ),
        companions=("UBI8_IMAGE_DIGEST",),
    ),
    _spec(
        "UBI8_IMAGE_DIGEST",
        "UBI 8 manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{UBI8_IMAGE_REPOSITORY}:{UBI8_IMAGE_TAG}",
        UpdatePolicy.COMPANION,
    ),
    _repository_spec("UBI9_IMAGE_REPOSITORY", "UBI 9 repository"),
    _spec(
        "UBI9_IMAGE_TAG",
        "UBI 9 package image",
        "images",
        r"latest|\d+(?:\.\d+)*",
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "UBI majors are package compatibility targets; moving tags are locked "
            "by their manifest digests."
        ),
        companions=("UBI9_IMAGE_DIGEST",),
    ),
    _spec(
        "UBI9_IMAGE_DIGEST",
        "UBI 9 manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{UBI9_IMAGE_REPOSITORY}:{UBI9_IMAGE_TAG}",
        UpdatePolicy.COMPANION,
    ),
)

CI_IMAGE_SPECS: tuple[VersionSpec, ...] = (
    _repository_spec("BUILDKIT_IMAGE_REPOSITORY", "BuildKit upstream repository"),
    _spec(
        "BUILDKIT_IMAGE_TAG",
        "BuildKit helper",
        "images",
        V_PREFIXED_SEMVER_PATTERN,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "BuildKit upgrades are reviewed manually with the current CI "
            "runner/buildx stack and native amd64/arm64 image paths because output "
            "media-type defaults can affect manifest publication."
        ),
        companions=("BUILDKIT_IMAGE_DIGEST",),
    ),
    _spec(
        "BUILDKIT_IMAGE_DIGEST",
        "BuildKit manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{BUILDKIT_IMAGE_REPOSITORY}:{BUILDKIT_IMAGE_TAG}",
        UpdatePolicy.COMPANION,
    ),
    _repository_spec(
        "SONAR_SCANNER_IMAGE_REPOSITORY", "Sonar scanner upstream repository"
    ),
    _spec(
        "SONAR_SCANNER_IMAGE_TAG",
        "Sonar scanner",
        "images",
        r"\d+(?:\.\d+)*",
        Source.OCI_TAG,
        "{SONAR_SCANNER_IMAGE_REPOSITORY}",
        companions=("SONAR_SCANNER_IMAGE_DIGEST",),
    ),
    _spec(
        "SONAR_SCANNER_IMAGE_DIGEST",
        "Sonar scanner manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{SONAR_SCANNER_IMAGE_REPOSITORY}:{SONAR_SCANNER_IMAGE_TAG}",
        UpdatePolicy.COMPANION,
    ),
    _repository_spec("K3S_IMAGE_REPOSITORY", "K3s upstream repository"),
    _spec(
        "K3S_IMAGE_DIGEST",
        "K3s manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{K3S_IMAGE_REPOSITORY}:{K3S_VERSION}",
        UpdatePolicy.COMPANION,
    ),
    _repository_spec("GITLEAKS_IMAGE_REPOSITORY", "Gitleaks upstream repository"),
    _spec(
        "GITLEAKS_IMAGE_DIGEST",
        "Gitleaks manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{GITLEAKS_IMAGE_REPOSITORY}:v{GITLEAKS_VERSION}",
        UpdatePolicy.COMPANION,
    ),
)

RELEASE_SPECS: tuple[VersionSpec, ...] = (
    _spec(
        "DCGM_VERSION",
        "DCGM release identity",
        "release",
        PLAIN_SEMVER,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale="DCGM compatibility is an explicit exporter release decision.",
        companions=("DCGM_E2E_IMAGE_DIGEST",),
    ),
    _spec(
        "DCGM_PACKAGE_REVISION",
        "DCGM native package revision",
        "release",
        r"\d+",
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "Native package revisions are reviewed against the selected DCGM API "
            "and supported distro matrix."
        ),
    ),
    _repository_spec("DCGM_E2E_IMAGE_REPOSITORY", "DCGM e2e repository"),
    _spec(
        "DCGM_IMAGE_TAG_SUFFIX",
        "DCGM image package suffix",
        "release",
        r"(?:\d+-)?ubuntu\d{2}\.\d{2}",
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale=(
            "The DCGM distribution, and package revision when the image tag has one, "
            "are selected from the supported release matrix."
        ),
    ),
    _spec(
        "DCGM_E2E_IMAGE_DIGEST",
        "DCGM e2e manifest digest",
        "digests",
        DIGEST,
        Source.OCI_DIGEST,
        "{DCGM_E2E_IMAGE_REPOSITORY}:{DCGM_VERSION}-{DCGM_IMAGE_TAG_SUFFIX}",
        UpdatePolicy.COMPANION,
    ),
    _spec(
        "EXPORTER_VERSION",
        "Exporter release identity",
        "release",
        PLAIN_SEMVER,
        Source.MANUAL,
        update_policy=UpdatePolicy.MANUAL,
        rationale="Exporter version changes only as part of the release process.",
    ),
    _repository_spec("EXPORTER_IMAGE_REPOSITORY", "Exporter NGC repository"),
    _repository_spec(
        "EXPORTER_DOCKER_HUB_IMAGE_REPOSITORY", "Exporter Docker Hub repository"
    ),
)

# SPECS is the immutable public schema. Its order is also the required
# versions.env order, which keeps generated diffs predictable.
SPECS: tuple[VersionSpec, ...] = (
    *GO_SPECS,
    *BAZEL_SPECS,
    *UV_SPECS,
    *KUBERNETES_SPECS,
    *KUBERNETES_CHECKSUM_SPECS,
    *NVIDIA_KUBERNETES_SPECS,
    *TOOL_SPECS,
    *GITHUB_ACTION_SPECS,
    *DOCKER_IMAGE_SPECS,
    *CUDA_IMAGE_SPECS,
    *DISTROLESS_IMAGE_SPECS,
    *UBUNTU_IMAGE_SPECS,
    *UBI_IMAGE_SPECS,
    *CI_IMAGE_SPECS,
    *RELEASE_SPECS,
)


def _build_registry(specs: Sequence[VersionSpec]) -> Mapping[str, VersionSpec]:
    """Index ordered specifications and validate registry-only invariants."""

    result: dict[str, VersionSpec] = {}
    for spec in specs:
        if spec.key in result:
            raise VersioningError(f"duplicate registry key: {spec.key}")
        if spec.update_policy is UpdatePolicy.MANUAL and not spec.rationale:
            raise VersioningError(f"manual registry entry lacks rationale: {spec.key}")
        result[spec.key] = spec
    return MappingProxyType(result)


REGISTRY: Mapping[str, VersionSpec] = _build_registry(SPECS)


def _parse_versions_env_line(
    raw: str,
    *,
    source: str,
    lineno: int,
) -> tuple[str, str] | None:
    """Parse assignments and report syntax errors with the source location."""

    line = raw.strip()
    if not line or line.startswith("#"):
        return None

    if "=" not in line:
        raise VersioningError(f"{source}:{lineno}: malformed line (missing '=')")

    key, value = (part.strip() for part in line.split("=", 1))

    if not re.fullmatch(r"[A-Z][A-Z0-9_]*", key):
        raise VersioningError(f"{source}:{lineno}: invalid key {key!r}")

    return key, value


def parse_versions_env_text(
    content: str,
    *,
    source: str = "versions.env",
    registry: Mapping[str, VersionSpec] = REGISTRY,
) -> dict[str, str]:
    """Parse and fully validate a versions.env document."""

    values: dict[str, str] = {}

    for lineno, raw in enumerate(content.splitlines(), start=1):
        parsed = _parse_versions_env_line(raw, source=source, lineno=lineno)
        if parsed is None:
            continue

        key, value = parsed
        if key in values:
            raise VersioningError(f"{source}:{lineno}: duplicate key {key}")
        if key not in registry:
            raise VersioningError(f"{source}:{lineno}: unknown key {key}")
        if len(value) >= 2 and value[:1] == value[-1:] and value[0] in "'\"":
            value = value[1:-1]
        if not value:
            raise VersioningError(f"{source}:{lineno}: empty value for {key}")
        registry[key].validate(value)
        values[key] = value

    missing = [key for key in registry if key not in values]
    if missing:
        raise VersioningError(f"{source}: missing required keys: {', '.join(missing)}")

    validate_relationships(values, registry=registry)
    return values


def parse_versions_env(
    path: Path, *, registry: Mapping[str, VersionSpec] = REGISTRY
) -> dict[str, str]:
    """Load and validate the canonical versions file from disk."""

    if not path.is_file():
        raise VersioningError(f"{path}: file not found")
    return parse_versions_env_text(
        path.read_text(), source=str(path), registry=registry
    )


def _validate_kubernetes_versions(values: Mapping[str, str]) -> None:
    """Keep kubectl aligned with the Kubernetes release embedded in K3s."""

    if {"K3S_VERSION", "KUBECTL_VERSION"} <= values.keys():
        k3s_kubernetes = values["K3S_VERSION"].split("-", 1)[0]
        if values["KUBECTL_VERSION"] != k3s_kubernetes:
            raise VersioningError(
                "KUBECTL_VERSION must equal the Kubernetes version in K3S_VERSION"
            )


def _validate_distribution_versions(values: Mapping[str, str]) -> None:
    """Validate coupled Ubuntu selections used by images and packages."""

    if {"DCGM_IMAGE_TAG_SUFFIX", "DISTROLESS_HELPER_UBUNTU_TAG"} <= values.keys():
        expected_suffix = f"ubuntu{values['DISTROLESS_HELPER_UBUNTU_TAG']}"
        if not values["DCGM_IMAGE_TAG_SUFFIX"].endswith(expected_suffix):
            raise VersioningError(
                "DCGM_IMAGE_TAG_SUFFIX distribution must match "
                "DISTROLESS_HELPER_UBUNTU_TAG"
            )

    if {
        "BUILDER_UBUNTU_TAG",
        "UBUNTU_2604_IMAGE_TAG",
    } <= values.keys() and values["BUILDER_UBUNTU_TAG"] != values[
        "UBUNTU_2604_IMAGE_TAG"
    ]:
        raise VersioningError("BUILDER_UBUNTU_TAG must match UBUNTU_2604_IMAGE_TAG")


def _validate_companions(
    values: Mapping[str, str],
    registry: Mapping[str, VersionSpec],
) -> None:
    """Require checksums, digests, and SHAs declared by a primary entry."""

    for key, spec in registry.items():
        if key not in values:
            continue
        absent = [companion for companion in spec.companions if companion not in values]
        if absent:
            raise VersioningError(f"{key} requires companion keys: {', '.join(absent)}")


def validate_relationships(
    values: Mapping[str, str],
    *,
    registry: Mapping[str, VersionSpec] = REGISTRY,
) -> None:
    """Validate invariants that span multiple canonical version keys."""

    _validate_kubernetes_versions(values)
    _validate_distribution_versions(values)
    _validate_companions(values, registry)
