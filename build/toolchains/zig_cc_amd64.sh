#!/usr/bin/env bash

# Compile CGO for amd64 against the supported glibc 2.28 baseline with pinned Zig.

set -euo pipefail

exec "$(dirname "$0")/zig_cc_wrapper.sh" x86_64-linux-gnu.2.28 "$@"
