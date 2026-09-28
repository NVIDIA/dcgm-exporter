# etc

Shipped metric collector CSV files for dcgm-exporter.

| File | Role |
|------|------|
| `default-counters.csv` | Default collectors |
| `dcp-metrics-included.csv` | Alternate DCP-oriented profile |
| `1.x-compatibility-metrics.csv` | Compatibility profile |

**Install destinations**

- `make install`: only `default-counters.csv` → `/etc/dcgm-exporter/default-counters.csv`
- Binary packages (`hack/package/stage-payload.sh`): all three CSVs → `/etc/dcgm-exporter/`
- Container images: entire `etc/` → `/etc/dcgm-exporter/`

Runtime default path is `/etc/dcgm-exporter/default-counters.csv` (`--collectors` / `-f`).

Row format and contract rules: [AGENTS.md](AGENTS.md).
