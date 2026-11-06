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

"""Implementation modules for the stable ``hack/versions.py`` command.

``pins`` owns registry definitions and validation. ``rendering`` turns pins
into canonical and derived file contents. ``writes`` preflights and applies
transactional updates. ``upstream`` owns HTTP and Go version discovery. ``cli``
coordinates those operations and receives the repository root from the script.

Dependencies point inward: rendering, writes, and upstream may use pin rules;
the CLI calls all four modules. No operation module imports the CLI.
"""
