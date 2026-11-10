#!/usr/bin/env -S uv run --script
# Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""Check final image file metadata from a `docker save` archive.

The archive is read in place; no layer path is extracted. The script prints a
JSON file inventory and exits nonzero when the image breaks a metadata rule.
"""

from __future__ import annotations

import argparse
import json
import posixpath
import sys
import tarfile
from pathlib import Path
from typing import IO, Any

# Layer entries are keyed by their normalized path without a leading slash.
Files = dict[str, tarfile.TarInfo]

WHITEOUT_PREFIX = ".wh."
OPAQUE_WHITEOUT = ".wh..wh..opq"
CONFIG_DIR = "etc/dcgm-exporter"
EXPORTER_BINARY = "/usr/bin/dcgm-exporter"


def _parse_args(argv: list[str]) -> argparse.Namespace:
    """Read the saved image archive and `docker image inspect` paths."""

    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("archive", type=Path, help="docker save archive")
    parser.add_argument("inspect", type=Path, help="docker image inspect JSON")
    return parser.parse_args(argv)


def _layer_entries(layer: tarfile.TarFile) -> list[tuple[str, tarfile.TarInfo]]:
    """Return normalized layer paths, rejecting any path outside the root."""

    entries: list[tuple[str, tarfile.TarInfo]] = []
    for entry in layer:
        path = posixpath.normpath(entry.name).lstrip("/")
        if path == ".." or path.startswith("../"):
            raise ValueError("invalid layer path")
        entries.append((path, entry))
    return entries


def _apply_whiteouts(files: Files, entries: list[tuple[str, tarfile.TarInfo]]) -> None:
    """Remove lower-layer paths hidden by this layer's whiteout entries."""

    for path, _ in entries:
        parent, name = posixpath.split(path)
        if not name.startswith(WHITEOUT_PREFIX):
            continue

        # An opaque whiteout hides the directory's contents; a plain whiteout
        # hides the named path and anything below it.
        opaque = name == OPAQUE_WHITEOUT
        target = (
            parent if opaque else posixpath.join(parent, name[len(WHITEOUT_PREFIX) :])
        )
        hidden = [
            old
            for old in files
            if (not target and old != ".")
            or old.startswith(target + "/")
            or (not opaque and old == target)
        ]
        for old in hidden:
            del files[old]


def _add_entries(files: Files, entries: list[tuple[str, tarfile.TarInfo]]) -> None:
    """Add this layer's entries over the lower layers."""

    for path, entry in entries:
        if posixpath.basename(path).startswith(WHITEOUT_PREFIX):
            continue

        # A non-directory that replaces a directory also drops its children.
        previous = files.get(path)
        if previous is not None and previous.isdir() and not entry.isdir():
            replaced = [old for old in files if old.startswith(path + "/")]
            for old in replaced:
                del files[old]
        files[path] = entry


def _member(archive: tarfile.TarFile, name: str) -> IO[bytes]:
    """Open one regular file inside the saved image archive."""

    stream = archive.extractfile(name)
    if stream is None:
        raise ValueError(f"{name}: not a regular file in the image archive")
    return stream


def read_image_files(archive_path: Path) -> Files:
    """Merge every layer of a single saved image into its final file tree."""

    files: Files = {}
    with tarfile.open(archive_path) as archive:
        manifests = json.load(_member(archive, "manifest.json"))
        if len(manifests) != 1:
            raise ValueError("expected exactly one saved image")

        for layer_path in manifests[0]["Layers"]:
            with tarfile.open(
                fileobj=_member(archive, layer_path), mode="r|*"
            ) as layer:
                entries = _layer_entries(layer)

            # Whiteouts apply to lower layers, before adding the current layer.
            _apply_whiteouts(files, entries)
            _add_entries(files, entries)
    return files


def _record(path: str, entry: tarfile.TarInfo) -> dict[str, Any]:
    """Describe one final file for the retained inventory."""

    return {
        "path": "/" + path,
        "uid": entry.uid,
        "gid": entry.gid,
        "mode": oct(entry.mode),
        "type": entry.type.decode("ascii"),
        "link": entry.linkname,
        "pax": entry.pax_headers,
    }


def _file_violations(path: str, entry: tarfile.TarInfo) -> list[str]:
    """Check ownership, mode bits, and capabilities for one final file."""

    violations: list[str] = []
    if entry.isfile() and (entry.uid != 0 or entry.gid != 0):
        violations.append(f"/{path}: owner must be root:root")

    if entry.isfile():
        if entry.mode & 0o6000:
            violations.append(f"/{path}: setuid/setgid bits")
        if entry.mode & 0o022:
            violations.append(f"/{path}: group/world writable")

    if any("security.capability" in key for key in entry.pax_headers):
        violations.append(f"/{path}: file capabilities")

    if path.startswith(CONFIG_DIR + "/") and entry.isfile() and entry.mode != 0o644:
        violations.append(f"/{path}: exporter configuration must be 0644")
    return violations


def _config_dir_violations(files: Files) -> list[str]:
    """Require the exporter configuration directory to be root-owned 0755."""

    directory = files.get(CONFIG_DIR)
    if (
        directory is None
        or not directory.isdir()
        or directory.mode != 0o755
        or directory.uid != 0
        or directory.gid != 0
    ):
        return [f"/{CONFIG_DIR}: expected root-owned directory with mode 0755"]
    return []


def _entrypoint_violations(files: Files, entrypoint: list[str]) -> list[str]:
    """Require the exporter and the entrypoint to be 0755 regular files."""

    violations: list[str] = []
    for path in [EXPORTER_BINARY, *entrypoint[:1]]:
        entry = files.get(path.lstrip("/"))
        if entry is None or not entry.isfile() or entry.mode != 0o755:
            violations.append(f"{path}: expected regular executable with mode 0755")

    if not entrypoint:
        violations.append("missing image entrypoint")
    return violations


def main(argv: list[str]) -> int:
    """Print the image file report and return 1 when any rule fails."""

    args = _parse_args(argv)
    files = read_image_files(args.archive)

    inventory: list[dict[str, Any]] = []
    violations: list[str] = []
    for path, entry in sorted(files.items()):
        inventory.append(_record(path, entry))
        violations.extend(_file_violations(path, entry))
    violations.extend(_config_dir_violations(files))

    with args.inspect.open(encoding="utf-8") as stream:
        image = json.load(stream)[0]
    entrypoint: list[str] = image["Config"].get("Entrypoint") or []
    violations.extend(_entrypoint_violations(files, entrypoint))

    report = {
        "image_id": image["Id"],
        "repo_digests": image.get("RepoDigests", []),
        "entrypoint": entrypoint,
        "files": inventory,
        "violations": violations,
    }
    json.dump(report, sys.stdout, indent=2)
    print()

    if violations:
        print("Image metadata violations: " + "; ".join(violations), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
