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
"""Check whether pins in hack/versions.env are outdated upstream.

Queries upstream sources (go.dev, GitHub releases/tags) and compares against
the current pin values. The default action is a freshness check; `apply` and
`list` are explicit subcommands.

Usage:
    hack/check-versions.py                   # check all pins for freshness
    hack/check-versions.py -v                # verbose
    hack/check-versions.py -f GO_VERSION     # check one package
    make check-versions-apply                # apply available updates to versions.env
    hack/check-versions.py list              # list known packages
"""

from __future__ import annotations

import json
import os
import re
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from enum import Enum
from pathlib import Path
from typing import Annotated, Literal

import requests
import typer
from rich.console import Console
from rich.table import Table

console = Console()

SCRIPT_DIR = Path(__file__).resolve().parent
VERSIONS_ENV = SCRIPT_DIR / "versions.env"

CACHE_DIR = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "check-versions"
CACHE_TTL_HOURS = 1

GITHUB_API_URL = "https://api.github.com"
GO_DL_URL = "https://go.dev/dl/?mode=json"
API_TIMEOUT = 10


# =============================================================================
# Data model
# =============================================================================


class Source(Enum):
    GO_DEV = "go_dev"
    GITHUB_RELEASE = "github_release"
    GITHUB_TAG = "github_tag"
    MANUAL = "manual"


@dataclass(frozen=True)
class Package:
    variable: str
    display_name: str
    category: str
    source: Source
    source_id: str = ""
    strip_prefix: str = "v"


@dataclass
class DiscoveredPackage:
    package: Package
    current_value: str
    source_file: Path


@dataclass
class FetchResult:
    version: str | None = None
    reason: str | None = None


Status = Literal["up-to-date", "update-available", "ahead", "unknown", "skip", "error"]


@dataclass
class CheckResult:
    package: Package
    current: str
    latest: str
    status: Status
    reason: str = ""


@dataclass
class ResultSummary:
    up_to_date: int = 0
    updates: int = 0
    ahead: int = 0
    skipped: int = 0
    unknown: int = 0
    errors: int = 0

    @property
    def checked(self) -> int:
        return self.up_to_date + self.updates + self.ahead + self.unknown + self.errors


@dataclass
class AppContext:
    verbose: bool = False
    no_cache: bool = False
    filter_pkg: str | None = None
    workspace: Path = field(default_factory=Path.cwd)
    cache: "VersionCache | None" = None
    updates: list[tuple[DiscoveredPackage, str]] = field(default_factory=list)


# =============================================================================
# Registry: dcgm-exporter packages only
# =============================================================================

# fmt: off
REGISTRY: dict[str, Package] = {
    "GO_VERSION":                Package("GO_VERSION",                "Go",                "go_tools", Source.GO_DEV),
    "UV_VERSION":                Package("UV_VERSION",                "uv",                "tools",    Source.GITHUB_RELEASE, "astral-sh/uv"),
    "GOLANGCI_LINT_VERSION":     Package("GOLANGCI_LINT_VERSION",     "golangci-lint",     "go_tools", Source.GITHUB_RELEASE, "golangci/golangci-lint"),
    "GOIMPORTS_VERSION":         Package("GOIMPORTS_VERSION",         "goimports",         "go_tools", Source.GITHUB_TAG,     "golang/tools:v0."),
    "GOFUMPT_VERSION":           Package("GOFUMPT_VERSION",           "gofumpt",           "go_tools", Source.GITHUB_RELEASE, "mvdan/gofumpt"),
    "GOTESTSUM_VERSION":         Package("GOTESTSUM_VERSION",         "gotestsum",         "go_tools", Source.GITHUB_RELEASE, "gotestyourself/gotestsum"),
    "GOCOVER_COBERTURA_VERSION": Package("GOCOVER_COBERTURA_VERSION", "gocover-cobertura", "go_tools", Source.GITHUB_RELEASE, "boumenot/gocover-cobertura"),
    "CUDA_BASE_TAG":             Package("CUDA_BASE_TAG",             "CUDA base",         "docker",   Source.MANUAL),
    "CUDA_UBUNTU_TAG":           Package("CUDA_UBUNTU_TAG",           "CUDA Ubuntu",       "docker",   Source.MANUAL),
    "DISTROLESS_HELPER_UBUNTU_TAG": Package("DISTROLESS_HELPER_UBUNTU_TAG", "distroless helper Ubuntu", "docker", Source.MANUAL),
    "DISTROLESS_TAG":            Package("DISTROLESS_TAG",            "distroless",        "docker",   Source.MANUAL),
    "BUILDER_UBUNTU_TAG":        Package("BUILDER_UBUNTU_TAG",        "ubuntu builder",    "docker",   Source.MANUAL),
}
# fmt: on


# =============================================================================
# Cache
# =============================================================================


class VersionCache:
    def __init__(self, cache_dir: Path, ttl_hours: int = 1) -> None:
        self.cache_dir = cache_dir
        self.ttl_hours = ttl_hours
        self._cache: dict[str, str] = {}
        self._cache_file = cache_dir / "versions-cache.json"

    def load(self) -> None:
        if not self._cache_file.exists():
            return
        try:
            mtime = self._cache_file.stat().st_mtime
            if (time.time() - mtime) / 3600 < self.ttl_hours:
                self._cache = json.loads(self._cache_file.read_text())
        except (json.JSONDecodeError, OSError):
            pass

    def get(self, key: str) -> str | None:
        return self._cache.get(key)

    def set(self, key: str, value: str) -> None:
        self._cache[key] = value

    def save(self) -> None:
        self.cache_dir.mkdir(parents=True, exist_ok=True)
        self._cache_file.write_text(json.dumps(self._cache, indent=2))

    def clear(self) -> None:
        if self._cache_file.exists():
            self._cache_file.unlink()
        self._cache = {}


# =============================================================================
# Utility
# =============================================================================


def log_debug(ctx: AppContext, msg: str) -> None:
    if ctx.verbose:
        console.print(f"[dim]{msg}[/dim]")


def parse_env_file(path: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if "=" in line:
            key, _, value = line.partition("=")
            result[key.strip()] = value.strip().strip('"').strip("'")
    return result


def get_github_headers() -> dict[str, str]:
    headers = {"Accept": "application/vnd.github.v3+json"}
    token = os.environ.get("GITHUB_TOKEN")
    if token:
        headers["Authorization"] = f"token {token}"
    return headers


# =============================================================================
# Fetchers
# =============================================================================


def fetch_go_version(cache: VersionCache, ctx: AppContext) -> FetchResult:
    key = "go_dev:latest"
    cached = cache.get(key)
    if cached:
        log_debug(ctx, f"Go: cache hit ({cached})")
        return FetchResult(version=cached)
    log_debug(ctx, "Go: fetching from go.dev...")
    try:
        r = requests.get(GO_DL_URL, timeout=API_TIMEOUT)
        r.raise_for_status()
        releases = r.json()
        if not releases:
            return FetchResult(reason="no-releases")
        version = releases[0]["version"].replace("go", "")
        cache.set(key, version)
        return FetchResult(version=version)
    except Exception as e:
        log_debug(ctx, f"Go: error - {e}")
        return FetchResult(reason="error")


def fetch_github_release(repo: str, cache: VersionCache, ctx: AppContext) -> FetchResult:
    key = f"github_release:{repo}"
    cached = cache.get(key)
    if cached:
        log_debug(ctx, f"GitHub {repo}: cache hit ({cached})")
        return FetchResult(version=cached)
    log_debug(ctx, f"GitHub {repo}: fetching...")
    try:
        r = requests.get(
            f"{GITHUB_API_URL}/repos/{repo}/releases/latest",
            headers=get_github_headers(),
            timeout=API_TIMEOUT,
        )
        if r.status_code == 403:
            return FetchResult(reason="rate-limited")
        if r.status_code == 404:
            return FetchResult(reason="no-releases")
        r.raise_for_status()
        tag = r.json().get("tag_name", "")
        if tag:
            cache.set(key, tag)
            return FetchResult(version=tag)
        return FetchResult(reason="no-tag")
    except Exception as e:
        log_debug(ctx, f"GitHub {repo}: error - {e}")
        return FetchResult(reason="error")


def fetch_github_tag(repo_prefix: str, cache: VersionCache, ctx: AppContext) -> FetchResult:
    parts = repo_prefix.split(":")
    repo = parts[0]
    prefix = parts[1] if len(parts) > 1 else "v"
    key = f"github_tag:{repo}:{prefix}"
    cached = cache.get(key)
    if cached:
        log_debug(ctx, f"GitHub tag {repo}: cache hit ({cached})")
        return FetchResult(version=cached)
    log_debug(ctx, f"GitHub tag {repo}: fetching...")
    try:
        r = requests.get(
            f"{GITHUB_API_URL}/repos/{repo}/tags?per_page=50",
            headers=get_github_headers(),
            timeout=API_TIMEOUT,
        )
        if r.status_code == 403:
            return FetchResult(reason="rate-limited")
        r.raise_for_status()
        for tag in r.json():
            name = tag["name"]
            if name.startswith(prefix) and not any(
                x in name.lower() for x in ["rc", "alpha", "beta", "-dev"]
            ):
                cache.set(key, name)
                return FetchResult(version=name)
        return FetchResult(reason="no-matching-tag")
    except Exception as e:
        log_debug(ctx, f"GitHub tag {repo}: error - {e}")
        return FetchResult(reason="error")


def fetch_version(package: Package, cache: VersionCache, ctx: AppContext) -> FetchResult:
    match package.source:
        case Source.GO_DEV:
            return fetch_go_version(cache, ctx)
        case Source.GITHUB_RELEASE:
            return fetch_github_release(package.source_id, cache, ctx)
        case Source.GITHUB_TAG:
            return fetch_github_tag(package.source_id, cache, ctx)
        case Source.MANUAL:
            return FetchResult(reason="manual")


# =============================================================================
# Version comparison
# =============================================================================


def normalize_version(current: str, latest: str) -> str:
    current_has_v = current.startswith("v")
    latest_has_v = latest.startswith("v")
    if current_has_v and not latest_has_v:
        return f"v{latest}"
    if not current_has_v and latest_has_v:
        return latest.lstrip("v")
    return latest


def numeric_version_parts(version: str) -> tuple[int, ...] | None:
    match = re.match(r"^v?(\d+(?:\.\d+)*)", version)
    if not match:
        return None
    return tuple(int(part) for part in match.group(1).split("."))


def compare_versions(current: str, latest: str) -> int | None:
    current_parts = numeric_version_parts(current)
    latest_parts = numeric_version_parts(latest)
    if current_parts is None or latest_parts is None:
        return None

    width = max(len(current_parts), len(latest_parts))
    current_parts += (0,) * (width - len(current_parts))
    latest_parts += (0,) * (width - len(latest_parts))

    if current_parts < latest_parts:
        return -1
    if current_parts > latest_parts:
        return 1
    return 0


def determine_status(
    current: str, result: FetchResult, package: Package
) -> tuple[Status, str]:
    if package.source == Source.MANUAL:
        return ("skip", "manual")
    if result.version is None:
        reasons = {
            "rate-limited": "rate limited",
            "timeout": "timeout",
            "error": "error",
            "no-releases": "no releases",
            "no-tag": "no tag",
            "no-matching-tag": "no match",
        }
        return ("unknown", reasons.get(result.reason or "", result.reason or "unknown"))

    strip = package.strip_prefix
    cur = current.lstrip(strip) if strip else current
    lat = result.version.lstrip(strip) if strip else result.version
    if current == result.version or cur == lat:
        return ("up-to-date", "")

    comparison = compare_versions(cur, lat)
    if comparison is None:
        return ("unknown", "uncomparable")
    if comparison < 0:
        return ("update-available", "")
    return ("ahead", "current ahead of upstream")


# =============================================================================
# Discovery
# =============================================================================


def discover_packages(ctx: AppContext) -> list[DiscoveredPackage]:
    if not VERSIONS_ENV.exists():
        console.print(f"[red]error:[/red] {VERSIONS_ENV} not found")
        raise typer.Exit(2)

    found = parse_env_file(VERSIONS_ENV)
    missing = [var for var in REGISTRY if var not in found]
    if missing:
        console.print(
            f"[red]error:[/red] {VERSIONS_ENV} missing expected keys: "
            f"{', '.join(missing)}"
        )
        raise typer.Exit(2)

    packages: list[DiscoveredPackage] = [
        DiscoveredPackage(
            package=REGISTRY[var],
            current_value=found[var],
            source_file=VERSIONS_ENV,
        )
        for var in REGISTRY
    ]
    log_debug(ctx, f"Matched {len(packages)}/{len(REGISTRY)} packages")
    return packages


# =============================================================================
# Orchestration
# =============================================================================


def fetch_all(
    packages: list[DiscoveredPackage], cache: VersionCache, ctx: AppContext
) -> list[CheckResult]:
    results: list[CheckResult] = []
    with ThreadPoolExecutor(max_workers=8) as pool:
        future_to_pkg = {
            pool.submit(fetch_version, p.package, cache, ctx): p for p in packages
        }
        for fut in as_completed(future_to_pkg):
            pkg = future_to_pkg[fut]
            try:
                fr = fut.result()
                status, reason = determine_status(pkg.current_value, fr, pkg.package)
                display = normalize_version(pkg.current_value, fr.version) if fr.version else "?"
                results.append(
                    CheckResult(pkg.package, pkg.current_value, display, status, reason)
                )
                if status == "update-available" and fr.version:
                    ctx.updates.append((pkg, normalize_version(pkg.current_value, fr.version)))
            except Exception as e:
                results.append(
                    CheckResult(pkg.package, pkg.current_value, "?", "error", str(e)[:30])
                )
    return results


# =============================================================================
# Output
# =============================================================================


def print_results(
    results: list[CheckResult], category: str | None = None
) -> ResultSummary:
    by_cat: dict[str, list[CheckResult]] = {}
    for r in results:
        if category and r.package.category != category:
            continue
        by_cat.setdefault(r.package.category, []).append(r)

    summary = ResultSummary()
    for cat, items in sorted(by_cat.items()):
        console.print(f"\n[bold blue]=== {cat.replace('_', ' ').title()} ===[/bold blue]\n")
        table = Table(show_header=True, header_style="bold", box=None, padding=(0, 2))
        table.add_column("Package", style="cyan", no_wrap=True)
        table.add_column("Current", no_wrap=True)
        table.add_column("Latest", no_wrap=True)
        table.add_column("Status", no_wrap=True)

        for r in sorted(items, key=lambda x: x.package.display_name.lower()):
            if r.status == "up-to-date":
                st = "[green]up-to-date[/green]"
                summary.up_to_date += 1
            elif r.status == "update-available":
                st = "[yellow]update available[/yellow]"
                summary.updates += 1
            elif r.status == "ahead":
                st = "[blue]ahead[/blue]" + (f" [dim]({r.reason})[/dim]" if r.reason else "")
                summary.ahead += 1
            elif r.status == "skip":
                st = f"[dim]skip ({r.reason})[/dim]" if r.reason else "[dim]skip[/dim]"
                summary.skipped += 1
            elif r.status == "unknown":
                st = f"[red]{r.status}[/red]" + (f" [dim]({r.reason})[/dim]" if r.reason else "")
                summary.unknown += 1
            else:
                st = f"[red]{r.status}[/red]" + (f" [dim]({r.reason})[/dim]" if r.reason else "")
                summary.errors += 1
            table.add_row(r.package.display_name, r.current, r.latest, st)
        console.print(table)
    return summary


# =============================================================================
# Apply
# =============================================================================


def apply_updates(ctx: AppContext, category: str | None = None) -> None:
    to_apply = [
        (pkg, ver) for pkg, ver in ctx.updates
        if not category or pkg.package.category == category
    ]
    if not to_apply:
        console.print("[green]No updates to apply.[/green]")
        return

    content = VERSIONS_ENV.read_text()
    for pkg, new_value in to_apply:
        pattern = rf"^{re.escape(pkg.package.variable)}=.*$"
        content = re.sub(pattern, f"{pkg.package.variable}={new_value}", content, flags=re.MULTILINE)
        console.print(f"  Updated {pkg.package.variable}: {pkg.current_value} -> {new_value}")

    VERSIONS_ENV.write_text(content)
    console.print(f"\n[green]Wrote {VERSIONS_ENV}[/green]")
    console.print("[dim]Run `make sync-versions` to propagate changes to derived files.[/dim]")


# =============================================================================
# CLI
# =============================================================================

app = typer.Typer(
    help="Check whether pins in hack/versions.env are outdated upstream.",
    add_completion=False,
    rich_markup_mode="rich",
    no_args_is_help=False,
)


@app.callback(invoke_without_command=True)
def main(
    ctx: typer.Context,
    verbose: Annotated[bool, typer.Option("--verbose", "-v")] = False,
    no_cache: Annotated[bool, typer.Option("--no-cache")] = False,
    filter_pkg: Annotated[str | None, typer.Option("--filter", "-f")] = None,
    category: Annotated[str | None, typer.Option("--category", "-c")] = None,
) -> None:
    """Report which pinned versions are outdated upstream.

    Runs a freshness check by default. Use `apply` to write updates into
    hack/versions.env, or `list` to show the registry.
    """
    cache = VersionCache(CACHE_DIR)
    if not no_cache:
        cache.load()
    ctx.obj = AppContext(
        verbose=verbose, no_cache=no_cache, filter_pkg=filter_pkg, cache=cache
    )
    if ctx.invoked_subcommand is None:
        run_check(ctx.obj, category=category)


def run_check(app_ctx: AppContext, *, category: str | None = None) -> None:
    """Default action: check all dependency versions against latest available."""
    cache = app_ctx.cache
    assert cache is not None

    effective_filter = app_ctx.filter_pkg

    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        log_debug(app_ctx, "No GITHUB_TOKEN - using unauthenticated requests (60/hr)")

    console.print("\n[bold]Checking dependency versions...[/bold]")
    console.print(f"Source: {VERSIONS_ENV}\n")

    packages = discover_packages(app_ctx)
    if effective_filter:
        filters = [f.strip().lower() for f in effective_filter.split(",")]
        packages = [
            p for p in packages
            if p.package.variable.lower() in filters
            or p.package.display_name.lower() in filters
        ]
        if not packages:
            console.print(f"[yellow]No packages matched filter: {effective_filter}[/yellow]")
            return

    if category:
        packages = [p for p in packages if p.package.category == category]
    if not packages:
        selected = ", ".join(
            part
            for part in (
                f"filter {effective_filter!r}" if effective_filter else "",
                f"category {category!r}" if category else "",
            )
            if part
        )
        console.print(f"[yellow]No packages matched {selected or 'the selected scope'}[/yellow]")
        return

    results = fetch_all(packages, cache, app_ctx)
    cache.save()

    summary = print_results(results, category)
    console.print()
    if summary.updates:
        console.print(f"[yellow]{summary.updates} update(s) available.[/yellow]")
        console.print("\nTo apply: [bold]make check-versions-apply[/bold]")
        if summary.unknown or summary.errors:
            console.print(
                "[yellow]Some checks were incomplete: "
                f"{summary.unknown} unknown, {summary.errors} error(s), "
                f"{summary.skipped} skipped.[/yellow]"
            )
    elif summary.checked and not (summary.ahead or summary.unknown or summary.errors):
        console.print("[green]All checked dependencies are up-to-date![/green]")
    elif summary.checked:
        console.print(
            "[yellow]No safe updates to apply: "
            f"{summary.up_to_date} up-to-date, {summary.ahead} ahead, "
            f"{summary.unknown} unknown, "
            f"{summary.errors} error(s), {summary.skipped} skipped.[/yellow]"
        )
    else:
        console.print(
            "[yellow]No auto-checkable dependencies in selected scope; "
            f"skipped {summary.skipped} manual package(s).[/yellow]"
        )

    if summary.skipped and summary.checked:
        console.print(f"[dim]Skipped {summary.skipped} manual package(s).[/dim]")

    if not token and any(r.reason in ("rate limited", "rate-limited") for r in results):
        console.print(
            "\n[dim]Hint: Set GITHUB_TOKEN to avoid rate limits[/dim]"
        )


@app.command()
def apply(
    ctx: typer.Context,
    verbose: Annotated[bool, typer.Option("--verbose", "-v")] = False,
    category: Annotated[str | None, typer.Option("--category", "-c")] = None,
    filter_pkg: Annotated[str | None, typer.Option("--filter", "-f")] = None,
) -> None:
    """Apply version updates to hack/versions.env."""
    app_ctx: AppContext = ctx.obj
    if verbose:
        app_ctx.verbose = True
    cache = app_ctx.cache
    assert cache is not None

    console.print("\n[bold]Checking for updates to apply...[/bold]\n")
    packages = discover_packages(app_ctx)
    if filter_pkg or app_ctx.filter_pkg:
        filters = [f.strip().lower() for f in (filter_pkg or app_ctx.filter_pkg or "").split(",")]
        packages = [
            p for p in packages
            if p.package.variable.lower() in filters
            or p.package.display_name.lower() in filters
        ]

    fetch_all(packages, cache, app_ctx)
    cache.save()
    apply_updates(app_ctx, category)


@app.command("list")
def list_packages(
    category: Annotated[str | None, typer.Option("--category", "-c")] = None,
) -> None:
    """List all known version variables and their sources."""
    by_cat: dict[str, list[Package]] = {}
    for pkg in REGISTRY.values():
        if category and pkg.category != category:
            continue
        by_cat.setdefault(pkg.category, []).append(pkg)

    if not by_cat:
        console.print(f"[yellow]No packages found for category: {category}[/yellow]")
        return

    console.print("\n[bold]Known Version Variables[/bold]\n")
    for cat, packages in sorted(by_cat.items()):
        console.print(f"[bold blue]=== {cat.replace('_', ' ').title()} ===[/bold blue]\n")
        table = Table(show_header=True, header_style="bold", box=None, padding=(0, 2))
        table.add_column("Variable", style="cyan", no_wrap=True)
        table.add_column("Display Name", no_wrap=True)
        table.add_column("Source", style="dim", no_wrap=True)

        for pkg in sorted(packages, key=lambda x: x.variable):
            table.add_row(pkg.variable, pkg.display_name, pkg.source.value)
        console.print(table)
        console.print()

    total = sum(len(pkgs) for pkgs in by_cat.values())
    console.print(f"[dim]Total: {total} packages in {len(by_cat)} categories[/dim]")


@app.command("clear-cache")
def clear_cache() -> None:
    """Clear the version cache."""
    cache = VersionCache(CACHE_DIR)
    cache.clear()
    console.print("[green]Cache cleared.[/green]")


if __name__ == "__main__":
    app()
