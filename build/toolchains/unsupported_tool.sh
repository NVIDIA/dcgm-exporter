#!/usr/bin/env bash

# Reject standalone C/C++ tool actions that the exporter CGO toolchain does not support.

set -euo pipefail

echo "unsupported standalone C/C++ tool in DCGM Exporter Bazel toolchain" >&2
exit 1
