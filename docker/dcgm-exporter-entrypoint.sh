#!/usr/bin/env bash
set -euo pipefail

# Entrypoint for dcgm-exporter
# Capability checking is done in Go code (internal/pkg/capabilities)

exec /usr/bin/dcgm-exporter "$@"
