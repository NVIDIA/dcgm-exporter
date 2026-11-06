#!/usr/bin/env bash

# Forward archive actions to the same pinned Zig tool selected for CGO compilation.

set -euo pipefail

exec "$(dirname "$0")/zig_cc_wrapper.sh" --ar "$@"
