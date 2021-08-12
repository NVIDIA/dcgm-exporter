#!/usr/bin/env bash
set -euox pipefail

helm repo index helm-charts --url https://nvidia.github.io/dcgm-exporter/helm-charts
