#!/usr/bin/env -S uv run --script
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

# /// script
# requires-python = ">=3.13"
# dependencies = [
#     "requests>=2.31",
#     "rich>=13.0",
#     "typer>=0.12",
# ]
# ///
"""Propagate hack/versions.env to every derived file that tracks it.

hack/versions.env is the source of truth. Files that can read it at runtime
(Makefiles via include, CI shell scripts via source) do so directly and are
not touched by this tool. Files that cannot -- go.mod's toolchain directive,
Dockerfile FROM lines and ARG defaults, optional CI configuration, Helm chart +
raw YAML artifacts, and fenced version examples in the two READMEs -- are kept
in sync by this script.

Usage:
    hack/sync-versions.py           # fetch Go SHA256s from dl.google.com, apply changes
    hack/sync-versions.py --check   # verify no drift against versions.env; exit 1 if any
    hack/sync-versions.py -v        # verbose: report every update + ok line

Exit codes:
    0 - in sync (nothing to do) or changes applied successfully
    1 - --check found drift (a derived file is out of sync with versions.env)
    2 - environment error: missing file, anchor count mismatch, missing fence,
        malformed versions.env, network failure
"""

from __future__ import annotations

import re
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Annotated

import requests
import typer
from rich.console import Console

console = Console(stderr=True, highlight=False)

# ==============================================================================
# Paths + exit codes
# ==============================================================================

SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parent
VERSIONS_ENV = SCRIPT_DIR / "versions.env"

EXIT_OK = 0
EXIT_DRIFT = 1
EXIT_ENV_ERROR = 2


# ==============================================================================
# versions.env parsing
# ==============================================================================


def parse_versions_env(path: Path) -> dict[str, str]:
    """Parse KEY=VALUE dotenv. Ignores comments and blank lines."""
    if not path.exists():
        console.print(f"[red]error:[/red] {path} not found")
        raise typer.Exit(EXIT_ENV_ERROR)
    result: dict[str, str] = {}
    for lineno, raw in enumerate(path.read_text().splitlines(), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            console.print(
                f"[red]error:[/red] {path}:{lineno}: malformed (no '='): {raw!r}"
            )
            raise typer.Exit(EXIT_ENV_ERROR)
        key, _, value = line.partition("=")
        key = key.strip()
        value = value.strip().strip('"').strip("'")
        if not re.match(r"^[A-Z][A-Z0-9_]*$", key):
            console.print(
                f"[red]error:[/red] {path}:{lineno}: invalid KEY {key!r}"
            )
            raise typer.Exit(EXIT_ENV_ERROR)
        result[key] = value
    return result


# ==============================================================================
# Update primitives
# ==============================================================================


@dataclass
class AnchoredUpdate:
    """Regex-based single-pattern replacement with an expected match count."""

    rel_path: str
    pattern: str  # raw regex; use capture groups for bits you want to keep
    replacement: str  # re.sub replacement string; supports \g<N> back-refs
    expect: int
    desc: str


@dataclass
class FencedUpdate:
    """Replace the body between `# sync:<name>:start` / `# sync:<name>:end`.

    Sentinel lines are preserved. The body is set exactly as given (no indent
    manipulation); the caller is responsible for matching surrounding indent.
    """

    rel_path: str
    name: str
    body: str  # final text; caller bakes in indentation
    desc: str


@dataclass
class SyncResult:
    drift_files: list[str] = field(default_factory=list)
    updated_files: list[str] = field(default_factory=list)


def _atomic_write(path: Path, content: str) -> None:
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(content)
    tmp.replace(path)


def apply_anchored(
    upd: AnchoredUpdate, *, mode: str, verbose: bool, result: SyncResult
) -> None:
    path = REPO_ROOT / upd.rel_path
    if not path.exists():
        console.print(f"[red]error:[/red] {upd.desc}: file not found: {path}")
        raise typer.Exit(EXIT_ENV_ERROR)

    original = path.read_text()
    matches = len(re.findall(upd.pattern, original, flags=re.MULTILINE))
    if matches != upd.expect:
        console.print(
            f"[red]error:[/red] {upd.desc}: anchor count mismatch "
            f"(got {matches}, expected {upd.expect}); pattern={upd.pattern!r}"
        )
        raise typer.Exit(EXIT_ENV_ERROR)

    new = re.sub(upd.pattern, upd.replacement, original, flags=re.MULTILINE)
    _finalize(upd.rel_path, upd.desc, original, new, mode, verbose, result)


def apply_fenced(
    upd: FencedUpdate, *, mode: str, verbose: bool, result: SyncResult
) -> None:
    path = REPO_ROOT / upd.rel_path
    if not path.exists():
        console.print(f"[red]error:[/red] {upd.desc}: file not found: {path}")
        raise typer.Exit(EXIT_ENV_ERROR)

    start_re = re.compile(
        rf"(?m)^(\s*(?:#|//|<!--) *sync:{re.escape(upd.name)}:start\b.*)$"
    )
    end_re = re.compile(
        rf"(?m)^(\s*(?:#|//|<!--) *sync:{re.escape(upd.name)}:end\b.*)$"
    )
    original = path.read_text()
    starts = start_re.findall(original)
    ends = end_re.findall(original)
    if len(starts) != 1 or len(ends) != 1:
        console.print(
            f"[red]error:[/red] {upd.desc}: fenced region "
            f"'sync:{upd.name}' must appear exactly once "
            f"(found {len(starts)} start, {len(ends)} end)"
        )
        raise typer.Exit(EXIT_ENV_ERROR)

    # Replace everything strictly between start and end lines.
    def _repl(match: re.Match[str]) -> str:
        start_line, end_line = match.group(1), match.group(2)
        body = upd.body
        # Ensure single trailing newline between body and end sentinel.
        if body and not body.endswith("\n"):
            body += "\n"
        return f"{start_line}\n{body}{end_line}"

    pattern = re.compile(
        rf"(?ms)^(\s*(?:#|//|<!--) *sync:{re.escape(upd.name)}:start\b[^\n]*)\n"
        rf".*?"
        rf"^(\s*(?:#|//|<!--) *sync:{re.escape(upd.name)}:end\b[^\n]*)"
    )
    new = pattern.sub(_repl, original, count=1)
    _finalize(upd.rel_path, upd.desc, original, new, mode, verbose, result)


def _finalize(
    rel_path: str,
    desc: str,
    original: str,
    new: str,
    mode: str,
    verbose: bool,
    result: SyncResult,
) -> None:
    if original == new:
        if verbose:
            console.print(f"[dim]ok:[/dim] {desc}")
        return
    if mode == "check":
        result.drift_files.append(rel_path)
        console.print(f"[yellow]DRIFT:[/yellow] {desc}")
        if verbose:
            _print_diff(original, new, rel_path)
        return
    _atomic_write(REPO_ROOT / rel_path, new)
    result.updated_files.append(rel_path)
    console.print(f"[green]updated:[/green] {desc}")


def _print_diff(original: str, new: str, rel_path: str) -> None:
    import difflib

    diff = difflib.unified_diff(
        original.splitlines(keepends=True),
        new.splitlines(keepends=True),
        fromfile=f"a/{rel_path}",
        tofile=f"b/{rel_path}",
        n=2,
    )
    console.print("".join(diff), end="")


# ==============================================================================
# Go tarball SHA256 fetch
# ==============================================================================


def fetch_go_sha256(go_version: str, arch: str) -> str:
    """Fetch the expected SHA256 for a Go tarball from dl.google.com."""
    url = f"https://dl.google.com/go/go{go_version}.linux-{arch}.tar.gz.sha256"
    try:
        r = requests.get(url, timeout=10)
        r.raise_for_status()
    except requests.RequestException as e:
        console.print(f"[red]error:[/red] fetch {url}: {e}")
        raise typer.Exit(EXIT_ENV_ERROR) from e
    sha = r.text.strip()
    if not re.fullmatch(r"[0-9a-f]{64}", sha):
        console.print(f"[red]error:[/red] {url} returned non-sha256: {sha!r}")
        raise typer.Exit(EXIT_ENV_ERROR)
    return sha


def resolve_go_sha256s(
    go_version: str, *, verbose: bool
) -> tuple[str, str]:
    """Fetch Go tarball SHA256s for both architectures."""
    if verbose:
        console.print(f"[dim]fetching Go {go_version} SHA256s from dl.google.com...[/dim]")
    sha_amd64 = fetch_go_sha256(go_version, "amd64")
    sha_arm64 = fetch_go_sha256(go_version, "arm64")
    return sha_amd64, sha_arm64


# ==============================================================================
# Derived-file target definitions
# ==============================================================================


def build_updates(
    v: dict[str, str], sha_amd64: str, sha_arm64: str
) -> tuple[list[AnchoredUpdate], list[FencedUpdate]]:
    """Return the list of all derived-file updates, formatted from versions.env."""
    full_ver = f"{v['DCGM_VERSION']}-{v['EXPORTER_VERSION']}"
    go_version_parts = v["GO_VERSION"].split(".")
    if len(go_version_parts) < 2 or not all(
        part.isdigit() for part in go_version_parts[:2]
    ):
        console.print(
            "[red]error:[/red] GO_VERSION must start with numeric major.minor, "
            f"got: {v['GO_VERSION']!r}"
        )
        raise typer.Exit(EXIT_ENV_ERROR)
    go_directive = ".".join(go_version_parts[:2]) + ".0"
    go_mod = (REPO_ROOT / "go.mod").read_text()
    if match := re.search(r"^go (\d+)\.(\d+)(?:\.(\d+))?$", go_mod, re.M):
        existing_parts = match.groups(default="0")
        if list(existing_parts[:2]) == go_version_parts[:2]:
            go_directive = ".".join(existing_parts)
    cuda_ubuntu_suffix = f"ubuntu{v['CUDA_UBUNTU_TAG']}"
    k3d_node_base_ubuntu_suffix = f"ubuntu{v['K3D_NODE_BASE_UBUNTU_TAG']}"
    distroless_helper_ubuntu_suffix = f"ubuntu{v['DISTROLESS_HELPER_UBUNTU_TAG']}"

    anchored: list[AnchoredUpdate] = [
        # --- Go toolchain ---
        AnchoredUpdate(
            rel_path="go.mod",
            pattern=r"^go \d+\.\d+(?:\.\d+)?$",
            replacement=f"go {go_directive}",
            expect=1,
            desc="go.mod go directive",
        ),
        AnchoredUpdate(
            rel_path="go.mod",
            pattern=r"^toolchain go\S+$",
            replacement=f"toolchain go{v['GO_VERSION']}",
            expect=1,
            desc="go.mod toolchain directive",
        ),
        AnchoredUpdate(
            rel_path="docker/Dockerfile",
            pattern=r"^ARG GOLANG_VERSION=\S+$",
            replacement=f"ARG GOLANG_VERSION={v['GO_VERSION']}",
            expect=1,
            desc="docker/Dockerfile ARG GOLANG_VERSION default",
        ),
        AnchoredUpdate(
            rel_path="docker/package.Dockerfile",
            pattern=r"^ARG GOLANG_VERSION=\S+$",
            replacement=f"ARG GOLANG_VERSION={v['GO_VERSION']}",
            expect=1,
            desc="docker/package.Dockerfile ARG GOLANG_VERSION default",
        ),
        AnchoredUpdate(
            rel_path=".devcontainer/Dockerfile",
            pattern=r"^ARG GOLANG_VERSION=\S+$",
            replacement=f"ARG GOLANG_VERSION={v['GO_VERSION']}",
            expect=1,
            desc=".devcontainer/Dockerfile ARG GOLANG_VERSION default",
        ),
        AnchoredUpdate(
            rel_path=".gitlab-ci.yml",
            pattern=r'^(  GOLANG_VERSION: )"[^"]+"$',
            replacement=rf'\g<1>"{v["GO_VERSION"]}"',
            expect=1,
            desc=".gitlab-ci.yml top-level GOLANG_VERSION",
        ),
        # --- Container base image tags ---
        AnchoredUpdate(
            rel_path="docker/Dockerfile",
            pattern=r"^(FROM --platform=\$TARGETARCH nvcr\.io/nvidia/cuda:)[^-\s]+-base-ubuntu\d+\.\d+( AS runtime-ubuntu)$",
            replacement=rf"\g<1>{v['CUDA_BASE_TAG']}-{cuda_ubuntu_suffix}\g<2>",
            expect=1,
            desc="docker/Dockerfile runtime-ubuntu FROM cuda:<tag>-ubuntu",
        ),
        AnchoredUpdate(
            rel_path="docker/Dockerfile",
            pattern=r"^(FROM --platform=\$TARGETARCH nvcr\.io/nvidia/cuda:)[^-\s]+-base-ubuntu\d+\.\d+( AS runtime-distroless-helper)$",
            replacement=rf"\g<1>{v['CUDA_BASE_TAG']}-{distroless_helper_ubuntu_suffix}\g<2>",
            expect=1,
            desc="docker/Dockerfile runtime-distroless-helper FROM cuda:<tag>-ubuntu",
        ),
        AnchoredUpdate(
            rel_path="docker/Dockerfile",
            pattern=r"^(FROM [^\n]*nvcr\.io/nvidia/distroless/cc:)\S+",
            replacement=rf"\g<1>{v['DISTROLESS_TAG']}",
            expect=1,
            desc="docker/Dockerfile FROM distroless/cc",
        ),
        AnchoredUpdate(
            rel_path="docker/Dockerfile",
            pattern=r"^ARG UBUNTU_IMAGE=ubuntu:\S+$",
            replacement=f"ARG UBUNTU_IMAGE=ubuntu:{v['BUILDER_UBUNTU_TAG']}",
            expect=1,
            desc="docker/Dockerfile ARG UBUNTU_IMAGE default",
        ),
        AnchoredUpdate(
            rel_path="Makefile",
            pattern=r"^(UBUNTU_IMAGE\s+\?= ubuntu:)\S+$",
            replacement=rf"\g<1>{v['BUILDER_UBUNTU_TAG']}",
            expect=1,
            desc="Makefile UBUNTU_IMAGE default",
        ),
        AnchoredUpdate(
            rel_path=".gitlab-ci.yml",
            pattern=r'^(  UBUNTU_IMAGE: )"([^"]*/)?ubuntu:[^"]+"$',
            replacement=rf'\g<1>"\g<2>ubuntu:{v["BUILDER_UBUNTU_TAG"]}"',
            expect=1,
            desc=".gitlab-ci.yml UBUNTU_IMAGE",
        ),
        AnchoredUpdate(
            rel_path=".gitlab-ci.yml",
            pattern=r'^(  BINFMT_IMAGE: )"([^"]*/)?tonistiigi/binfmt:[^"@]+(?:@sha256:[0-9a-f]+)?"$',
            replacement=rf'\g<1>"\g<2>tonistiigi/binfmt:{v["BINFMT_IMAGE_TAG"]}@{v["BINFMT_IMAGE_DIGEST"]}"',
            expect=1,
            desc=".gitlab-ci.yml BINFMT_IMAGE",
        ),
        AnchoredUpdate(
            rel_path=".gitlab-ci.yml",
            pattern=r'^(  BUILDKIT_IMAGE: )"([^"]*/)?moby/buildkit:[^"@]+(?:@sha256:[0-9a-f]+)?"$',
            replacement=rf'\g<1>"\g<2>moby/buildkit:{v["BUILDKIT_IMAGE_TAG"]}@{v["BUILDKIT_IMAGE_DIGEST"]}"',
            expect=1,
            desc=".gitlab-ci.yml BUILDKIT_IMAGE",
        ),
        AnchoredUpdate(
            rel_path=".gitlab-ci.yml",
            pattern=r'^(  SONAR_SCANNER_IMAGE: )"([^"]*/)?sonarsource/sonar-scanner-cli:[^"@]+(?:@sha256:[0-9a-f]+)?"$',
            replacement=rf'\g<1>"\g<2>sonarsource/sonar-scanner-cli:{v["SONAR_SCANNER_IMAGE_TAG"]}@{v["SONAR_SCANNER_IMAGE_DIGEST"]}"',
            expect=1,
            desc=".gitlab-ci.yml SONAR_SCANNER_IMAGE",
        ),
        AnchoredUpdate(
            rel_path=".devcontainer/Dockerfile",
            pattern=r"^(FROM nvcr\.io/nvidia/cuda:)[^-\s]+-base-ubuntu\d+\.\d+$",
            replacement=rf"\g<1>{v['CUDA_BASE_TAG']}-{cuda_ubuntu_suffix}",
            expect=1,
            desc=".devcontainer/Dockerfile FROM cuda",
        ),
        AnchoredUpdate(
            rel_path="hack/e2e-local-node.Dockerfile",
            pattern=r"^(ARG K3S_IMAGE=rancher/k3s:)\S+$",
            replacement=rf"\g<1>{v['K3S_VERSION']}",
            expect=1,
            desc="hack/e2e-local-node.Dockerfile ARG K3S_IMAGE default",
        ),
        AnchoredUpdate(
            rel_path="hack/e2e-local-node.Dockerfile",
            pattern=r"^(ARG CUDA_IMAGE=nvcr\.io/nvidia/cuda:)[^-\s]+-base-ubuntu\d+\.\d+$",
            replacement=rf"\g<1>{v['CUDA_BASE_TAG']}-{k3d_node_base_ubuntu_suffix}",
            expect=1,
            desc="hack/e2e-local-node.Dockerfile ARG CUDA_IMAGE default",
        ),
        # --- Helm chart ---
        AnchoredUpdate(
            rel_path="deployment/Chart.yaml",
            pattern=r'^version: "[^"]+"$',
            replacement=f'version: "{v["EXPORTER_VERSION"]}"',
            expect=1,
            desc="deployment/Chart.yaml version",
        ),
        AnchoredUpdate(
            rel_path="deployment/Chart.yaml",
            pattern=r'^appVersion: "[^"]+"$',
            replacement=f'appVersion: "{v["EXPORTER_VERSION"]}"',
            expect=1,
            desc="deployment/Chart.yaml appVersion",
        ),
        AnchoredUpdate(
            rel_path="deployment/values.yaml",
            pattern=r"^(  tag: )\S+-distroless$",
            replacement=rf"\g<1>{full_ver}-distroless",
            expect=1,
            desc="deployment/values.yaml image tag",
        ),
        # --- Raw YAML install artifacts ---
        AnchoredUpdate(
            rel_path="dcgm-exporter.yaml",
            pattern=r'^(\s*app\.kubernetes\.io/version: )"[^"]+"$',
            replacement=rf'\g<1>"{v["EXPORTER_VERSION"]}"',
            expect=5,
            desc="dcgm-exporter.yaml app.kubernetes.io/version labels",
        ),
        AnchoredUpdate(
            rel_path="dcgm-exporter.yaml",
            pattern=r'^(\s*- image: "nvcr\.io/nvidia/k8s/dcgm-exporter:)[^"]+"$',
            replacement=rf'\g<1>{full_ver}-distroless"',
            expect=1,
            desc="dcgm-exporter.yaml image tag",
        ),
        AnchoredUpdate(
            rel_path="service-monitor.yaml",
            pattern=r'^(\s*app\.kubernetes\.io/version: )"[^"]+"$',
            replacement=rf'\g<1>"{v["EXPORTER_VERSION"]}"',
            expect=2,
            desc="service-monitor.yaml app.kubernetes.io/version labels",
        ),
    ]

    # Fenced regions: Go SHA256 block + doc examples.
    # The SHA256 block sits inside a bash heredoc-like `|` YAML block scalar in
    # dependencies.yml; its indentation is 6 spaces (match the surrounding).
    go_sha_body = (
        f'      GO_SHA256_amd64="{sha_amd64}"\n'
        f'      GO_SHA256_arm64="{sha_arm64}"'
    )

    # README docker-run example: full fenced code block (sentinels wrap the fence).
    readme_example = (
        "```shell\n"
        f"docker run -d --gpus all --cap-add SYS_ADMIN --rm "
        f"-p 9400:9400 nvcr.io/nvidia/k8s/dcgm-exporter:{full_ver}-distroless\n"
        "```"
    )

    tests_docker_run_example = (
        "```bash\n"
        f"docker run --rm --gpus all --cap-add SYS_ADMIN "
        f"nvcr.io/nvidia/k8s/dcgm-exporter:{full_ver}-distroless\n"
        "```"
    )

    fenced: list[FencedUpdate] = [
        FencedUpdate(
            rel_path=".gitlab/ci/dependencies.yml",
            name="go-sha256",
            body=go_sha_body,
            desc=".gitlab/ci/dependencies.yml GO_SHA256 block",
        ),
        FencedUpdate(
            rel_path="README.md",
            name="docker-run-example",
            body=readme_example,
            desc="README.md docker run example",
        ),
        FencedUpdate(
            rel_path="tests/container/README.md",
            name="image-tags",
            body=(
                f"- `nvidia/dcgm-exporter:{full_ver}-distroless`\n"
                f"- `nvidia/dcgm-exporter:{full_ver}-{cuda_ubuntu_suffix}`"
            ),
            desc="tests/container/README.md image tag list",
        ),
        FencedUpdate(
            rel_path="tests/container/README.md",
            name="full-version-example",
            body=f"| `FULL_VERSION` | `{full_ver}` | Combined DCGM and exporter version (read from hack/versions.env) |",
            desc="tests/container/README.md FULL_VERSION table row",
        ),
        FencedUpdate(
            rel_path="tests/container/README.md",
            name="docker-run-example",
            body=tests_docker_run_example,
            desc="tests/container/README.md docker run example",
        ),
    ]

    return anchored, fenced


# ==============================================================================
# CLI
# ==============================================================================

app = typer.Typer(
    help="Propagate hack/versions.env to every derived file that tracks it.",
    add_completion=False,
    rich_markup_mode="rich",
    no_args_is_help=False,
)


@app.callback(invoke_without_command=True)
def main(
    check: Annotated[
        bool,
        typer.Option(
            "--check",
            "-n",
            "--dry-run",
            help="Verify no drift; exit 1 if any. No writes. CI-safe.",
        ),
    ] = False,
    verbose: Annotated[
        bool,
        typer.Option("--verbose", "-v", help="Report every update + ok line."),
    ] = False,
) -> None:
    """Propagate hack/versions.env to every derived file that tracks it."""
    mode = "check" if check else "apply"

    v = parse_versions_env(VERSIONS_ENV)

    required = (
        "GO_VERSION",
        "K3S_VERSION",
        "K3D_LINUX_AMD64_SHA256",
        "K3D_LINUX_ARM64_SHA256",
        "KUBECTL_LINUX_AMD64_SHA256",
        "KUBECTL_LINUX_ARM64_SHA256",
        "HELM_LINUX_AMD64_SHA256",
        "HELM_LINUX_ARM64_SHA256",
        "CUDA_BASE_TAG",
        "CUDA_UBUNTU_TAG",
        "K3D_NODE_BASE_UBUNTU_TAG",
        "DISTROLESS_HELPER_UBUNTU_TAG",
        "DISTROLESS_TAG",
        "BUILDER_UBUNTU_TAG",
        "BINFMT_IMAGE_TAG",
        "BINFMT_IMAGE_DIGEST",
        "BUILDKIT_IMAGE_TAG",
        "BUILDKIT_IMAGE_DIGEST",
        "SONAR_SCANNER_IMAGE_TAG",
        "SONAR_SCANNER_IMAGE_DIGEST",
        "DCGM_VERSION",
        "EXPORTER_VERSION",
        "PACKAGE_REVISION",
    )
    missing = [k for k in required if k not in v]
    if missing:
        console.print(
            f"[red]error:[/red] versions.env missing required keys: {', '.join(missing)}"
        )
        raise typer.Exit(EXIT_ENV_ERROR)

    if not re.fullmatch(r"[0-9]{3}", v["PACKAGE_REVISION"]):
        console.print(
            "[red]error:[/red] PACKAGE_REVISION must contain exactly three digits, "
            f"got: {v['PACKAGE_REVISION']!r}"
        )
        raise typer.Exit(EXIT_ENV_ERROR)

    sha_amd64, sha_arm64 = resolve_go_sha256s(
        v["GO_VERSION"], verbose=verbose
    )

    anchored, fenced = build_updates(v, sha_amd64, sha_arm64)
    if not (REPO_ROOT / ".gitlab-ci.yml").exists():
        anchored = [upd for upd in anchored if upd.rel_path != ".gitlab-ci.yml"]
    if not (REPO_ROOT / ".gitlab/ci/dependencies.yml").exists():
        fenced = [
            upd for upd in fenced if upd.rel_path != ".gitlab/ci/dependencies.yml"
        ]
    result = SyncResult()

    for upd in anchored:
        apply_anchored(upd, mode=mode, verbose=verbose, result=result)
    for upd in fenced:
        apply_fenced(upd, mode=mode, verbose=verbose, result=result)

    if mode == "check":
        if result.drift_files:
            console.print(
                f"\n[red]drift detected in {len(result.drift_files)} file(s); "
                f"run `make sync-versions` to fix[/red]"
            )
            raise typer.Exit(EXIT_DRIFT)
        if verbose:
            console.print("[green]all derived files in sync[/green]")
        raise typer.Exit(EXIT_OK)

    if result.updated_files:
        console.print(
            f"\n[green]updated {len(result.updated_files)} file(s)[/green]"
        )
    elif verbose:
        console.print("[green]nothing to update[/green]")


if __name__ == "__main__":
    app()
