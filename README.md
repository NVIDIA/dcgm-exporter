# DCGM Exporter

DCGM Exporter exposes NVIDIA GPU telemetry from [NVIDIA Data Center GPU
Manager (DCGM)](https://developer.nvidia.com/dcgm) in the Prometheus text
format. This repository contains the exporter source, container and package
builds, Helm chart, tests, and dashboards.

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
- [Default metric collectors](etc/default-counters.csv)

## Dashboards

- [NVIDIA DCGM Exporter dashboard for Grafana](https://grafana.com/grafana/dashboards/12239/)
- Checked-in Grafana dashboard: [`grafana/dcgm-exporter-dashboard.json`](grafana/dcgm-exporter-dashboard.json)
- Community [OpenObserve dashboard](https://github.com/openobserve/dashboards/tree/main/NVIDIA%20GPU%20Monitoring)
  and [integration guide](https://openobserve.ai/blog/how-to-monitor-nvidia-gpu/)

## Contributing and support

Read [CONTRIBUTING.md](CONTRIBUTING.md) before submitting a change. For
community support, [open an issue](https://github.com/NVIDIA/dcgm-exporter/issues/new).

Report potential security vulnerabilities through the
[NVIDIA Product Security](https://www.nvidia.com/en-us/security/) process, not
through a public issue. See [SECURITY.md](SECURITY.md) for reporting details.
