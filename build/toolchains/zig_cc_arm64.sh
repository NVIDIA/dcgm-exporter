#!/usr/bin/env bash

# Compile CGO for arm64 against the supported glibc 2.28 baseline with pinned Zig.

set -euo pipefail

exec "$(dirname "$0")/zig_cc_wrapper.sh" aarch64-linux-gnu.2.28 "$@"
