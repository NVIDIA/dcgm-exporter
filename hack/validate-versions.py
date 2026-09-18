#!/usr/bin/env python3
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

"""Check that public release metadata matches hack/versions.env."""

from __future__ import annotations

import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def read_versions() -> dict[str, str]:
    """Read simple KEY=VALUE entries from the release version file."""
    versions: dict[str, str] = {}
    for line in (ROOT / "hack" / "versions.env").read_text().splitlines():
        if "=" not in line or line.lstrip().startswith("#"):
            continue
        key, value = line.split("=", 1)
        versions[key] = value
    return versions


def require(pattern: str, path: str, description: str) -> None:
    """Fail when one public file does not contain the expected value."""
    text = (ROOT / path).read_text()
    if not re.search(pattern, text, flags=re.MULTILINE):
        raise ValueError(f"{description} does not match hack/versions.env: {path}")


def main() -> int:
    """Validate the public files that duplicate release version values."""
    versions = read_versions()
    required = ("GO_VERSION", "DCGM_VERSION", "EXPORTER_VERSION")
    missing = [key for key in required if not versions.get(key)]
    if missing:
        raise ValueError(f"versions.env is missing required keys: {', '.join(missing)}")

    go_version = re.escape(versions["GO_VERSION"])
    exporter_version = re.escape(versions["EXPORTER_VERSION"])
    image_tag = re.escape(
        f'{versions["DCGM_VERSION"]}-{versions["EXPORTER_VERSION"]}-distroless'
    )
    example_tag = re.escape(f'{versions["DCGM_VERSION"]}-{versions["EXPORTER_VERSION"]}')

    require(rf"^go {go_version}$", "go.mod", "Go version")
    require(rf'^version: "{exporter_version}"$', "deployment/Chart.yaml", "Chart version")
    require(rf'^appVersion: "{exporter_version}"$', "deployment/Chart.yaml", "Chart appVersion")
    require(rf"^  tag: {image_tag}$", "deployment/values.yaml", "Default image tag")
    require(example_tag, "deployment/examples/dcgm-exporter.yaml", "Deployment example image tag")
    print("Public version metadata matches hack/versions.env")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except ValueError as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(1) from error
