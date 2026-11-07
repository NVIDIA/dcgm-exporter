# etc

Shipped metric collector CSV files and the default configuration file for dcgm-exporter.

| File | Role |
|------|------|
| `config.yaml` | Default configuration file; same settings as running with no file and no flags |
| `default-counters.csv` | Default collectors |
| `dcp-metrics-included.csv` | Deprecated. Identical copy of `default-counters.csv`, kept so existing paths keep working; use `default-counters.csv` instead. To be removed in a later release |
| `1.x-compatibility-metrics.csv` | Deprecated. Metric names from dcgm-exporter 1.x; use `default-counters.csv` instead and update dashboards and alerts to its metric names. To be removed in a later release |

**Install destinations**

- `task install`: `default-counters.csv` and `config.yaml` → `/etc/dcgm-exporter/`
- Binary packages (`//packaging:dcgm_exporter_tar`): all three CSVs and `config.yaml` → `/etc/dcgm-exporter/`
- Container images: entire `etc/` → `/etc/dcgm-exporter/`

Runtime default path is `/etc/dcgm-exporter/default-counters.csv` (`--collectors` / `-f`).

**Default configuration file**

- Container images set `DCGM_EXPORTER_CONFIG_FILE=/etc/dcgm-exporter/config.yaml`, and the packaged
  systemd unit sets the same variable.
- Flags and environment variables override the file. A `--config-file` argument or another
  `DCGM_EXPORTER_CONFIG_FILE` value replaces it; the two are never merged.
- Mounting a volume over `/etc/dcgm-exporter` hides `config.yaml`, and the exporter then stops at
  startup with an error naming the file and `DCGM_EXPORTER_CONFIG_FILE`. Include a `config.yaml` in the
  mount, or set `DCGM_EXPORTER_CONFIG_FILE` (or `--config-file`) to a file you provide.
- The Helm chart renders its own file from its `config` value, which gives the same defaults with
  Kubernetes labels set to `auto`. With `config: null` the chart uses this file.

Row format and contract rules: [AGENTS.md](AGENTS.md).
