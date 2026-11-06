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

"""Command-line workflows for checking and synchronizing version pins."""

from __future__ import annotations

import argparse
import sys
from collections.abc import Mapping
from pathlib import Path

from . import pins, rendering, upstream, writes

EXIT_OK = 0
EXIT_DRIFT = 1
EXIT_ERROR = 2
# All version subcommands read this canonical pin file.
VERSIONS_ENV_FILENAME = "versions.env"


def _display(results: list[upstream.Resolution]) -> None:
    """Print a dependency freshness table using plain terminal output."""

    columns = ("KEY", "CURRENT", "LATEST", "STATUS", "POLICY / DETAIL")
    rows: list[tuple[str, str, str, str, str]] = []
    for item in results:
        rows.append(
            (
                item.spec.key,
                item.current,
                item.latest or "-",
                item.status,
                item.detail,
            )
        )
    widths = [
        max(len(columns[index]), *(len(row[index]) for row in rows))
        for index in range(len(columns))
    ]
    print(
        "  ".join(column.ljust(widths[index]) for index, column in enumerate(columns))
    )
    print("  ".join("-" * width for width in widths))
    for row in rows:
        print("  ".join(value.ljust(widths[index]) for index, value in enumerate(row)))


def _display_module_updates(
    updates: list[upstream.ModuleUpdate],
    constrained: list[upstream.ModuleUpdate],
) -> None:
    """Print compatible and constrained Go module update results."""

    print("\nCompatible Go module updates")
    if not updates:
        print("  go get -u -t ./... produces no module changes")
    for update in updates:
        kind = "transitive" if update.indirect else "direct"
        print(f"  {kind:10} {update.path}: {update.current} -> {update.latest}")
    if constrained:
        print("\nOther upstream module releases (informational; not selected by go get -u -t ./...)")
        for update in constrained:
            kind = "transitive" if update.indirect else "direct"
            print(f"  {kind:10} {update.path}: {update.current} -> {update.latest}")


def _constrained_module_updates(
    advertised: list[upstream.ModuleUpdate],
    selected: list[upstream.ModuleUpdate],
) -> list[upstream.ModuleUpdate]:
    """Find upstream module releases not selected by compatible upgrades."""

    selected_by_path = {update.path: update for update in selected}
    constrained: list[upstream.ModuleUpdate] = []
    for update in advertised:
        selected_update = selected_by_path.get(update.path)
        if selected_update is None:
            constrained.append(update)
        elif selected_update.latest != update.latest:
            constrained.append(
                upstream.ModuleUpdate(
                    path=update.path,
                    current=selected_update.latest,
                    latest=update.latest,
                    indirect=update.indirect,
                )
            )
    return constrained


def _strict_exit(
    results: list[upstream.Resolution],
    module_updates: list[upstream.ModuleUpdate],
) -> int:
    """Map complete, stale, and incomplete reports to documented exit codes."""

    if any(item.status == "error" for item in results):
        return EXIT_ERROR
    if module_updates or any(item.status == "update-available" for item in results):
        return EXIT_DRIFT
    return EXIT_OK


def _complete_apply_updates(
    client: upstream.HTTPClient,
    values: Mapping[str, str],
) -> dict[str, str]:
    """Produce a complete companion-aware versions.env update set."""

    updates: dict[str, str] = {}
    # Resolve independently versioned roots before values derived from them.
    root_specs = [
        spec
        for spec in pins.SPECS
        if spec.update_policy is pins.UpdatePolicy.AUTOMATIC
        and spec.source is not pins.Source.OCI_DIGEST
    ]
    root_results = [upstream.resolve_spec(client, spec, values) for spec in root_specs]
    errors = [item for item in root_results if item.status == "error"]
    if errors:
        raise pins.VersioningError(
            "freshness resolution failed: "
            + "; ".join(f"{item.spec.key}: {item.detail}" for item in errors)
        )
    for item in root_results:
        if item.status == "update-available":
            updates[item.spec.key] = item.latest

    # Derived pins must see root updates even though versions.env is not written yet.
    proposed = dict(values)
    proposed.update(updates)
    derived_specs = [
        spec for spec in pins.SPECS if spec.update_policy is pins.UpdatePolicy.DERIVED
    ]
    for spec in derived_specs:
        result = upstream.resolve_spec(client, spec, proposed)
        if result.status == "error":
            raise pins.VersioningError(
                f"derived resolution failed: {spec.key}: {result.detail}"
            )
        proposed[spec.key] = result.latest
        updates[spec.key] = result.latest

    # Resolve integrity companions last so each one matches the proposed tag/version.
    companion_specs = [
        spec for spec in pins.SPECS if spec.update_policy is pins.UpdatePolicy.COMPANION
    ]
    companion_results = [
        upstream.resolve_spec(client, spec, proposed) for spec in companion_specs
    ]
    errors = [item for item in companion_results if item.status == "error"]
    if errors:
        raise pins.VersioningError(
            "companion resolution failed: "
            + "; ".join(f"{item.spec.key}: {item.detail}" for item in errors)
        )
    for item in companion_results:
        updates[item.spec.key] = item.latest
    return updates


def _sync_plan(repo_root: Path, values: Mapping[str, str]) -> writes.WritePlan:
    """Build the transactional plan for derived files only."""

    anchored, fenced = rendering.build_updates(dict(values), repo_root)
    return writes.build_write_plan(repo_root, anchored, fenced)


def _complete_apply_plan(
    repo_root: Path,
    candidate_env: str,
    candidate_values: Mapping[str, str],
) -> writes.WritePlan:
    """Combine canonical, derived, and Go module candidates in one plan."""

    derived_plan = _sync_plan(repo_root, candidate_values)
    originals = dict(derived_plan.originals)
    candidates = dict(derived_plan.candidates)

    go_mod = repo_root / "go.mod"
    go_sum = repo_root / "go.sum"
    versions_env = repo_root / "hack" / VERSIONS_ENV_FILENAME
    base_sum = go_sum.read_text()
    module_mod, module_sum = upstream.build_go_module_candidates(
        repo_root,
        candidates[go_mod],
        base_sum,
        candidate_values,
    )
    candidates[go_mod] = module_mod
    originals[go_sum] = base_sum
    candidates[go_sum] = module_sum
    originals[versions_env] = versions_env.read_text()
    candidates[versions_env] = candidate_env
    return writes.WritePlan(
        root=repo_root,
        originals=originals,
        candidates=candidates,
    )


def check_versions(*, strict: bool, repo_root: Path) -> int:
    """Implement the read-only `check` command."""

    try:
        versions_env = repo_root / "hack" / VERSIONS_ENV_FILENAME
        values = pins.parse_versions_env(versions_env)

        results = upstream.resolve_all(upstream.HTTPClient(), values)
        advertised_updates = upstream.discover_module_updates(repo_root)
        module_updates = upstream.discover_compatible_module_updates(repo_root, values)
        constrained_updates = _constrained_module_updates(
            advertised_updates,
            module_updates,
        )

        _display(results)
        _display_module_updates(module_updates, constrained_updates)

        if any(item.status == "error" for item in results):
            return EXIT_ERROR
        return _strict_exit(results, module_updates) if strict else EXIT_OK
    except (pins.VersioningError, OSError) as error:
        print(f"error: {error}", file=sys.stderr)
        return EXIT_ERROR


def sync_versions(*, check: bool, verbose: bool, repo_root: Path) -> int:
    """Implement `sync`, including read-only drift and verbose modes."""

    try:
        versions_env = repo_root / "hack" / VERSIONS_ENV_FILENAME
        values = pins.parse_versions_env(versions_env)
        plan = _sync_plan(repo_root, values)

        if check:
            for path in plan.changed_paths:
                print(f"DRIFT: {path.relative_to(repo_root)}", file=sys.stderr)
                if verbose:
                    print(
                        writes.unified_diff(plan, path), end="", file=sys.stderr
                    )

            if plan.changed_paths:
                print(
                    f"drift detected in {len(plan.changed_paths)} file(s); "
                    "run `task deps:sync`",
                    file=sys.stderr,
                )
                return EXIT_DRIFT

            if verbose:
                print("all derived files are in sync", file=sys.stderr)
            return EXIT_OK

        writes.apply_write_plan(plan)
        for path in plan.changed_paths:
            print(f"updated: {path.relative_to(repo_root)}", file=sys.stderr)
        if verbose and not plan.changed_paths:
            print("nothing to update", file=sys.stderr)
        return EXIT_OK
    except (pins.VersioningError, OSError) as error:
        print(f"error: {error}", file=sys.stderr)
        return EXIT_ERROR


def apply_versions(*, repo_root: Path) -> int:
    """Implement automatic pin and compatible Go graph updates."""

    try:
        versions_env = repo_root / "hack" / VERSIONS_ENV_FILENAME
        values = pins.parse_versions_env(versions_env)
        updates = _complete_apply_updates(upstream.HTTPClient(), values)
        candidate_env = rendering.render_versions_env_updates(
            versions_env.read_text(),
            updates,
            source=str(versions_env),
        )
        candidate_values = pins.parse_versions_env_text(
            candidate_env,
            source=str(versions_env),
        )
        plan = _complete_apply_plan(repo_root, candidate_env, candidate_values)
        writes.apply_write_plan(plan)
        for path in plan.changed_paths:
            print(f"updated: {path.relative_to(repo_root)}")
        if not plan.changed_paths:
            print("all versions and Go modules are current")
        manual = [
            spec
            for spec in pins.SPECS
            if spec.update_policy is pins.UpdatePolicy.MANUAL
        ]
        if manual:
            print("\nManual policies left unchanged:")
            for spec in manual:
                print(f"  {spec.key}: {spec.rationale}")
        return EXIT_OK
    except (pins.VersioningError, OSError) as error:
        print(f"error: {error}", file=sys.stderr)
        return EXIT_ERROR


def test_versions(repo_root: Path) -> int:
    """Run the version-tool unit test suite through the public CLI."""

    import unittest

    suite = unittest.defaultTestLoader.discover(
        str(repo_root / "hack" / "tests"),
        pattern="test_*.py",
    )
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    return EXIT_OK if result.wasSuccessful() else EXIT_ERROR


def _parser() -> argparse.ArgumentParser:
    """Define the stable public subcommands and compatibility flags."""

    parser = argparse.ArgumentParser(
        description="Check, apply, and synchronize repository dependency versions."
    )
    commands = parser.add_subparsers(dest="command", required=True)

    check = commands.add_parser("check", help="report upstream updates")
    check.add_argument(
        "--strict",
        action="store_true",
        help="exit 1 for selectable updates; constrained module releases are informational",
    )

    sync = commands.add_parser(
        "sync",
        help="propagate versions.env to derived files",
    )
    sync.add_argument(
        "--check",
        action="store_true",
        help="report drift without writing",
    )
    sync.add_argument(
        "--verbose",
        "-v",
        action="store_true",
        help="show diffs or unchanged status",
    )

    commands.add_parser(
        "apply",
        help="apply automatic upstream and Go module updates",
    )
    commands.add_parser("test", help="run version-tool unit tests")
    return parser


def main(argv: list[str] | None = None, *, repo_root: Path) -> int:
    """Parse CLI arguments and dispatch to one public command."""

    args = _parser().parse_args(argv)
    if args.command == "check":
        return check_versions(strict=args.strict, repo_root=repo_root)
    if args.command == "sync":
        return sync_versions(
            check=args.check,
            verbose=args.verbose,
            repo_root=repo_root,
        )
    if args.command == "apply":
        return apply_versions(repo_root=repo_root)
    if args.command == "test":
        return test_versions(repo_root)
    raise AssertionError(f"unhandled command: {args.command}")
