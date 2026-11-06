<div align="center">

<img src="assets/logo.svg" alt="DCGM Exporter logo" width="180" />

# DCGM Exporter

**NVIDIA hardware telemetry for Prometheus.**

<a href="https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html">Get started</a>
·
<a href="https://docs.nvidia.com/datacenter/dcgm/latest/reference/command-line-reference/dcgm-exporter.html">Command reference</a>
·
<a href="deployment/README.md">Deploy with Helm</a>
·
<a href="CONTRIBUTING.md">Contribute</a>

<p>
  <a href="https://github.com/NVIDIA/dcgm-exporter/actions/workflows/go.yml"><img alt="GitHub Actions build" src="https://github.com/NVIDIA/dcgm-exporter/actions/workflows/go.yml/badge.svg?branch=main" /></a>
  <a href="https://app.codecov.io/gh/NVIDIA/dcgm-exporter"><img alt="Codecov coverage" src="https://img.shields.io/codecov/c/github/NVIDIA/dcgm-exporter/main" /></a>
  <a href="https://github.com/NVIDIA/dcgm-exporter/releases"><img alt="Latest release" src="https://img.shields.io/github/v/release/NVIDIA/dcgm-exporter?display_name=tag&amp;sort=semver" /></a>
  <a href="https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html"><img alt="NVIDIA documentation" src="https://img.shields.io/badge/docs-NVIDIA-76B900?logo=nvidia&amp;logoColor=white" /></a>
  <a href="https://catalog.ngc.nvidia.com/orgs/nvidia/teams/k8s/containers/dcgm-exporter"><img alt="NGC container image" src="https://img.shields.io/badge/container-NGC-76B900?logo=nvidia&amp;logoColor=white" /></a>
  <a href="LICENSE"><img alt="License: Apache-2.0" src="https://img.shields.io/badge/License-Apache--2.0-blue.svg" /></a>
</p>

</div>

---

## Overview

DCGM Exporter exposes NVIDIA GPU telemetry from [NVIDIA Data Center GPU
Manager (DCGM)](https://developer.nvidia.com/dcgm) in the Prometheus text
format. This repository contains the exporter source, container and package
builds, Helm chart, tests, and a Grafana dashboard.

## Documentation

Use the official NVIDIA documentation for user guidance:

- [Install DCGM Exporter](https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html)
- [Configure Prometheus for DCGM Exporter](https://docs.nvidia.com/datacenter/dcgm/latest/learn/getting-started-for-system-administrators/configure-prometheus-for-dcgm-exporter.html)
- [`dcgm-exporter` command reference](https://docs.nvidia.com/datacenter/dcgm/latest/reference/command-line-reference/dcgm-exporter.html)
- [DCGM Exporter Metrics](https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html)
- [DCGM Exporter Release Notes](https://docs.nvidia.com/datacenter/dcgm/latest/release-notes/dcgm-exporter.html)

The installation guide covers deployment, configuration, verification,
administration, and troubleshooting.

## Version compatibility

Run DCGM Exporter with the DCGM version paired with that exporter release.
See [Version Compatibility](https://docs.nvidia.com/datacenter/dcgm/latest/installation/install-dcgm-exporter.html#dcgm-exporter-version-compatibility)
for supported package and remote-connection combinations.

## Repository resources

- [Helm chart configuration and troubleshooting](deployment/README.md)
- [Contribution requirements and development workflow](CONTRIBUTING.md)
- [Kubernetes integration and end-to-end tests](tests/k8s/README.md)
- [Container tests and image examples](tests/container/README.md)
- [CLI package maintainer note](pkg/cmd/README.md)
- [Default metric definitions](etc/default-counters.csv)

## Dashboards

- Checked-in Grafana dashboard: [`grafana/dcgm-exporter-dashboard.json`](grafana/dcgm-exporter-dashboard.json)

## Contributing and support

Read [CONTRIBUTING.md](CONTRIBUTING.md) before submitting a change. For
community support, [open an issue](https://github.com/NVIDIA/dcgm-exporter/issues/new).
NVIDIA Enterprise customers can access knowledge base articles and support
cases, or [submit a ticket](https://www.nvidia.com/en-us/data-center/products/ai-enterprise-suite/support/).

Report potential security vulnerabilities through the
[NVIDIA Product Security](https://www.nvidia.com/en-us/security/) process, not
through a public issue. See [SECURITY.md](SECURITY.md) for reporting details.

## License

DCGM Exporter source is licensed under the [Apache License, Version 2.0](LICENSE).
Use of the NGC container image is also subject to the
[NVIDIA Software License Agreement](https://www.nvidia.com/en-us/agreements/enterprise-software/nvidia-software-license-agreement/).
