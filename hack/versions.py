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
# requires-python = ">=3.12"
# dependencies = [
#     "packaging==26.3",
#     "PyYAML==6.0.3",
#     "requests==2.34.2",
# ]
# ///
"""Check, apply, and synchronize repository dependency versions."""

from __future__ import annotations

import sys
from pathlib import Path

sys.dont_write_bytecode = True

from version_tool.cli import main as _main  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parent.parent

if __name__ == "__main__":
    sys.exit(_main(repo_root=REPO_ROOT))
