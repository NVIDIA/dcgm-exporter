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

"""Preflight, stage, and transactionally apply repository file updates."""

from __future__ import annotations

import difflib
import os
import re
import tempfile
from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from types import MappingProxyType

from .pins import VersioningError


@dataclass(frozen=True)
class AnchoredUpdate:
    """Replace an exact number of regex anchors in one derived file."""

    rel_path: str
    pattern: str
    replacement: str
    expect: int
    desc: str


@dataclass(frozen=True)
class FencedUpdate:
    """Replace content between a named pair of synchronization sentinels."""

    rel_path: str
    name: str
    body: str
    desc: str


@dataclass(frozen=True)
class WritePlan:
    """Hold original and candidate contents for an all-or-nothing write."""

    root: Path
    originals: Mapping[Path, str]
    candidates: Mapping[Path, str]

    @property
    def changed_paths(self) -> tuple[Path, ...]:
        """Return destinations whose candidate differs from its original."""

        return tuple(
            path
            for path, candidate in self.candidates.items()
            if self.originals[path] != candidate
        )


def _load_plan_files(
    root: Path, updates: Iterable[AnchoredUpdate | FencedUpdate]
) -> tuple[dict[Path, str], dict[Path, str]]:
    """Load each update target once and seed its in-memory candidate."""

    originals: dict[Path, str] = {}
    candidates: dict[Path, str] = {}
    for update in updates:
        path = root / update.rel_path
        if path not in originals:
            if not path.is_file():
                raise VersioningError(
                    f"{update.desc}: file not found: {update.rel_path}"
                )
            originals[path] = path.read_text()
            candidates[path] = originals[path]
    return originals, candidates


def build_write_plan(
    root: Path,
    anchored: Sequence[AnchoredUpdate],
    fenced: Sequence[FencedUpdate],
) -> WritePlan:
    """Preflight every update and compute every candidate without writing."""

    all_updates: list[AnchoredUpdate | FencedUpdate] = [*anchored, *fenced]
    originals, candidates = _load_plan_files(root, all_updates)

    for update in anchored:
        path = root / update.rel_path
        content = candidates[path]
        count = len(re.findall(update.pattern, content, flags=re.MULTILINE))
        if count != update.expect:
            raise VersioningError(
                f"{update.desc}: anchor count {count}, expected "
                f"{update.expect}; pattern={update.pattern!r}"
            )
        candidates[path] = re.sub(
            update.pattern,
            update.replacement,
            content,
            flags=re.MULTILINE,
        )

    for update in fenced:
        path = root / update.rel_path
        content = candidates[path]
        start = rf"^\s*(?:#|//|<!--) *sync:{re.escape(update.name)}:start\b[^\n]*$"
        end = rf"^\s*(?:#|//|<!--) *sync:{re.escape(update.name)}:end\b[^\n]*$"
        if (
            len(re.findall(start, content, re.MULTILINE)) != 1
            or len(re.findall(end, content, re.MULTILINE)) != 1
        ):
            raise VersioningError(
                f"{update.desc}: fenced region sync:{update.name} "
                "must have exactly one start and one end"
            )
        pattern = re.compile(
            rf"(?ms)^(\s*(?:#|//|<!--) *sync:{re.escape(update.name)}:start\b[^\n]*)\n"
            rf".*?"
            rf"^(\s*(?:#|//|<!--) *sync:{re.escape(update.name)}:end\b[^\n]*)$"
        )
        body = update.body
        if body and not body.endswith("\n"):
            body += "\n"
        candidates[path], count = pattern.subn(
            lambda match, body=body: f"{match.group(1)}\n{body}{match.group(2)}",
            content,
            count=1,
        )
        if count != 1:
            raise VersioningError(
                f"{update.desc}: could not replace fenced region sync:{update.name}"
            )

    return WritePlan(
        root=root,
        originals=MappingProxyType(originals),
        candidates=MappingProxyType(candidates),
    )


ReplaceFn = Callable[
    [
        str | bytes | os.PathLike[str] | os.PathLike[bytes],
        str | bytes | os.PathLike[str] | os.PathLike[bytes],
    ],
    None,
]


def _stage_content(path: Path, content: str) -> Path:
    """Write one candidate beside its destination for atomic replacement."""

    mode = path.stat().st_mode
    fd, name = tempfile.mkstemp(
        prefix=f".{path.name}.", suffix=".versioning", dir=path.parent
    )
    staged = Path(name)
    try:
        with os.fdopen(fd, "w") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(staged, mode)
        return staged
    except BaseException:
        staged.unlink(missing_ok=True)
        raise


def apply_write_plan(plan: WritePlan, *, replace: ReplaceFn = os.replace) -> None:
    """Commit a preflighted plan and restore originals on a write failure."""

    changed = plan.changed_paths
    if not changed:
        return
    staged: dict[Path, Path] = {}
    committed: list[Path] = []
    try:
        for path in changed:
            staged[path] = _stage_content(path, plan.candidates[path])

        for path in changed:
            replace(staged[path], path)
            committed.append(path)
    except BaseException as error:
        restore_errors: list[str] = []
        for path in reversed(committed):
            try:
                restore = _stage_content(path, plan.originals[path])
                # Cleanup also owns this file if restoration fails.
                staged[path] = restore
                os.replace(restore, path)
            # Rollback is best-effort and must report every failed restoration.
            except BaseException as restore_error:  # noqa: BLE001
                restore_errors.append(f"{path}: {restore_error}")
        suffix = (
            f"; rollback failures: {'; '.join(restore_errors)}"
            if restore_errors
            else ""
        )
        raise VersioningError(f"transactional write failed: {error}{suffix}") from error
    finally:
        for staged_path in staged.values():
            staged_path.unlink(missing_ok=True)


def unified_diff(plan: WritePlan, path: Path) -> str:
    """Render the planned change for one path as a compact unified diff."""

    rel = path.relative_to(plan.root)
    return "".join(
        difflib.unified_diff(
            plan.originals[path].splitlines(keepends=True),
            plan.candidates[path].splitlines(keepends=True),
            fromfile=f"a/{rel}",
            tofile=f"b/{rel}",
            n=2,
        )
    )
